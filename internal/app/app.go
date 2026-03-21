package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/x0lie/pia-tun/internal/api"
	"github.com/x0lie/pia-tun/internal/apperrors"
	"github.com/x0lie/pia-tun/internal/cacher"
	"github.com/x0lie/pia-tun/internal/dns"
	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/metrics"
	"github.com/x0lie/pia-tun/internal/monitor"
	"github.com/x0lie/pia-tun/internal/portforward"
	"github.com/x0lie/pia-tun/internal/portsync"
	"github.com/x0lie/pia-tun/internal/proxy"
	"github.com/x0lie/pia-tun/internal/vpn"
	"github.com/x0lie/pia-tun/internal/wan"
	"github.com/x0lie/pia-tun/internal/wg"
	"golang.org/x/sync/errgroup"
)

type App struct {
	// Config (set once, read-only)
	cfg Config

	// Connection state
	connInfo      *vpn.ConnectionInfo
	connectionUp  atomic.Bool
	exitedCleanly bool
	preVPNIP      string

	// Infrastructure
	log      *log.Logger
	fw       *firewall.Firewall
	cache    *cacher.Cache
	resolver *dns.Resolver
	metrics  *metrics.Metrics
	wan      *wan.Checker
	api      *api.Server
	dotProxy *dns.Proxy
}

// Run is the main entry point for the orchestrated VPN client.
// It manages the full lifecycle: initialization, connection, service management,
// reconnection, and cleanup.
func Run(ctx context.Context) error {
	a := &App{
		cfg:   LoadConfig(),
		log:   log.New("app"),
		cache: &cacher.Cache{},
		wan:   &wan.Checker{},
	}
	a.logConfig()
	if err := a.cfg.validate(); err != nil {
		log.Error(err.Error())
		return err
	}

	if err := a.initialize(ctx); err != nil {
		log.Error(err.Error())
		return err
	}
	defer a.cleanup()

	// Initial connection with wan-aware retry
	if err := a.retryWithWANCheck(ctx, a.connect); err != nil {
		log.Error(err.Error())
		return err
	}

	a.showMonitorStatus()
	if a.cfg.Proxy.Enabled {
		a.showProxyStatus()
	}

	for {
		err := a.runServices(ctx)
		if ctx.Err() != nil {
			a.exitedCleanly = true
			return nil // graceful shutdown (SIGTERM)
		}

		if errors.Is(err, apperrors.ErrReconnect) {
			log.Info("")
			log.Error(err.Error())

			log.ReconnectingBanner()
			a.wan.WaitForUp(ctx, a.metrics)

			if err := a.retryWithWANCheck(ctx, a.connect); err != nil {
				log.Error(err.Error())
				return err
			}
			a.metrics.RecordReconnect()
			continue
		}

		log.Info("")
		log.Error(err.Error())
		return err
	}
}

// initialize performs one-time setup (killswitch, services scoped to container lifetime, etc.)
func (a *App) initialize(ctx context.Context) error {
	log.StartupBanner(a.cfg.Version, a.cfg.SHA)
	var err error

	// If DNS != "system", backup and clear /etc/resolv.conf to prevent leaks to LOCAL_NETWORKS
	if a.cfg.DNSMode != "system" {
		if err = dns.Backup(); err != nil {
			return err
		}
		if err := dns.Clear(); err != nil {
			return err
		}
		a.log.Debug("/etc/resolv.conf moved to /etc/resolv.bak")
	}

	// Initialize firewall
	a.fw, err = firewall.New(a.cfg.FW.Backend)
	if err != nil {
		return err
	}

	// Setup Killswitch
	if err = a.fw.Setup(firewall.KillswitchConfig{LANs: a.cfg.FW.LANs, IPv6Enabled: a.cfg.VPN.IPv6Enabled}); err != nil {
		return err
	}

	// Run Metrics Collector
	a.metrics = metrics.New(a.cfg.Metrics, a.cfg.Version)
	if a.cfg.Metrics.Enabled {
		go a.metrics.RunCollector(ctx, a.fw)
	}

	// Start API server
	a.api = api.New(a.cfg.Metrics.Port, a.fw.IsActive, a.connectionUp.Load, a.metrics)
	go a.api.Start()

	// Start Proxy server
	if a.cfg.Proxy.Enabled {
		go func() {
			if err = proxy.Run(ctx, a.cfg.Proxy); err != nil {
				log.Error("Proxy server error: %v", err)
			}
		}()
	}

	a.resolver = dns.NewResolver(a.fw)

	if err := a.retryWithWANCheck(ctx, a.setupDNS); err != nil {
		return err
	}

	// Non-fatal: capture pre-VPN IP for leak detection
	a.preVPNIP = a.captureRealIP(ctx)

	return nil
}

// connect runs a single connection attempt using the Go VPN orchestrator.
// Fatal errors cause exit (returns apperrors.ErrFatal)
func (a *App) connect(ctx context.Context) error {
	cfg := vpn.Config{
		PIAUser:    a.cfg.PIA.User,
		PIAPass:    a.cfg.PIA.Pass,
		Location:   a.cfg.PIA.Location,
		PFRequired: a.cfg.PF.Enabled,
		ManualCN:   a.cfg.PIA.CN,
		ManualIP:   a.cfg.PIA.IP,
		MTU:        a.cfg.VPN.MTU,
		IPv6:       a.cfg.VPN.IPv6Enabled,
		WGBackend:  a.cfg.VPN.Backend,
	}

	// Connect - Setup VPN
	connInfo, err := vpn.Setup(ctx, cfg, a.fw, a.cache, a.resolver)
	if err != nil {
		return err
	}
	a.connInfo = connInfo
	a.metrics.RecordNewConnection(connInfo.ServerCN, connInfo.ServerIP)

	// Write PIA DNS if enabled
	if a.cfg.DNSMode == "pia" {
		if err := dns.Write(a.connInfo.DNS); err != nil {
			return err
		}
		if err := a.fw.AddPIADNSRoutes(a.connInfo.DNS); err != nil {
			return err
		}
	}

	// Verify connection
	log.Step("Verifying connection...")
	if err := vpn.VerifyConnection(ctx, a.cfg.DNSMode, a.connInfo.DNS, a.preVPNIP); err != nil {
		return err
	}

	// Signal success
	a.connectionUp.Store(true)
	a.metrics.UpdateConnectionStatus(true)
	log.ConnectedBanner()

	return nil
}

// runServices starts services that track with VPN lifecycle. They run as goroutines managed
// by errgroup. Returns ErrReconnect for reconnection request and nil on graceful shutdown
func (a *App) runServices(ctx context.Context) error {
	g, gCtx := errgroup.WithContext(ctx)

	// Monitor - verifies tunnel working
	g.Go(func() error {
		return monitor.Run(gCtx, &a.cfg.Monitor, a.metrics, a.connInfo.ServerIP)
	})

	// Cacher - refreshes PIA login token and server list
	g.Go(func() error {
		return cacher.Run(gCtx, a.cache, a.cfg.PIA.User, a.cfg.PIA.Pass)
	})

	// Port syncer - syncs port to desired endpoint
	syncer := portsync.New(a.cfg.PS)
	if a.cfg.PS.Client != "" || a.cfg.PS.Script != "" {
		g.Go(func() error {
			return syncer.Run(gCtx)
		})
	}

	// Port forwarding - acquires port from PIA gateway
	if a.cfg.PF.Enabled {
		connCfg := portforward.ConnectionConfig{
			ClientIP:  a.connInfo.ClientIP,
			ServerCN:  a.connInfo.ServerCN,
			PFGateway: a.connInfo.PFGateway,
		}
		g.Go(func() error {
			return portforward.Run(gCtx, &a.cfg.PF, &connCfg, a.cache, a.metrics, syncer, a.fw)
		})
	}

	// Wait for error (reconnect or fatal)
	err := g.Wait()

	if ctx.Err() != nil {
		return nil // parent SIGTERM
	}

	// Signal down state
	a.connectionUp.Store(false)
	a.metrics.UpdateConnectionStatus(false)

	// Clear PIA DNS or close DoT upstream connections if enabled
	switch a.cfg.DNSMode {
	case "pia":
		if err := dns.Clear(); err != nil {
			log.Warning("Failed to clear resolv.conf: %v", err)
		}
	case "dot":
		if a.cfg.DNSMode == "dot" {
			a.dotProxy.CloseUpstreams() // Keeps VerifyConnection from hanging on dead connections
		}
	}

	return err
}

func (a *App) setupDNS(ctx context.Context) error {
	switch a.cfg.DNSMode {
	case "pia":
		return nil
	case "system":
		log.Step("Continuing with system DNS:")
		resolvServers, err := dns.Read()
		if err != nil {
			return fmt.Errorf("%w: %v", apperrors.ErrFatal, err)
		}
		log.Success(strings.Join(resolvServers, ", "))
		return nil
	case "do53":
		log.Step("Writing DNS to resolv.conf...")
		if err := dns.Write(a.cfg.DNS); err != nil {
			return fmt.Errorf("%w: %v", apperrors.ErrFatal, err)
		}
		log.Success(strings.Join(a.cfg.DNS, ", "))
		return nil
	case "dot":
		log.Step("Starting DoT proxy on port 53...")
		a.dotProxy = dns.New(a.cfg.DNS, a.resolver)
		if err := a.dotProxy.Setup(ctx); err != nil {
			return err
		}
		log.Success(a.dotProxy.Display())
		return nil
	default:
		return fmt.Errorf("unknown DNS mode: %s", a.cfg.DNSMode)
	}
}

// retryWithWANCheck retries givenFunction() with exponential backoff until it succeeds, the context is cancelled, or
// the givenFunction() returns apperrors.ErrFatal - all other errors logged and retried
func (a *App) retryWithWANCheck(ctx context.Context, fn func(context.Context) error) error {
	delay := 5 * time.Second
	const maxDelay = 60 * time.Second

	for {
		err := fn(ctx)
		if err == nil {
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Fatal errors
		if errors.Is(err, apperrors.ErrFatal) {
			return err
		}

		// Non-fatal errors
		log.Warning(err.Error())
		if !a.wan.Check(ctx) {
			a.wan.WaitForUp(ctx, a.metrics)
			delay = 5 * time.Second
			continue
		}
		log.Warning("Will retry in %s", delay)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

func (a *App) showMonitorStatus() {
	log.Step("Health monitor running...")
	log.Success("Check interval: %ds, Failure window: %ds",
		int(a.cfg.Monitor.Interval.Seconds()),
		int(a.cfg.Monitor.FailureWindow.Seconds()))

	port := a.cfg.Metrics.Port
	log.Success("Endpoints on localhost:%d", port)

	if a.cfg.Metrics.Enabled {
		log.Info("    /ready /health /metrics /metrics?format=json")
	} else {
		log.Info("    /ready /health")
	}
}

func (a *App) showProxyStatus() {
	log.Step("Proxy server running...")

	if a.cfg.Proxy.User != "" && a.cfg.Proxy.Pass != "" {
		log.Success("Proxy servers ready (authenticated):")
		log.Info("    SOCKS5: socks5://%s:****@<container-ip>:%d", a.cfg.Proxy.User, a.cfg.Proxy.Socks5Port)
		log.Info("    HTTP:   http://%s:****@<container-ip>:%d", a.cfg.Proxy.User, a.cfg.Proxy.HTTPPort)
	} else {
		log.Success("Proxy servers ready:")
		log.Info("    SOCKS5: socks5://<container-ip>:%d", a.cfg.Proxy.Socks5Port)
		log.Info("    HTTP:   http://<container-ip>:%d", a.cfg.Proxy.HTTPPort)
	}
}

func (a *App) cleanup() {
	log.Step("Shutting down...")
	a.api.Shutdown()
	wg.Down(context.Background())

	if a.exitedCleanly {
		a.fw.Cleanup()
		dns.Restore()
	} else {
		log.Warning("Killswitch preserved due to error exit")
	}
	log.Success("Cleanup complete")
}

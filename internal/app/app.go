package app

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/x0lie/pia-tun/internal/pia"
	"github.com/x0lie/pia-tun/internal/portforward"
	"github.com/x0lie/pia-tun/internal/portsync"
	"github.com/x0lie/pia-tun/internal/proxy"
	"github.com/x0lie/pia-tun/internal/vpn"
	"github.com/x0lie/pia-tun/internal/wan"
	"github.com/x0lie/pia-tun/internal/wg"
	"golang.org/x/sync/errgroup"
)

// App holds the application state and configuration.
type App struct {
	// Config (set once, read-only)
	cfg Config

	// Runtime state
	cache         *cacher.Cache
	fw            *firewall.Firewall
	connInfo      *vpn.ConnectionInfo
	connectionUp  atomic.Bool
	exitedCleanly bool
	metrics       *metrics.Metrics
	api           *api.Server
	preVPNIP      string

	// Infrastructure
	log      *log.Logger
	resolver *dns.Resolver
	wan      *wan.Checker
}

// Run is the main entry point for the orchestrated VPN client.
// It manages the full lifecycle: initialization, connection, service management,
// reconnection, and cleanup.
func Run(ctx context.Context) error {
	a := &App{
		cfg:   LoadConfig(),
		cache: &cacher.Cache{},
		log:   log.New("app"),
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

	// Initial connection with retry
	if err := a.connectLoop(ctx); err != nil {
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

			if err := a.connectLoop(ctx); err != nil {
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

// initialize validates config, clears stale state, sets up the killswitch, and configures DNS.
func (a *App) initialize(ctx context.Context) error {
	log.StartupBanner(a.cfg.Version, a.cfg.SHA)
	var err error

	// If DNS != "system", clear /etc/resolv.conf, otherwise set it to a.cfg.DNS
	if a.cfg.DNSMode != "system" {
		if err = dns.Clear(); err != nil {
			return err
		}
		a.log.Debug("/etc/resolv.conf cleared")
	} else {
		if a.cfg.DNS, err = dns.Read(); err != nil {
			return err
		}
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
				log.Error(fmt.Sprintf("Proxy server error: %v", err))
			}
		}()
	}

	a.wan = &wan.Checker{}
	a.resolver = dns.NewResolver(a.fw)

	// Write custom DNS
	if a.cfg.DNSMode == "custom" {
		if err = dns.Write(a.cfg.DNS); err != nil {
			return err
		}
	}

	// Non-fatal: capture pre-VPN IP for leak detection
	a.preVPNIP = a.captureRealIP(ctx)

	return nil
}

// connect runs a single connection attempt using the Go VPN orchestrator.
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
		return err // Error type (AuthError/ConnectivityError) preserved for connectLoop
	}
	a.connInfo = connInfo
	a.log.Debug("Connected to %s (%s) in %s, latency %dms",
		connInfo.ServerCN, connInfo.ServerIP, connInfo.Location, connInfo.Latency.Milliseconds())
	a.metrics.RecordNewConnection(connInfo.ServerCN, connInfo.ServerIP)

	// Write PIA DNS if enabled
	if a.cfg.DNSMode == "pia" {
		a.cfg.DNS = a.connInfo.DNS
		if err := dns.Write(a.cfg.DNS); err != nil {
			return err
		}
		if err := a.fw.AddPIADNSRoutes(a.cfg.DNS); err != nil {
			return err
		}
	}

	// Verify connection
	log.Step("Verifying connection...")
	if err := vpn.VerifyConnection(ctx, a.cfg.DNSMode, a.cfg.DNS, a.preVPNIP); err != nil {
		return err
	}

	// Signal success
	a.connectionUp.Store(true)
	a.metrics.UpdateConnectionStatus(true)
	log.ConnectedBanner()

	return nil
}

// connectLoop retries connect() with exponential backoff until it succeeds or the context is cancelled.
// Returns immediately on AuthError and LocationError (bad config - fatal).
func (a *App) connectLoop(ctx context.Context) error {
	delay := 5 * time.Second
	const maxDelay = 60 * time.Second

	for {
		err := a.connect(ctx)
		if err == nil {
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// AuthError is fatal - bad credentials, don't retry
		if _, isAuth := err.(*pia.AuthError); isAuth {
			log.Warning("Check PIA_USER/PASS or secrets pia_user/pass")
			return err
		}

		// LocationError is fatal - no servers available, don't retry
		if _, isLocation := err.(*pia.LocationError); isLocation {
			return err
		}

		// ConnectivityError is nonfatal - wait for wan or retry with backoff
		log.Error(fmt.Sprintf("%v", err))
		if !a.wan.Check(ctx) {
			a.wan.WaitForUp(ctx, a.metrics)
			delay = 5 * time.Second
			continue
		}
		log.Warning(fmt.Sprintf("Will retry in %s", delay))

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

// runServices starts services that track with vpn lifecycle. They run as goroutines managed
// by errgroup. Services that track with container lifecycle start in initialize. Returns
// ErrReconnect for reconnection request and nil on graceful shutdown
func (a *App) runServices(ctx context.Context) error {
	g, gCtx := errgroup.WithContext(ctx)

	// Monitor - always runs (verifies tunnel working)
	g.Go(func() error {
		return monitor.Run(gCtx, &a.cfg.Monitor, a.metrics, a.connInfo.ServerIP)
	})

	// Cacher - always runs (refreshes PIA login token and server list)
	g.Go(func() error {
		return cacher.Run(gCtx, a.cache, a.cfg.PIA.User, a.cfg.PIA.Pass)
	})

	// Port syncer - conditional
	syncer := portsync.New(a.cfg.PS)
	if a.cfg.PS.Client != "" || a.cfg.PS.Script != "" {
		g.Go(func() error {
			return syncer.Run(gCtx)
		})
	}

	// Port forwarding - conditional
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

	// Clear PIA DNS if enabled
	if a.cfg.DNSMode == "pia" {
		if err := dns.Clear(); err != nil {
			log.Warning(fmt.Sprintf("Failed to clear resolv.conf: %v", err))
		}
	}

	return err
}

func (a *App) showMonitorStatus() {
	log.Step("Health monitor running...")
	log.Success(fmt.Sprintf("Check interval: %ds, Failure window: %ds",
		int(a.cfg.Monitor.Interval.Seconds()),
		int(a.cfg.Monitor.FailureWindow.Seconds())))

	port := a.cfg.Metrics.Port
	log.Success(fmt.Sprintf("Endpoints on localhost:%d", port))

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
		log.Info(fmt.Sprintf("    SOCKS5: socks5://%s:****@<container-ip>:%d", a.cfg.Proxy.User, a.cfg.Proxy.Socks5Port))
		log.Info(fmt.Sprintf("    HTTP:   http://%s:****@<container-ip>:%d", a.cfg.Proxy.User, a.cfg.Proxy.HTTPPort))
	} else {
		log.Success("Proxy servers ready:")
		log.Info(fmt.Sprintf("    SOCKS5: socks5://<container-ip>:%d", a.cfg.Proxy.Socks5Port))
		log.Info(fmt.Sprintf("    HTTP:   http://<container-ip>:%d", a.cfg.Proxy.HTTPPort))
	}
}

func (a *App) cleanup() {
	log.Step("Shutting down...")
	a.api.Shutdown()
	wg.Down(context.Background(), a.log)

	if a.cfg.DNSMode == "pia" && a.connInfo != nil {
		dns.Clear()
	}

	if a.exitedCleanly {
		a.fw.Cleanup()
	} else {
		log.Warning("Killswitch preserved due to error exit")
	}
	log.Success("Cleanup complete")
}

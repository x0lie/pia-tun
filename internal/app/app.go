package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/x0lie/pia-tun/internal/api"
	"github.com/x0lie/pia-tun/internal/cacher"
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

// ErrReconnect is a sentinel error indicating a service has requested VPN reconnection.
var ErrReconnect = errors.New("reconnect requested")

// App holds the application state and configuration.
type App struct {
	// Config (set once, read-only)
	cfg Config

	// Runtime state
	monitorState  *monitor.State
	cache         *vpn.CacheState
	fw            *firewall.Firewall
	connInfo      *vpn.ConnectionInfo
	connectionUp  atomic.Bool
	exitedCleanly bool
	metrics       *metrics.Metrics
	api           *api.Server

	// Infrastructure
	log      *log.Logger
	resolver *pia.Resolver
	wan      *wan.Checker
}

// Run is the main entry point for the orchestrated VPN client.
// It manages the full lifecycle: initialization, connection, service management,
// reconnection, and cleanup.
func Run(ctx context.Context) error {
	a := &App{
		cfg:          LoadConfig(),
		monitorState: monitor.NewState(),
		cache:        &vpn.CacheState{},
		log:          log.New("app"),
	}

	a.logConfig()
	log.StartupBanner(a.cfg.Version, a.cfg.SHA)

	if err := a.initialize(ctx); err != nil {
		return fmt.Errorf("initialization failed: %w", err)
	}
	defer a.cleanup()

	// Initial connection with retry
	if err := a.connectLoop(ctx); err != nil {
		return err
	}

	a.connectionUp.Store(true)
	a.metrics.UpdateConnectionStatus(true)
	reconnectCh := make(chan struct{}, 1)

	// Permanent services (persist across reconnects, use parent ctx)
	a.startMonitor(ctx, reconnectCh)
	if a.cfg.Proxy.Enabled {
		a.startProxy(ctx)
	}

	for {
		a.monitorState.Resume()
		err := a.runServices(ctx, reconnectCh)
		if ctx.Err() != nil {
			a.exitedCleanly = true
			return nil // graceful shutdown (SIGTERM)
		}

		a.connectionUp.Store(false)
		a.metrics.UpdateConnectionStatus(false)
		if errors.Is(err, ErrReconnect) {
			a.log.Debug("Services requested reconnect")

			a.monitorState.Pause()
			log.ReconnectingBanner()
			a.teardown()

			a.metrics.RecordReconnect()
			a.wan.WaitForUp(ctx, a.metrics)

			if err := a.connectLoop(ctx); err != nil {
				return err
			}
			a.connectionUp.Store(true)
			a.metrics.UpdateConnectionStatus(true)
			continue
		}

		return fmt.Errorf("services exited unexpectedly: %w", err)
	}
}

// initialize validates config, clears stale state, sets up the killswitch, and configures DNS.
func (a *App) initialize(ctx context.Context) error {
	// Validate required credentials early
	if a.cfg.PIA.User == "" || a.cfg.PIA.Pass == "" {
		log.Error("PIA credentials not configured")
		log.Error("Set PIA_USER and PIA_PASS environment variables, or use Docker secrets at /run/secrets/pia_user and /run/secrets/pia_pass")
		return fmt.Errorf("PIA credentials not configured")
	}
	if a.cfg.PIA.Location == "" && a.cfg.PIA.CN == "" {
		log.Error("PIA_LOCATION not configured")
		log.Error("Set PIA_LOCATION to a region ID (e.g., 'us_california', 'uk_london')")
		return fmt.Errorf("PIA_LOCATION not configured")
	}

	exec.CommandContext(ctx, "ip", "link", "delete", "pia0").Run()

	if err := checkCapNetAdmin(); err != nil {
		log.Error("Container missing CAP_NET_ADMIN capability")
		log.Error("Required for firewall management. Add '--cap-add=NET_ADMIN'")
		return err
	}
	a.log.Debug("CAP_NET_ADMIN check passed")

	// Initialize firewall
	fw, err := firewall.New(a.cfg.FW.Backend)
	if err != nil {
		log.Error("Failed to initialize firewall")
		return fmt.Errorf("firewall init: %w", err)
	}
	a.fw = fw
	a.resolver = pia.NewResolver(fw, a.log)

	if err := a.fw.Setup(firewall.KillswitchConfig{LANs: a.cfg.FW.LANs, IPv6Enabled: a.cfg.VPN.IPv6Enabled}); err != nil {
		log.Error("CRITICAL: Killswitch setup failed - cannot safely connect to VPN")
		return fmt.Errorf("killswitch setup failed: %w", err)
	}

	// Set up RFC1918/ULA bypass routes so LOCAL_NETWORKS traffic uses default gateway, not pia0
	if err := a.fw.SetupPrivateRoutes(); err != nil {
		log.Error("Failed to setup private network routes")
		return fmt.Errorf("private network routes: %w", err)
	}

	// Configure DNS once after killswitch is up
	if err := a.fw.AddPIADNSRoutes(a.cfg.FW.DNS); err != nil {
		log.Warning(fmt.Sprintf("Failed to add PIA DNS routes: %v", err))
	}
	a.writeDNS()

	a.metrics = metrics.New(a.cfg.Metrics, a.cfg.Version)
	if a.cfg.Metrics.Enabled {
		go a.metrics.RunCollector(ctx, a.fw)
	}

	a.api = api.New(a.cfg.Metrics.Port, a.fw.IsActive, a.connectionUp.Load, a.metrics)
	go a.api.Start()

	a.wan = &wan.Checker{}

	// Non-fatal: capture pre-VPN IP for leak detection
	a.captureRealIP(ctx)

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
	if err := a.fw.AddPFRoute(a.cfg.PF.Enabled, a.connInfo.PFGateway); err != nil {
		log.Warning(fmt.Sprintf("Failed to add PF gateway route: %v", err))
	}

	// Verify connection (non-fatal)
	log.Step("Verifying connection...")
	if publicIP, err := vpn.VerifyConnection(ctx); err == nil {
		log.Success(fmt.Sprintf("External IP: %s%s%s%s", log.ColorGreen, log.ColorBold, publicIP, log.ColorReset))
	} else {
		log.Warning(fmt.Sprintf("%v", err))
	}
	log.ConnectedBanner()

	return nil
}

// connectLoop retries connect() with exponential backoff until it succeeds or the context is cancelled.
// Returns immediately on AuthError (bad credentials - fatal).
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
			log.Error("Authentication failed - check PIA_USER and PIA_PASS")
			return err
		}

		// LocationError is fatal - no servers available, don't retry
		if _, isLocation := err.(*pia.LocationError); isLocation {
			log.Error(err.Error())
			log.Warning("Check PIA_LOCATION")
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

// runServices starts transient services (cacher, port forwarding, port monitor) as
// goroutines managed by errgroup. Monitor and proxy are permanent services started
// separately. Returns ErrReconnect if reconnection was requested, nil on graceful shutdown.
func (a *App) runServices(ctx context.Context, reconnectCh chan struct{}) error {
	// Drain any stale reconnect signal from the previous cycle
	select {
	case <-reconnectCh:
	default:
	}

	svcCtx, svcCancel := context.WithCancelCause(ctx)
	defer svcCancel(nil)

	g, gCtx := errgroup.WithContext(svcCtx)

	// Cacher - always runs (refreshes PIA login token and server list)
	g.Go(func() error {
		return cacher.Run(gCtx, a.cache, a.cfg.PIA.User, a.cfg.PIA.Pass)
	})

	syncer := portsync.New(a.cfg.PS)
	if a.cfg.PS.Client != "" || a.cfg.PS.Script != "" {
		g.Go(func() error {
			return syncer.Run(gCtx)
		})
	}

	// Port forwarding - conditional
	if a.cfg.PF.Enabled {
		pfReconnect := func() {
			svcCancel(ErrReconnect)
		}
		connCfg := portforward.ConnectionConfig{
			Token:     a.connInfo.Token,
			ClientIP:  a.connInfo.ClientIP,
			ServerCN:  a.connInfo.ServerCN,
			PFGateway: a.connInfo.PFGateway,
		}
		g.Go(func() error {
			return portforward.Run(gCtx, &a.cfg.PF, &connCfg, pfReconnect, a.metrics, syncer, a.fw)
		})
	}

	errCh := make(chan error, 1)
	go func() { errCh <- g.Wait() }()

	select {
	case <-reconnectCh:
		// Monitor requested reconnect
		svcCancel(nil)
		<-errCh
		return ErrReconnect
	case err := <-errCh:
		if ctx.Err() != nil {
			return nil // parent SIGTERM
		}
		if errors.Is(context.Cause(svcCtx), ErrReconnect) {
			return ErrReconnect
		}
		return fmt.Errorf("services exited: %w", err)
	}
}

func (a *App) showStatus() {
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

// startProxy launches the SOCKS5/HTTP proxy as a background goroutine.
// It uses the parent context so the proxy persists across VPN reconnects
// and shuts down only on SIGTERM.
func (a *App) startProxy(ctx context.Context) {
	log.Step("Starting proxy servers...")

	if a.cfg.Proxy.User != "" && a.cfg.Proxy.Pass != "" {
		log.Success("Proxy servers ready (authenticated):")
		log.Info(fmt.Sprintf("    SOCKS5: socks5://%s:****@<container-ip>:%d", a.cfg.Proxy.User, a.cfg.Proxy.Socks5Port))
		log.Info(fmt.Sprintf("    HTTP:   http://%s:****@<container-ip>:%d", a.cfg.Proxy.User, a.cfg.Proxy.HTTPPort))
	} else {
		log.Success("Proxy servers ready:")
		log.Info(fmt.Sprintf("    SOCKS5: socks5://<container-ip>:%d", a.cfg.Proxy.Socks5Port))
		log.Info(fmt.Sprintf("    HTTP:   http://<container-ip>:%d", a.cfg.Proxy.HTTPPort))
	}

	go func() {
		if err := proxy.Run(ctx, a.cfg.Proxy); err != nil {
			log.Error(fmt.Sprintf("Proxy server error: %v", err))
		}
	}()
}

// startMonitor launches the health monitor as a permanent background goroutine.
// It uses the parent context so the monitor persists across VPN reconnects.
// Reconnect requests are signaled via reconnectCh.
func (a *App) startMonitor(ctx context.Context, reconnectCh chan<- struct{}) {
	log.Step("Starting health monitor...")

	reconnect := func() {
		select {
		case reconnectCh <- struct{}{}:
		default:
		}
	}

	go func() {
		if err := monitor.Run(ctx, &a.cfg.Monitor, reconnect, a.monitorState, a.metrics); err != nil {
			log.Error(fmt.Sprintf("Health monitor error: %v", err))
		}
	}()

	a.showStatus()
}

func (a *App) teardown() {
	a.log.Debug("Tearing down VPN tunnel")
	// Remove VPN from killswitch first to prevent leak window
	a.fw.RemoveVPN()
	wg.Down(context.Background(), a.log)
	a.fw.RemovePFRoute(a.connInfo.PFGateway)
	a.connInfo = nil
}

func (a *App) cleanup() {
	log.Step("Shutting down...")
	a.api.Shutdown()

	if a.connInfo != nil {
		a.teardown()
	}
	a.fw.CleanupPrivateRoutes()
	a.fw.RemovePIADNSRoutes()
	if a.exitedCleanly {
		a.fw.Cleanup()
	} else {
		log.Warning("Killswitch preserved due to error exit")
	}
	log.Success("Cleanup complete")
}

// PIA DNS servers (used when DNS="pia" or empty)
var piaDNSServers = []string{"10.0.0.243", "10.0.0.242"}

// writeDNS writes /etc/resolv.conf based on the DNS configuration.
func (a *App) writeDNS() {
	dns := a.cfg.FW.DNS
	if dns == "none" {
		a.log.Debug("DNS disabled (DNS=none)")
		return
	}

	var servers []string
	if dns == "" || dns == "pia" {
		servers = piaDNSServers
		a.log.Debug("Using PIA DNS: %v", servers)
	} else {
		for _, s := range strings.Split(dns, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				servers = append(servers, s)
			}
		}
		a.log.Debug("Using custom DNS: %v", servers)
	}

	if len(servers) == 0 {
		return
	}

	var buf strings.Builder
	buf.WriteString("# Set by pia-tun\n")
	for _, s := range servers {
		buf.WriteString("nameserver ")
		buf.WriteString(s)
		buf.WriteString("\n")
	}

	if err := os.WriteFile("/etc/resolv.conf", []byte(buf.String()), 0644); err != nil {
		a.log.Debug("Failed to write /etc/resolv.conf: %v", err)
	}
}

// Environment setup (called before LoadConfig)

func checkCapNetAdmin() error {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fmt.Errorf("cannot read /proc/self/status: %w", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			hex := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
			capEff, err := strconv.ParseUint(hex, 16, 64)
			if err != nil {
				return fmt.Errorf("cannot parse CapEff: %w", err)
			}
			const capNetAdmin = 1 << 12
			if capEff&capNetAdmin == 0 {
				return fmt.Errorf("missing CAP_NET_ADMIN")
			}
			return nil
		}
	}

	return fmt.Errorf("CapEff not found in /proc/self/status")
}

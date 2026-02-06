package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/cacher"
	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/monitor"
	"github.com/x0lie/pia-tun/internal/pia"
	"github.com/x0lie/pia-tun/internal/portforward"
	"github.com/x0lie/pia-tun/internal/proxy"
	"github.com/x0lie/pia-tun/internal/vpn"
	"github.com/x0lie/pia-tun/internal/wg"
	"golang.org/x/sync/errgroup"
)

// ErrReconnect is a sentinel error indicating a service has requested VPN reconnection.
var ErrReconnect = errors.New("reconnect requested")

// shellPreamble sources all shell scripts to make their functions available.
const shellPreamble = "set -euo pipefail; source /app/scripts/ui.sh; source /app/scripts/killswitch.sh; source /app/scripts/verify_connection.sh; "

// App holds the application state and configuration.
type App struct {
	cfg          Config
	log          *log.Logger
	monitorState *monitor.State
	cache        *vpn.CacheState
	fw           *firewall.Firewall
	resolver     *pia.Resolver
	connInfo     *vpn.ConnectionInfo
}

// Run is the main entry point for the orchestrated VPN client.
// It manages the full lifecycle: initialization, connection, service management,
// reconnection, and cleanup.
func Run(ctx context.Context) error {
	setupAutoEnable()
	setupLogLevel(os.Getenv("LOG_LEVEL"))
	cfg := LoadConfig()

	logger := &log.Logger{
		Enabled: os.Getenv("_LOG_LEVEL") == "2",
		Prefix:  "app",
	}

	a := &App{
		cfg: cfg,
		log: logger,
		monitorState: &monitor.State{
			ConnInfo: make(chan monitor.ConnectionInfo, 1),
		},
		cache: &vpn.CacheState{},
	}

	a.shellFunc(ctx, "print_banner")
	a.logConfig()

	if err := a.initialize(ctx); err != nil {
		return fmt.Errorf("initialization failed: %w", err)
	}
	defer a.cleanup()

	// Initial connection with retry
	if err := a.connectLoop(ctx); err != nil {
		return err
	}

	reconnectCh := make(chan struct{}, 1)

	// Permanent services (persist across reconnects, use parent ctx)
	if a.cfg.ProxyEnabled {
		a.startProxy(ctx)
	}
	a.startMonitor(ctx, reconnectCh)

	// Main loop: run services, reconnect on failure
	reconnecting := false

	for {
		if reconnecting {
			if a.cfg.ProxyEnabled {
				log.Step("Proxy servers still running")
				log.Success("Proxy servers ready")
			}
			log.Step("Health monitor resuming...")
			a.showStatus(false)
		}

		err := a.runServices(ctx, reconnectCh)
		if ctx.Err() != nil {
			return nil // graceful shutdown (SIGTERM)
		}

		if errors.Is(err, ErrReconnect) {
			a.log.Debug("Services requested reconnect")

			// Pause health monitor immediately, before teardown begins
			a.monitorState.Paused.Store(true)

			a.shellFunc(ctx, "show_reconnecting")
			a.monitorState.Reconnecting.Store(true)
			a.teardown()
			if err := a.connectLoop(ctx); err != nil {
				return err
			}
			reconnecting = true
			continue
		}

		return fmt.Errorf("services exited unexpectedly: %w", err)
	}
}

// initialize validates config, clears stale state, sets up the killswitch, and configures DNS.
func (a *App) initialize(ctx context.Context) error {
	// Validate required credentials early
	if a.cfg.PIAUser == "" || a.cfg.PIAPass == "" {
		log.Error("PIA credentials not configured")
		log.Error("Set PIA_USER and PIA_PASS environment variables, or use Docker secrets at /run/secrets/pia_user and /run/secrets/pia_pass")
		return fmt.Errorf("PIA credentials not configured")
	}
	if a.cfg.PIALocation == "" && a.cfg.PIACN == "" {
		log.Error("PIA_LOCATION not configured")
		log.Error("Set PIA_LOCATION to a region ID (e.g., 'us_california', 'uk_london')")
		return fmt.Errorf("PIA_LOCATION not configured")
	}

	a.log.Debug("Removing stale flag files")
	for _, f := range []string{
		"/tmp/monitor_up",
		"/tmp/killswitch_up",
	} {
		os.Remove(f)
	}

	exec.CommandContext(ctx, "ip", "link", "delete", "pia0").Run()

	if err := checkCapNetAdmin(); err != nil {
		log.Error("Container missing CAP_NET_ADMIN capability")
		log.Error("Required for firewall management. Add '--cap-add=NET_ADMIN' to docker run")
		return err
	}
	a.log.Debug("CAP_NET_ADMIN check passed")

	// Initialize firewall
	fw, err := firewall.New(a.log)
	if err != nil {
		log.Error("Failed to initialize firewall")
		return fmt.Errorf("firewall init: %w", err)
	}
	a.fw = fw
	a.resolver = pia.NewResolver(fw, a.log)
	a.log.Debug("Firewall initialized (backend: %s)", fw.Backend())

	if err := a.shellFunc(ctx, "setup_baseline_killswitch"); err != nil {
		log.Error("CRITICAL: Killswitch setup failed - cannot safely connect to VPN")
		return fmt.Errorf("killswitch setup failed: %w", err)
	}

	// Set up local network bypass routes (once, not on reconnect)
	if a.cfg.LocalNetworks != "" {
		networks := parseNetworkList(a.cfg.LocalNetworks)
		if err := a.fw.SetupLocalNetworkRoutes(networks); err != nil {
			log.Error("Failed to setup local network routes")
			return fmt.Errorf("local network routes: %w", err)
		}
		a.log.Debug("Local network routes configured for %d networks", len(networks))
	}

	// Configure DNS once after killswitch is up
	a.writeDNS()

	// Non-fatal: capture pre-VPN IP for leak detection
	a.captureRealIP(ctx)

	return nil
}

// connect runs a single connection attempt using the Go VPN orchestrator.
func (a *App) connect(ctx context.Context) error {
	log.Step("Establishing VPN connection...")

	cfg := vpn.SetupConfig{
		PIAUser:    a.cfg.PIAUser,
		PIAPass:    a.cfg.PIAPass,
		Location:   a.cfg.PIALocation,
		PFRequired: a.cfg.PFEnabled,
		ManualCN:   a.cfg.PIACN,
		ManualIP:   a.cfg.PIAIP,
		MTU:        a.cfg.MTU,
		IPv6:       a.cfg.IPv6Enabled,
		WGBackend:  a.cfg.WGBackend,
	}

	connInfo, err := vpn.Setup(ctx, cfg, a.fw, a.cache, a.resolver, a.log)
	if err != nil {
		return err // Error type (AuthError/ConnectivityError) preserved for connectLoop
	}
	a.connInfo = connInfo

	// Write connection info to /tmp/ files for portforward/cacher backward compat
	a.writeConnectionFiles()

	log.Success("VPN tunnel established")
	a.log.Debug("Connected to %s (%s) in %s, latency %dms",
		connInfo.ServerCN, connInfo.ServerIP, connInfo.Location, connInfo.Latency.Milliseconds())

	// Verify connection (non-fatal)
	log.Step("Verifying connection...")
	if err := a.runScript(ctx, "/app/scripts/verify_connection.sh"); err != nil {
		a.shellFunc(ctx, "show_vpn_connected_warning")
		return nil
	}

	a.shellFunc(ctx, "show_vpn_connected")
	return nil
}

// connectLoop retries connect() with exponential backoff until it succeeds or the context is cancelled.
// Returns immediately on AuthError (bad credentials - fatal).
func (a *App) connectLoop(ctx context.Context) error {
	delay := 5 * time.Second
	const maxDelay = 120 * time.Second

	a.monitorState.Paused.Store(true)
	defer a.monitorState.Paused.Store(false)

	for {
		err := a.connect(ctx)
		if err == nil {
			a.monitorState.Reconnecting.Store(false)
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// AuthError is fatal - bad credentials, don't retry
		if _, isAuth := err.(*pia.AuthError); isAuth {
			log.Blank()
			log.Error("Authentication failed - check PIA_USER and PIA_PASS")
			return err
		}

		log.Blank()
		log.Error(fmt.Sprintf("Connection failed: %v", err))
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
		return cacher.Run(gCtx, a.cache)
	})

	// Port forwarding - conditional
	if a.cfg.PFEnabled {
		pfReady := make(chan struct{})
		pfReconnect := func() {
			svcCancel(ErrReconnect)
		}
		g.Go(func() error {
			return portforward.Run(gCtx, pfReconnect, pfReady)
		})

		select {
		case <-pfReady:
			a.log.Debug("Port forwarding ready")
		case <-gCtx.Done():
		}
	}

	// Port monitor script (torrent client API sync) - conditional
	if a.cfg.PSClient != "" || a.cfg.PSCmd != "" {
		log.Step("Port sync starting...")
		g.Go(func() error {
			return a.runScript(gCtx, "/app/scripts/port_monitor.sh")
		})
	}

	// Signal connection info to metrics pipe
	if a.cfg.MetricsEnabled {
		a.signalConnectionReady()
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

// signalConnectionReady sends connection info to the metrics listener.
func (a *App) signalConnectionReady() {
	if a.connInfo == nil {
		return
	}

	select {
	case a.monitorState.ConnInfo <- monitor.ConnectionInfo{
		Server: a.connInfo.ServerCN,
		IP:     a.connInfo.ServerIP,
	}:
	default:
		a.log.Debug("Connection info channel full, skipping")
	}
}

func (a *App) showStatus(verbose bool) {
	log.Success(fmt.Sprintf("Check interval: %ds, Failure window: %ds",
		int(a.cfg.HealthCheckInterval.Seconds()),
		int(a.cfg.HealthFailureWindow.Seconds())))

	if !verbose {
		return
	}

	port := a.cfg.MetricsPort
	if a.cfg.MetricsEnabled {
		log.Success(fmt.Sprintf("Metrics available on port %d", port))
		log.Info(fmt.Sprintf("    Prometheus:  http://<container-ip>:%d/metrics", port))
		log.Info(fmt.Sprintf("    JSON:        http://<container-ip>:%d/metrics?format=json", port))
		log.Info(fmt.Sprintf("    Health:      http://<container-ip>:%d/health", port))
	} else {
		log.Info(fmt.Sprintf("    Health:      http://<container-ip>:%d/health", port))
	}
}

// startProxy launches the SOCKS5/HTTP proxy as a background goroutine.
// It uses the parent context so the proxy persists across VPN reconnects
// and shuts down only on SIGTERM.
func (a *App) startProxy(ctx context.Context) {
	log.Step("Starting proxy servers...")

	if a.cfg.ProxyUser != "" && a.cfg.ProxyPass != "" {
		log.Success("Proxy servers ready (authenticated):")
		log.Info(fmt.Sprintf("    SOCKS5: socks5://%s:****@<container-ip>:%d", a.cfg.ProxyUser, a.cfg.Socks5Port))
		log.Info(fmt.Sprintf("    HTTP:   http://%s:****@<container-ip>:%d", a.cfg.ProxyUser, a.cfg.HTTPProxyPort))
	} else {
		log.Success("Proxy servers ready:")
		log.Info(fmt.Sprintf("    SOCKS5: socks5://<container-ip>:%d", a.cfg.Socks5Port))
		log.Info(fmt.Sprintf("    HTTP:   http://<container-ip>:%d", a.cfg.HTTPProxyPort))
	}

	go func() {
		if err := proxy.Run(ctx); err != nil {
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
		if err := monitor.Run(ctx, reconnect, a.monitorState); err != nil {
			log.Error(fmt.Sprintf("Health monitor error: %v", err))
		}
	}()

	a.showStatus(true)
}

func (a *App) teardown() {
	a.log.Debug("Tearing down VPN tunnel")
	// Remove VPN from killswitch first to prevent leak window
	a.fw.RemoveVPN()
	wg.Down(context.Background(), a.log)
	a.connInfo = nil
}

func (a *App) cleanup() {
	log.Step("Shutting down...")

	for _, f := range []string{
		"/tmp/monitor_up",
		"/tmp/killswitch_up",
	} {
		os.Remove(f)
	}

	// Use background context since parent ctx is likely cancelled
	bgCtx := context.Background()
	a.fw.RemoveVPN()
	wg.Down(bgCtx, a.log)
	a.fw.CleanupLocalNetworkRoutes()
	a.shellFunc(bgCtx, "cleanup_killswitch")

	log.Success("Cleanup complete")
}

func (a *App) runScript(ctx context.Context, script string) error {
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (a *App) shellFunc(ctx context.Context, funcCall string) error {
	cmd := exec.CommandContext(ctx, "bash", "-c", shellPreamble+funcCall)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// PIA DNS servers (used when DNS="pia" or empty)
var piaDNSServers = []string{"10.0.0.243", "10.0.0.242"}

// writeDNS writes /etc/resolv.conf based on the DNS configuration.
func (a *App) writeDNS() {
	dns := a.cfg.DNS
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

func setupLogLevel(level string) {
	switch strings.ToLower(level) {
	case "debug", "2":
		os.Setenv("_LOG_LEVEL", "2")
	case "error", "0":
		os.Setenv("_LOG_LEVEL", "0")
	default:
		os.Setenv("_LOG_LEVEL", "1")
	}
}

func setupAutoEnable() {
	if os.Getenv("PS_CLIENT") != "" || os.Getenv("PS_CMD") != "" {
		os.Setenv("PS_ENABLED", "true")
		os.Setenv("PF_ENABLED", "true")
	}
}

func (a *App) logConfig() {
	a.log.Debug("Environment configuration:")
	a.log.Debug("  PF_ENABLED=%v", a.cfg.PFEnabled)
	a.log.Debug("  IPV6_ENABLED=%v", a.cfg.IPv6Enabled)
	a.log.Debug("  DNS=%s", a.cfg.DNS)
	a.log.Debug("  LOCAL_NETWORKS=%s", a.cfg.LocalNetworks)
	a.log.Debug("  LOCAL_PORTS=%s", a.cfg.LocalPorts)
	a.log.Debug("  HC_INTERVAL=%s", a.cfg.HealthCheckInterval)
	a.log.Debug("  HC_FAILURE_WINDOW=%s", a.cfg.HealthFailureWindow)
	a.log.Debug("  PROXY_ENABLED=%v", a.cfg.ProxyEnabled)
	a.log.Debug("  METRICS=%v", a.cfg.MetricsEnabled)
	a.log.Debug("  LOG_LEVEL=%s", a.cfg.LogLevel)
}

// writeConnectionFiles writes connection info to /tmp/ files for backward compat
// with portforward and other components that still read from temp files.
func (a *App) writeConnectionFiles() {
	if a.connInfo == nil {
		return
	}

	files := map[string]string{
		"/tmp/pia_login_token": a.connInfo.Token,
		"/tmp/client_ip":       a.connInfo.ClientIP,
		"/tmp/pia_cn":          a.connInfo.ServerCN,
		"/tmp/pf_gateway":      a.connInfo.PFGateway,
	}

	for path, content := range files {
		if content == "" {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			a.log.Debug("Failed to write %s: %v", path, err)
		}
	}
}

// parseNetworkList splits a comma-separated list of networks into a slice.
func parseNetworkList(networks string) []string {
	var result []string
	for _, net := range strings.Split(networks, ",") {
		net = strings.TrimSpace(net)
		if net != "" {
			result = append(result, net)
		}
	}
	return result
}

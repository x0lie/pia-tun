package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/x0lie/pia-tun/internal/cacher"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/monitor"
	"github.com/x0lie/pia-tun/internal/portforward"
	"github.com/x0lie/pia-tun/internal/proxy"
	"golang.org/x/sync/errgroup"
)

// ErrReconnect is a sentinel error indicating a service has requested VPN reconnection.
var ErrReconnect = errors.New("reconnect requested")

// shellPreamble sources all shell scripts to make their functions available.
const shellPreamble = "set -euo pipefail; source /app/scripts/ui.sh; source /app/scripts/killswitch.sh; source /app/scripts/vpn.sh; source /app/scripts/verify_connection.sh; "

// App holds the application state and configuration.
type App struct {
	cfg Config
	log *log.Logger
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

	a := &App{cfg: cfg, log: logger}

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
			os.WriteFile("/tmp/monitor_wait", []byte(""), 0644)

			a.shellFunc(ctx, "show_reconnecting")
			os.WriteFile("/tmp/reconnecting", []byte(""), 0644)
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

// initialize clears stale flag files, sets up the killswitch, and captures the real IP.
func (a *App) initialize(ctx context.Context) error {
	a.log.Debug("Removing stale flag files")
	for _, f := range []string{
		"/tmp/port_forwarding_complete",
		"/tmp/reconnecting",
		"/tmp/monitor_up",
		"/tmp/killswitch_up",
	} {
		os.Remove(f)
	}

	exec.CommandContext(ctx, "ip", "link", "delete", "pia0").Run()
	os.WriteFile("/etc/resolv.conf", []byte("\n"), 0644)

	if err := checkCapNetAdmin(); err != nil {
		log.Error("Container missing CAP_NET_ADMIN capability")
		log.Error("Required for firewall management. Add '--cap-add=NET_ADMIN' to docker run")
		return err
	}
	a.log.Debug("CAP_NET_ADMIN check passed")

	if err := a.shellFunc(ctx, "setup_baseline_killswitch"); err != nil {
		log.Error("CRITICAL: Killswitch setup failed - cannot safely connect to VPN")
		return fmt.Errorf("killswitch setup failed: %w", err)
	}

	// Create named pipe for metrics connection signaling
	if a.cfg.MetricsEnabled {
		os.Remove("/tmp/vpn_connection_pipe")
		syscall.Mkfifo("/tmp/vpn_connection_pipe", 0644)
	}

	// Non-fatal: capture pre-VPN IP for leak detection
	a.shellFunc(ctx, "capture_real_ip")

	return nil
}

// connect runs a single connection attempt: VPN setup, WireGuard interface, killswitch, verification.
func (a *App) connect(ctx context.Context) error {
	// Clear DNS config before each connection attempt so stale VPN DNS doesn't interfere
	os.WriteFile("/etc/resolv.conf", []byte("\n"), 0644)

	// vpn.sh does DNS resolution, PIA authentication, server selection, WG config generation
	if err := a.runScript(ctx, "/app/scripts/vpn.sh"); err != nil {
		return fmt.Errorf("vpn.sh failed: %w", err)
	}

	log.Step("Establishing VPN connection...")
	if err := a.shellFunc(ctx, "bring_up_wireguard /etc/wireguard/pia0.conf"); err != nil {
		return fmt.Errorf("bring_up_wireguard failed: %w", err)
	}
	log.Success("VPN tunnel established")

	if err := a.shellFunc(ctx, "add_vpn_to_killswitch"); err != nil {
		log.Error("CRITICAL: Failed to add VPN to killswitch")
		a.shellFunc(ctx, "teardown_wireguard")
		return fmt.Errorf("add_vpn_to_killswitch failed: %w", err)
	}

	a.shellFunc(ctx, "remove_all_temporary_exemptions")

	log.Step("Verifying connection...")
	if err := a.runScript(ctx, "/app/scripts/verify_connection.sh"); err != nil {
		a.shellFunc(ctx, "show_vpn_connected_warning")
		return nil
	}

	a.shellFunc(ctx, "show_vpn_connected")

	return nil
}

// connectLoop retries connect() with exponential backoff until it succeeds or the context is cancelled.
func (a *App) connectLoop(ctx context.Context) error {
	delay := 5 * time.Second
	const maxDelay = 120 * time.Second

	os.WriteFile("/tmp/monitor_wait", []byte(""), 0644)
	defer os.Remove("/tmp/monitor_wait")

	for {
		if err := a.connect(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
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
			continue
		}

		os.Remove("/tmp/reconnecting")
		return nil
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
		return cacher.Run(gCtx)
	})

	// Port forwarding - conditional
	if a.cfg.PFEnabled {
		pfReconnect := func() {
			svcCancel(ErrReconnect)
		}
		g.Go(func() error {
			return portforward.Run(gCtx, pfReconnect)
		})

		a.waitForPortForward(gCtx)
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

// waitForPortForward polls for the port forwarding completion flag (max 30s).
func (a *App) waitForPortForward(ctx context.Context) {
	a.log.Debug("Waiting for port forwarding completion (max 30s)")
	for i := 0; i < 30; i++ {
		if _, err := os.Stat("/tmp/port_forwarding_complete"); err == nil {
			a.log.Debug("Port forwarding completed after %ds", i)
			os.Remove("/tmp/port_forwarding_complete")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
	a.log.Debug("Port forwarding wait timed out after 30s")
}

// signalConnectionReady writes connection info to the metrics named pipe.
func (a *App) signalConnectionReady() {
	pipe := "/tmp/vpn_connection_pipe"
	if _, err := os.Stat(pipe); err != nil {
		return
	}

	server, _ := os.ReadFile("/tmp/pia_cn")
	vpnIP, _ := os.ReadFile("/tmp/server_endpoint")
	if len(server) == 0 || len(vpnIP) == 0 {
		return
	}

	data := fmt.Sprintf("%s|%s|%d",
		strings.TrimSpace(string(server)),
		strings.TrimSpace(string(vpnIP)),
		time.Now().Unix())

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-c",
			fmt.Sprintf("echo '%s' > '%s'", data, pipe))
		cmd.Run()
	}()
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
		if err := monitor.Run(ctx, reconnect); err != nil {
			log.Error(fmt.Sprintf("Health monitor error: %v", err))
		}
	}()

	a.showStatus(true)
}

func (a *App) teardown() {
	a.log.Debug("Tearing down VPN tunnel")
	a.shellFunc(context.Background(), "teardown_wireguard")
}

func (a *App) cleanup() {
	log.Step("Shutting down...")

	for _, f := range []string{
		"/tmp/port_forwarding_complete",
		"/tmp/reconnecting",
		"/tmp/monitor_up",
		"/tmp/killswitch_up",
	} {
		os.Remove(f)
	}

	// Use background context since parent ctx is likely cancelled
	bgCtx := context.Background()
	a.shellFunc(bgCtx, "teardown_wireguard")
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

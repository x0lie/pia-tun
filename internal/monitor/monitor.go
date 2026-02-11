package monitor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/x0lie/pia-tun/internal/config"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/metrics"
	"github.com/x0lie/pia-tun/internal/wan"
)

// Config holds monitor configuration.
type Config struct {
	CheckInterval  time.Duration
	FailureWindow  time.Duration
	DebugMode      bool
	MetricsEnabled bool
}

// State allows the orchestrator to communicate with the monitor
// without filesystem flags or named pipes. Nil in standalone mode.
type State struct {
	Paused       atomic.Bool // pause health checks during startup/reconnection
	Reconnecting atomic.Bool // active reconnection in progress
}

// Monitor manages VPN health monitoring.
type Monitor struct {
	config            Config
	log               *log.Logger
	reconnectAttempts int
	metrics           *metrics.Metrics
	mu                sync.Mutex

	// Reconnect callback for orchestrated mode.
	// When set, triggerReconnect calls this instead of writing to a pipe file.
	onReconnect func()

	// Orchestrator state for pause/reconnect signaling. Nil in standalone mode.
	state *State

	// Health status for /health endpoint
	healthy         bool
	lastHealthCheck time.Time

	// Wan checking
	wan *wan.Checker
}

func loadConfig() Config {
	return Config{
		CheckInterval:  config.GetEnvDuration("HC_INTERVAL", 10),
		FailureWindow:  config.GetEnvDuration("HC_FAILURE_WINDOW", 30),
		DebugMode:      config.IsDebugMode(),
		MetricsEnabled: config.GetEnvBool("METRICS"),
	}
}

func (m *Monitor) setHealthStatus(healthy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthy = healthy
	m.lastHealthCheck = time.Now()
}

func (m *Monitor) isHealthy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	staleThreshold := m.config.CheckInterval * 2
	if time.Since(m.lastHealthCheck) > staleThreshold && m.lastHealthCheck.Unix() > 0 {
		return false
	}

	return m.healthy
}

func (m *Monitor) monitorLoop(ctx context.Context) {
	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	const normalTimeout = 5 * time.Second
	const rapidTimeout = 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			m.log.Debug("Monitor loop received shutdown signal")
			return

		case <-ticker.C:
			// Check for external reconnect trigger (used by test suite)
			if _, err := os.Stat("/tmp/trigger_reconnect"); err == nil {
				os.Remove("/tmp/trigger_reconnect")
				m.log.Debug("External reconnect trigger detected")
				m.triggerReconnect(ctx)
				continue
			}

			// Skip checks during active reconnection to avoid races
			if m.state != nil && m.state.Reconnecting.Load() {
				m.log.Debug("Skipping health check during active reconnection")
				continue
			}

			// Skip checks during startup/reconnection
			if m.state != nil && m.state.Paused.Load() {
				m.log.Debug("Skipping health check during startup")
				continue
			}

			// Start server latency ping in parallel
			var serverLatencyChan chan float64
			if m.metrics != nil {
				serverLatencyChan = make(chan float64, 1)
				go func() {
					serverIP := m.getServerEndpoint()
					if serverIP == "" {
						serverLatencyChan <- -1
						return
					}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					serverLatencyChan <- m.pingServerLatency(ctx, serverIP)
				}()
			}

			// Normal health check
			result, err := m.checkVPNHealth(normalTimeout)

			m.setHealthStatus(err == nil)
			m.updateMetrics(result, err == nil)

			if m.metrics != nil {
				m.metrics.RecordCheck(err == nil, result.CheckDuration)

				if serverLatencyChan != nil {
					latency := <-serverLatencyChan
					if latency > 0 {
						m.metrics.ObserveServerLatency(latency)
					}
				}
			}

			// If check failed, enter rapid check mode
			if err != nil {
				m.log.Debug("Entering rapid check mode (failure window: %s)", m.config.FailureWindow)

				if m.config.DebugMode {
					fmt.Printf("  %s\u2139%s Debug info:\n", log.ColorYellow, log.ColorReset)
					cmd := exec.Command("wg", "show", "pia0")
					output, wgErr := cmd.Output()
					if wgErr == nil {
						lines := strings.Split(string(output), "\n")
						for i, line := range lines {
							if i >= 5 {
								break
							}
							fmt.Printf("    %s\n", line)
						}
					}
				}

				failureStart := time.Now()
				recovered := false

				for {
					// Exit rapid checks if reconnect is already in progress
					if m.state != nil && m.state.Paused.Load() {
						m.log.Debug("Reconnect in progress, exiting rapid checks")
						recovered = true
						break
					}

					rapidResult, rapidErr := m.checkVPNHealth(rapidTimeout)

					m.setHealthStatus(rapidErr == nil)
					m.updateMetrics(rapidResult, rapidErr == nil)

					if rapidErr == nil {
						m.log.Debug("Connectivity recovered during rapid checks")
						recovered = true
						break
					}

					elapsed := time.Since(failureStart)
					if elapsed >= m.config.FailureWindow {
						m.log.Debug("Rapid check failed (elapsed: %s/%s)",
							elapsed.Round(time.Second),
							m.config.FailureWindow)
						break
					}

					m.log.Debug("Rapid check failed (elapsed: %s/%s)",
						elapsed.Round(time.Second),
						m.config.FailureWindow)
				}

				if !recovered {
					fmt.Printf("\n  %s\u2717%s VPN connection lost (down for more than %s)\n",
						log.ColorRed, log.ColorReset, m.config.FailureWindow)
					m.triggerReconnect(ctx)
				}
			}
		}
	}
}

func (m *Monitor) updateMetrics(result *HealthCheckResult, healthy bool) {
	if m.metrics != nil {
		const iface = "pia0"
		rx, tx, _ := m.getTransferBytes()

		m.metrics.UpdateTransferBytes(iface, rx, tx)
		m.metrics.UpdateConnectionStatus(iface, healthy && result.InterfaceUp && result.Connectivity)
		m.metrics.UpdateKillswitchStatus(m.isKillswitchActive())
		m.metrics.UpdateLastHandshake(iface, m.getLastHandshake())

		pfActive := m.isPortForwardingActive()
		pfPort := m.getPortForwardingPort()
		m.metrics.UpdatePortForwarding(pfActive, pfPort)

		pktsIn, bytesIn, pktsOut, bytesOut := m.getKillswitchDropStats()
		m.metrics.UpdateKillswitchDrops(pktsIn, bytesIn, pktsOut, bytesOut)
	}
}

// Run starts the monitor. This is the main entry point called by the dispatcher.
// onReconnect is an optional callback for orchestrated mode. When set, the monitor
// calls it instead of writing to a pipe file when a reconnect is needed.
// state provides orchestrator pause/reconnect signaling. Pass nil for both
// in standalone mode.
func Run(ctx context.Context, onReconnect func(), state *State, wanChecker *wan.Checker, m *metrics.Metrics) error {
	cfg := loadConfig()

	logger := &log.Logger{
		Enabled: os.Getenv("_LOG_LEVEL") == "2",
		Prefix:  "monitor",
	}

	if m != nil && wanChecker != nil {
		wanChecker.Metrics = m
	}

	monitor := &Monitor{
		config:      cfg,
		log:         logger,
		metrics:     m,
		onReconnect: onReconnect,
		state:       state,
		wan:         wanChecker,
	}

	// Always start HTTP server for /health endpoint
	go startHTTPServer(monitor)

	monitor.monitorLoop(ctx)
	return nil
}

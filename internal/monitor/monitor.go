package monitor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/x0lie/pia-tun/internal/config"
	"github.com/x0lie/pia-tun/internal/log"
)

// Config holds monitor configuration.
type Config struct {
	CheckInterval  time.Duration
	FailureWindow  time.Duration
	DebugMode      bool
	MetricsEnabled bool
}

// Monitor manages VPN health monitoring.
type Monitor struct {
	config            Config
	log               *log.Logger
	reconnectAttempts int
	metrics           *Metrics
	mu                sync.Mutex

	// Health status for /health endpoint
	healthy         bool
	lastHealthCheck time.Time
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

	if m.metrics != nil {
		latency := m.getServerLatency()
		if latency > 0 {
			m.metrics.ObserveServerLatency(float64(latency) / 1000.0)
		}
	}

	for {
		select {
		case <-ctx.Done():
			m.log.Debug("Monitor loop received shutdown signal")
			return

		case <-ticker.C:
			// Check for port forwarding signature failure flag
			if _, err := os.Stat("/tmp/pf_signature_failed"); err == nil {
				if removeErr := os.Remove("/tmp/pf_signature_failed"); removeErr != nil {
					m.log.Debug("Failed to remove PF failure flag: %v", removeErr)
				}
				m.triggerReconnect()
				continue
			}

			// Skip checks during active reconnection to avoid races
			if _, err := os.Stat("/tmp/reconnecting"); err == nil {
				m.log.Debug("Skipping health check during active reconnection")
				continue
			}

			// Skip checks during startup
			if _, err := os.Stat("/tmp/monitor_wait"); err == nil {
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
					if _, pfErr := os.Stat("/tmp/pf_signature_failed"); pfErr == nil {
						os.Remove("/tmp/pf_signature_failed")
						m.log.Debug("Port forwarding failure during rapid checks")
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
					exec.Command("pkill", "-f", "portforward").Run()
					m.triggerReconnect()

					m.log.Debug("Waiting for reconnection to complete")
					reconnectTimeout := time.After(60 * time.Second)
					checkTicker := time.NewTicker(2 * time.Second)
					defer checkTicker.Stop()

					for {
						select {
						case <-reconnectTimeout:
							m.log.Debug("Reconnection wait timeout, resuming health checks")
							if m.metrics != nil {
								m.metrics.ResetSession()
								if latency := m.getServerLatency(); latency > 0 {
									m.metrics.ObserveServerLatency(float64(latency) / 1000.0)
								}
							}
							goto resumeMonitoring
						case <-checkTicker.C:
							if _, err := os.Stat("/tmp/reconnecting"); os.IsNotExist(err) {
								m.log.Debug("Reconnection complete, resuming health checks")
								time.Sleep(5 * time.Second)

								if m.metrics != nil {
									m.metrics.ResetSession()
									if latency := m.getServerLatency(); latency > 0 {
										m.metrics.ObserveServerLatency(float64(latency) / 1000.0)
									}
								}

								goto resumeMonitoring
							}
						}
					}
				resumeMonitoring:
				}
			}
		}
	}
}

func (m *Monitor) updateMetrics(result *HealthCheckResult, healthy bool) {
	if m.metrics != nil {
		const iface = "pia0"

		rx, tx, _ := m.getTransferBytes()
		server := m.getCurrentServer()

		var ip string
		if healthy {
			ip = m.getCurrentIP()
		}

		m.metrics.UpdateVPNInfo(iface, server, ip, rx, tx)
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
func Run(ctx context.Context) error {
	cfg := loadConfig()

	logger := &log.Logger{
		Enabled: cfg.DebugMode,
	}

	var metrics *Metrics
	if cfg.MetricsEnabled {
		metrics = NewMetrics()
	}

	monitor := &Monitor{
		config:  cfg,
		log:     logger,
		metrics: metrics,
	}

	// Always start HTTP server for /health endpoint
	go startHTTPServer(monitor)

	if cfg.MetricsEnabled {
		metrics.StartConnectionPipeListener(logger)
	}

	monitor.monitorLoop(ctx)
	return nil
}

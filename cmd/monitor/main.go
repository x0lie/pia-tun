package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	CheckInterval  time.Duration
	FailureWindow  time.Duration
	DebugMode      bool
	MetricsEnabled bool
}

type Monitor struct {
	config            Config
	reconnectAttempts int
	metrics           *Metrics
	mu                sync.Mutex
}

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorBlue   = "\033[0;34m"
	colorYellow = "\033[0;33m"
)

func loadConfig() Config {
	getEnvInt := func(key string, defaultVal int) int {
		if val := os.Getenv(key); val != "" {
			if i, err := strconv.Atoi(val); err == nil {
				return i
			}
		}
		return defaultVal
	}

	getEnvDuration := func(key string, defaultVal int) time.Duration {
		return time.Duration(getEnvInt(key, defaultVal)) * time.Second
	}

	getEnvBool := func(key string) bool {
		return os.Getenv(key) == "true"
	}

	return Config{
		CheckInterval:  getEnvDuration("HC_INTERVAL", 10),
		FailureWindow:  getEnvDuration("HC_FAILURE_WINDOW", 30),
		DebugMode:      getEnvInt("_LOG_LEVEL", 0) == 2,
		MetricsEnabled: getEnvBool("METRICS"),
	}
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	} else if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

func (m *Monitor) debugLog(format string, args ...interface{}) {
	if m.config.DebugMode {
		timestamp := time.Now().Format("15:04:05")
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintf(os.Stderr, "    %s[DEBUG]%s %s - %s\n", colorBlue, colorReset, timestamp, msg)
	}
}

func (m *Monitor) showSuccess(msg string) {
	fmt.Printf("  %s✓%s %s\n", colorGreen, colorReset, msg)
}

func (m *Monitor) showError(msg string) {
	fmt.Printf("  %s✗%s %s\n", colorRed, colorReset, msg)
}

func (m *Monitor) monitorLoop(ctx context.Context) {
	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	const normalTimeout = 5 * time.Second
	const rapidTimeout = 2 * time.Second

	if m.metrics != nil {
		latency := m.getServerLatency()
		if latency > 0 {
			// Convert milliseconds to seconds
			m.metrics.ObserveServerLatency(float64(latency) / 1000.0)
		}
	}

	for {
		select {
		case <-ctx.Done():
			m.debugLog("Monitor loop received shutdown signal")
			return

		case <-ticker.C:
			// Check for port forwarding signature failure flag
			if _, err := os.Stat("/tmp/pf_signature_failed"); err == nil {
				if removeErr := os.Remove("/tmp/pf_signature_failed"); removeErr != nil {
					m.debugLog("Failed to remove PF failure flag: %v", removeErr)
				}
				m.triggerReconnect()
				continue
			}

			// Skip checks during active reconnection to avoid races
			if _, err := os.Stat("/tmp/reconnecting"); err == nil {
				m.debugLog("Skipping health check during active reconnection")
				continue
			}

			// Start server latency ping in parallel (only during normal checks, not rapid)
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

			m.updateMetrics(result, err == nil)

			if m.metrics != nil {
				m.metrics.RecordCheck(err == nil, result.CheckDuration)

				// Collect server latency result (non-blocking, already completed or nearly done)
				if serverLatencyChan != nil {
					latency := <-serverLatencyChan
					if latency > 0 {
						m.metrics.ObserveServerLatency(latency)
					}
				}
			}

			// If check failed, enter rapid check mode
			if err != nil {
				m.debugLog("Entering rapid check mode (failure window: %s)", m.config.FailureWindow)

				if m.config.DebugMode {
					fmt.Printf("  %sℹ%s Debug info:\n", colorYellow, colorReset)
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

				// Rapid check loop - timeout provides natural 2s interval when failing
				for {
					// Check for manual reconnect triggers
					if _, pfErr := os.Stat("/tmp/pf_signature_failed"); pfErr == nil {
						os.Remove("/tmp/pf_signature_failed")
						m.debugLog("Port forwarding failure during rapid checks")
						break
					}

					rapidResult, rapidErr := m.checkVPNHealth(rapidTimeout)

					m.updateMetrics(rapidResult, rapidErr == nil)

					if rapidErr == nil {
						m.debugLog("Connectivity recovered during rapid checks")
						recovered = true
						break
					}

					// Check if we've exceeded the failure window
					elapsed := time.Since(failureStart)
					if elapsed >= m.config.FailureWindow {
						m.debugLog("Rapid check failed (elapsed: %s/%s)",
							elapsed.Round(time.Second),
							m.config.FailureWindow)
						break
					}

					m.debugLog("Rapid check failed (elapsed: %s/%s)",
						elapsed.Round(time.Second),
						m.config.FailureWindow)
				}

				// If still failing after failure window, reconnect
				if !recovered {
					fmt.Printf("\n  %s✗%s VPN connection lost (down for more than %s)\n",
						colorRed, colorReset, m.config.FailureWindow)
					exec.Command("pkill", "-f", "portforward").Run()
					m.triggerReconnect()

					// Wait for reconnection script to complete (poll for flag removal)
					m.debugLog("Waiting for reconnection to complete")
					reconnectTimeout := time.After(60 * time.Second)
					checkTicker := time.NewTicker(2 * time.Second)
					defer checkTicker.Stop()

					for {
						select {
						case <-reconnectTimeout:
							m.debugLog("Reconnection wait timeout, resuming health checks")
							// Still reset session metrics on timeout
							if m.metrics != nil {
								m.metrics.ResetSession()
								if latency := m.getServerLatency(); latency > 0 {
									m.metrics.ObserveServerLatency(float64(latency) / 1000.0)
								}
							}
							goto resumeMonitoring
						case <-checkTicker.C:
							if _, err := os.Stat("/tmp/reconnecting"); os.IsNotExist(err) {
								// File removed, reconnection complete
								m.debugLog("Reconnection complete, resuming health checks")
								time.Sleep(5 * time.Second) // Additional grace period for tunnel stability

								// Reset session metrics and update latency after reconnect
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

		// Only fetch public IP if connection is healthy (avoids 15s timeout when failing)
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

		// Collect killswitch drop stats
		pktsIn, bytesIn, pktsOut, bytesOut := m.getKillswitchDropStats()
		m.metrics.UpdateKillswitchDrops(pktsIn, bytesIn, pktsOut, bytesOut)
	}
}

func main() {
	config := loadConfig()

	var metrics *Metrics
	if config.MetricsEnabled {
		metrics = NewMetrics()
	}

	monitor := &Monitor{
		config:  config,
		metrics: metrics,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)

	go func() {
		<-sigChan
		monitor.debugLog("Received shutdown signal")
		cancel()
	}()

	_, err := net.LookupHost("google.com")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: DNS resolution may not be working: %v\n", err)
	}

	if config.MetricsEnabled {
		go startMetricsServer(monitor)
		// Start connection pipe listener for reliable VPN info updates
		metrics.StartConnectionPipeListener(monitor.debugLog)
	}

	monitor.monitorLoop(ctx)
}

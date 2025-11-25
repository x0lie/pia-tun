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
	CheckInterval      time.Duration
	MaxFailures        int
	RestartServices    string
	DebugMode          bool
	ParallelChecks     bool
	MetricsEnabled     bool
}

type Monitor struct {
	config             Config
	failureCount       int
	reconnectAttempts  int
	consecutiveSuccess int
	metrics            *Metrics
	mu                 sync.Mutex
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
		CheckInterval:   getEnvDuration("CHECK_INTERVAL", 15),
		MaxFailures:     getEnvInt("MAX_FAILURES", 3),
		RestartServices: os.Getenv("RESTART_SERVICES"),
		DebugMode:       getEnvInt("_LOG_LEVEL", 0) == 2,
		ParallelChecks:  getEnvBool("MONITOR_PARALLEL_CHECKS"),
		MetricsEnabled:  getEnvBool("METRICS"),
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

func (m *Monitor) showWarning(msg string) {
	fmt.Printf("  %s⚠%s %s\n", colorYellow, colorReset, msg)
}

func (m *Monitor) showError(msg string) {
	fmt.Printf("  %s✗%s %s\n", colorRed, colorReset, msg)
}

func (m *Monitor) monitorLoop(ctx context.Context) {
	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	failChan := make(chan struct{}, 1)

	if m.metrics != nil {
		latency := m.getServerLatency()
		if latency > 0 {
			m.metrics.SetServerLatency(latency)
		}
	}

	for {
		select {
		case <-ctx.Done():
			m.debugLog("Monitor loop received shutdown signal")
			return

		case <-failChan:
			m.mu.Lock()
			m.failureCount = m.config.MaxFailures
			m.consecutiveSuccess = 0
			m.mu.Unlock()
			m.triggerReconnect()
			m.mu.Lock()
			m.failureCount = 0
			m.mu.Unlock()

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
				m.mu.Lock()
				if m.failureCount > 0 {
					m.failureCount = 0 // Forgive failures during reconnect
				}
				m.mu.Unlock()
				continue
			}

			result, err := m.checkVPNHealth()

			if m.metrics != nil {
				rx, tx, _ := m.getTransferBytes()
				server := m.getCurrentServer()
				ip := m.getCurrentIP()
				m.metrics.UpdateVPNInfo(server, ip, rx, tx)
			}

			if m.metrics != nil {
				m.metrics.RecordCheck(err == nil, result.CheckDuration)
			}

			m.mu.Lock()
			if err == nil {
				if m.failureCount > 0 {
					fmt.Printf("\r%s\r", strings.Repeat(" ", 60))
					m.failureCount = 0
					m.reconnectAttempts = 0
				}
				m.consecutiveSuccess++
			} else {
				m.failureCount++
				m.consecutiveSuccess = 0

				if m.failureCount < m.config.MaxFailures {

					if m.config.DebugMode && m.failureCount == 1 {
						fmt.Printf("  %sℹ%s Debug info:\n", colorYellow, colorReset)
						cmd := exec.Command("wg", "show", "pia")
						output, err := cmd.Output()
						if err == nil {
							lines := strings.Split(string(output), "\n")
							for i, line := range lines {
								if i >= 5 {
									break
								}
								fmt.Printf("    %s\n", line)
							}
						}
					}
				} else {
					// Print the final error only when reaching MaxFailures
					fmt.Printf("\n  %s✗%s VPN connection lost (%d/%d)%s\n",
						colorRed, colorReset, m.failureCount, m.config.MaxFailures, strings.Repeat(" ", 20))
					exec.Command("pkill", "-f", "portforward").Run()
					m.mu.Unlock()
					m.triggerReconnect()
					m.mu.Lock()
					m.failureCount = 0
					m.mu.Unlock()
					m.mu.Lock()
				}
			}
			m.mu.Unlock()
		}
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
	}

	monitor.monitorLoop(ctx)
}

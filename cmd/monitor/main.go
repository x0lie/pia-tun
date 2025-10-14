package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
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
	ReconnectDelay     time.Duration
	MaxReconnectDelay  time.Duration
	RestartServices    string
	DebugMode          bool
	ParallelChecks     bool
	MetricsEnabled     bool
	FastFailMode       bool
	WatchHandshake     bool
	HandshakeTimeout   time.Duration
}

type Monitor struct {
	config             Config
	failureCount       int
	reconnectAttempts  int
	consecutiveSuccess int
	metrics            *Metrics
	mu                 sync.Mutex
}

type Metrics struct {
	TotalChecks        int64
	FailedChecks       int64
	SuccessfulChecks   int64
	TotalReconnects    int64
	LastCheckTime      time.Time
	LastCheckDuration  time.Duration
	UptimeStart        time.Time
	
	// VPN-specific metrics
	CurrentServer      string
	CurrentIP          string
	BytesReceived      int64
	BytesTransmitted   int64
	LastHandshakeTime  time.Time
	ConnectedAt        time.Time
	
	// Server performance tracking
	ServerLatency      int64  // Initial connection latency in ms
	ServerUptime       time.Duration
	
	// Performance metrics
	AvgCheckDuration   time.Duration
	MaxCheckDuration   time.Duration
	MinCheckDuration   time.Duration
	
	mu                 sync.Mutex
}

type HealthCheckResult struct {
	InterfaceUp    bool
	Connectivity   bool
	CheckDuration  time.Duration
	Error          error
}

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
		CheckInterval:     getEnvDuration("CHECK_INTERVAL", 15),
		MaxFailures:       getEnvInt("MAX_FAILURES", 2),
		ReconnectDelay:    getEnvDuration("RECONNECT_DELAY", 5),
		MaxReconnectDelay: getEnvDuration("MAX_RECONNECT_DELAY", 300),
		RestartServices:   os.Getenv("RESTART_SERVICES"),
		DebugMode:         getEnvBool("MONITOR_DEBUG"),
		ParallelChecks:    getEnvBool("MONITOR_PARALLEL_CHECKS"),
		MetricsEnabled:    getEnvBool("METRICS"),
		FastFailMode:      getEnvBool("MONITOR_FAST_FAIL"),
		WatchHandshake:    getEnvBool("MONITOR_WATCH_HANDSHAKE"),
		HandshakeTimeout:  getEnvDuration("HANDSHAKE_TIMEOUT", 180),
	}
}

func NewMetrics() *Metrics {
	return &Metrics{
		UptimeStart:      time.Now(),
		MinCheckDuration: time.Hour, // Will be replaced on first check
	}
}

func (m *Metrics) RecordCheck(success bool, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.TotalChecks++
	m.LastCheckTime = time.Now()
	m.LastCheckDuration = duration
	
	// Update duration stats
	if duration > m.MaxCheckDuration {
		m.MaxCheckDuration = duration
	}
	if duration < m.MinCheckDuration {
		m.MinCheckDuration = duration
	}
	// Running average
	if m.AvgCheckDuration == 0 {
		m.AvgCheckDuration = duration
	} else {
		m.AvgCheckDuration = (m.AvgCheckDuration*9 + duration) / 10
	}
	
	if success {
		m.SuccessfulChecks++
	} else {
		m.FailedChecks++
	}
}

func (m *Metrics) UpdateVPNInfo(server, ip string, rx, tx int64, handshake time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Track server changes and uptime
	if m.CurrentServer != server && server != "" {
		m.ConnectedAt = time.Now()
		m.CurrentServer = server
	}
	
	if m.CurrentServer != "" {
		m.ServerUptime = time.Since(m.ConnectedAt)
	}
	
	m.CurrentIP = ip
	m.BytesReceived = rx
	m.BytesTransmitted = tx
	m.LastHandshakeTime = handshake
}

func (m *Metrics) SetServerLatency(latency int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ServerLatency = latency
}

func (m *Metrics) RecordReconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TotalReconnects++
}

func (m *Metrics) GetStats() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	uptime := time.Since(m.UptimeStart)
	successRate := float64(0)
	if m.TotalChecks > 0 {
		successRate = float64(m.SuccessfulChecks) / float64(m.TotalChecks) * 100
	}
	
	timeSinceHandshake := time.Duration(0)
	if !m.LastHandshakeTime.IsZero() {
		timeSinceHandshake = time.Since(m.LastHandshakeTime)
	}
	
	return map[string]interface{}{
		// Health check metrics
		"total_checks":           m.TotalChecks,
		"successful_checks":      m.SuccessfulChecks,
		"failed_checks":          m.FailedChecks,
		"success_rate":           fmt.Sprintf("%.2f%%", successRate),
		"success_rate_decimal":   successRate / 100,
		"total_reconnects":       m.TotalReconnects,
		
		// Timing metrics
		"uptime_seconds":         int(uptime.Seconds()),
		"uptime_formatted":       formatDuration(uptime),
		"last_check":             m.LastCheckTime.Format("2006-01-02 15:04:05"),
		"last_check_duration_ms": m.LastCheckDuration.Milliseconds(),
		"avg_check_duration_ms":  m.AvgCheckDuration.Milliseconds(),
		"max_check_duration_ms":  m.MaxCheckDuration.Milliseconds(),
		"min_check_duration_ms":  m.MinCheckDuration.Milliseconds(),
		
		// VPN metrics
		"current_server":         m.CurrentServer,
		"current_ip":             m.CurrentIP,
		"bytes_received":         m.BytesReceived,
		"bytes_transmitted":      m.BytesTransmitted,
		"total_bytes":            m.BytesReceived + m.BytesTransmitted,
		"last_handshake":         m.LastHandshakeTime.Format("2006-01-02 15:04:05"),
		"handshake_age_seconds":  int(timeSinceHandshake.Seconds()),
		
		// Server performance
		"server_latency_ms":      m.ServerLatency,
		"server_uptime_seconds":  int(m.ServerUptime.Seconds()),
		"server_uptime_formatted": formatDuration(m.ServerUptime),
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

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorBlue   = "\033[0;34m"
	colorYellow = "\033[0;33m"
)

func (m *Monitor) debugLog(format string, args ...interface{}) {
	if m.config.DebugMode {
		timestamp := time.Now().Format("15:04:05")
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintf(os.Stderr, "  %s[DEBUG]%s %s - %s\n", colorBlue, colorReset, timestamp, msg)
	}
}

func (m *Monitor) getCurrentServer() string {
	data, err := os.ReadFile("/tmp/meta_cn")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (m *Monitor) getServerLatency() int64 {
	// Try to read the latency that was recorded during connection
	data, err := os.ReadFile("/tmp/server_latency")
	if err != nil {
		return 0
	}
	latency, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return latency
}

func (m *Monitor) getCurrentIP() string {
	cmd := exec.Command("ip", "addr", "show", "pia")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	
	// Parse "inet 10.x.x.x/32" from output
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "inet ") {
			fields := strings.Fields(line)
			for i, field := range fields {
				if field == "inet" && i+1 < len(fields) {
					// Remove the /32 suffix
					ip := strings.Split(fields[i+1], "/")[0]
					return ip
				}
			}
		}
	}
	return ""
}

func (m *Monitor) isInterfaceUp() bool {
	m.debugLog("Checking interface status")
	
	// Check if interface exists
	cmd := exec.Command("ip", "link", "show", "pia")
	if err := cmd.Run(); err != nil {
		m.debugLog("Interface not found")
		return false
	}

	// Check for IP address
	cmd = exec.Command("ip", "addr", "show", "pia")
	output, err := cmd.Output()
	if err == nil && strings.Contains(string(output), "inet ") {
		m.debugLog("Interface has IP address")
		return true
	}

	// Check WireGuard peers
	cmd = exec.Command("wg", "show", "pia", "peers")
	output, err = cmd.Output()
	if err == nil && len(strings.TrimSpace(string(output))) > 0 {
		m.debugLog("Interface has WireGuard peers")
		return true
	}

	// Check if interface is not DOWN
	cmd = exec.Command("ip", "link", "show", "pia")
	output, err = cmd.Output()
	if err == nil && !strings.Contains(string(output), "state DOWN") {
		m.debugLog("Interface is not DOWN")
		return true
	}

	m.debugLog("All interface checks failed")
	return false
}

func (m *Monitor) checkConnectivityPing(ctx context.Context, host string) bool {
	cmd := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "3", host)
	return cmd.Run() == nil
}

func (m *Monitor) checkConnectivityHTTP(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		return true
	}
	return false
}

// Parallel connectivity checks (new capability!)
func (m *Monitor) checkExternalConnectivityParallel() bool {
	m.debugLog("Checking external connectivity (parallel mode)")
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	type checkResult struct {
		name    string
		success bool
	}
	
	results := make(chan checkResult, 3)
	
	// Run checks in parallel
	go func() {
		success := m.checkConnectivityPing(ctx, "1.1.1.1")
		results <- checkResult{"ping-1.1.1.1", success}
	}()
	
	go func() {
		success := m.checkConnectivityPing(ctx, "8.8.8.8")
		results <- checkResult{"ping-8.8.8.8", success}
	}()
	
	go func() {
		success := m.checkConnectivityHTTP(ctx, "http://1.1.1.1")
		results <- checkResult{"http-1.1.1.1", success}
	}()
	
	// Return true if any check succeeds
	for i := 0; i < 3; i++ {
		result := <-results
		if result.success {
			m.debugLog("Connectivity check passed: %s", result.name)
			return true
		}
	}
	
	m.debugLog("All parallel connectivity checks failed")
	return false
}

func (m *Monitor) checkExternalConnectivitySerial() bool {
	m.debugLog("Checking external connectivity (serial mode)")
	
	// Try ping to 1.1.1.1
	cmd := exec.Command("ping", "-c", "1", "-W", "3", "1.1.1.1")
	if err := cmd.Run(); err == nil {
		m.debugLog("Ping to 1.1.1.1 successful")
		return true
	}

	// Try ping to 8.8.8.8
	cmd = exec.Command("ping", "-c", "1", "-W", "3", "8.8.8.8")
	if err := cmd.Run(); err == nil {
		m.debugLog("Ping to 8.8.8.8 successful")
		return true
	}

	// Try HTTP request
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := client.Get("http://1.1.1.1")
	if err == nil {
		resp.Body.Close()
		m.debugLog("HTTP request to 1.1.1.1 successful")
		return true
	}

	m.debugLog("All connectivity checks failed")
	return false
}

func (m *Monitor) checkExternalConnectivity() bool {
	if m.config.ParallelChecks {
		return m.checkExternalConnectivityParallel()
	}
	return m.checkExternalConnectivitySerial()
}

func (m *Monitor) checkVPNHealth() (*HealthCheckResult, error) {
	m.debugLog("Starting health check")
	start := time.Now()
	
	result := &HealthCheckResult{}
	
	if !m.isInterfaceUp() {
		m.debugLog("Interface check failed")
		result.CheckDuration = time.Since(start)
		result.Error = fmt.Errorf("interface is down")
		return result, result.Error
	}
	
	result.InterfaceUp = true
	m.debugLog("Interface is up")

	// In fast-fail mode, only do one quick connectivity check
	if m.config.FastFailMode {
		if m.checkExternalConnectivity() {
			result.Connectivity = true
			result.CheckDuration = time.Since(start)
			return result, nil
		}
		result.CheckDuration = time.Since(start)
		result.Error = fmt.Errorf("connectivity check failed")
		return result, result.Error
	}

	// Standard mode: Check with single retry
	if m.checkExternalConnectivity() {
		m.debugLog("Connectivity passed")
		result.Connectivity = true
		result.CheckDuration = time.Since(start)
		return result, nil
	}

	m.debugLog("First check failed, retrying...")
	time.Sleep(2 * time.Second)
	
	if m.checkExternalConnectivity() {
		m.debugLog("Retry passed")
		result.Connectivity = true
		result.CheckDuration = time.Since(start)
		return result, nil
	}

	m.debugLog("All checks failed")
	result.CheckDuration = time.Since(start)
	result.Error = fmt.Errorf("connectivity check failed")
	return result, result.Error
}

func (m *Monitor) triggerReconnect() {
	m.mu.Lock()
	m.reconnectAttempts++
	attempts := m.reconnectAttempts
	m.mu.Unlock()
	
	delay := m.config.ReconnectDelay * time.Duration(attempts)
	if delay > m.config.MaxReconnectDelay {
		delay = m.config.MaxReconnectDelay
	}

	fmt.Printf("\n%s▶%s Reconnecting in %ds...\n", colorBlue, colorReset, int(delay.Seconds()))
	time.Sleep(delay)

	// Create reconnect request file
	if err := os.WriteFile("/tmp/vpn_reconnect_requested", []byte{}, 0644); err != nil {
		log.Printf("Failed to create reconnect request: %v", err)
	}
	
	if m.metrics != nil {
		m.metrics.RecordReconnect()
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

func (m *Monitor) printMetrics() {
	if !m.config.MetricsEnabled || m.metrics == nil {
		return
	}
	
	stats := m.metrics.GetStats()
	fmt.Printf("\n%s[METRICS]%s\n", colorBlue, colorReset)
	fmt.Printf("  Uptime: %s\n", stats["uptime_formatted"])
	fmt.Printf("  Total Checks: %d (Success: %d, Failed: %d)\n", 
		stats["total_checks"], stats["successful_checks"], stats["failed_checks"])
	fmt.Printf("  Success Rate: %s\n", stats["success_rate"])
	fmt.Printf("  Reconnects: %d\n", stats["total_reconnects"])
	fmt.Printf("  Last Check: %s (Duration: %dms, Avg: %dms)\n", 
		stats["last_check"], stats["last_check_duration_ms"], stats["avg_check_duration_ms"])
	
	if server, ok := stats["current_server"].(string); ok && server != "" {
		fmt.Printf("  VPN Server: %s\n", server)
	}
	if ip, ok := stats["current_ip"].(string); ok && ip != "" {
		fmt.Printf("  VPN IP: %s\n", ip)
	}
	
	fmt.Println()
}

func (m *Monitor) getLastHandshakeTime() (time.Time, error) {
	cmd := exec.Command("wg", "show", "pia", "latest-handshakes")
	output, err := cmd.Output()
	if err != nil {
		return time.Time{}, err
	}
	
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return time.Time{}, fmt.Errorf("no handshake data")
	}
	
	// Output format: <public-key>\t<timestamp>
	parts := strings.Fields(lines[0])
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("invalid handshake format")
	}
	
	timestamp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	
	// Sanity check: if timestamp is 0 or in the future, it's invalid
	now := time.Now().Unix()
	if timestamp == 0 || timestamp > now {
		return time.Time{}, fmt.Errorf("invalid timestamp: %d", timestamp)
	}
	
	return time.Unix(timestamp, 0), nil
}

// Get transfer counters (better than handshake for detecting stale connection)
func (m *Monitor) getTransferBytes() (rx, tx int64, err error) {
	cmd := exec.Command("wg", "show", "pia", "transfer")
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, err
	}
	
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return 0, 0, fmt.Errorf("no transfer data")
	}
	
	// Output format: <public-key>\t<rx-bytes>\t<tx-bytes>
	parts := strings.Fields(lines[0])
	if len(parts) < 3 {
		// During teardown/reconnection, this is expected - not an error
		return 0, 0, fmt.Errorf("interface transitioning")
	}
	
	rx, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	
	tx, err = strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	
	return rx, tx, nil
}

// Watch for stale WireGuard connection using transfer bytes (Grok's recommendation)
func (m *Monitor) watchHandshakes(ctx context.Context, failChan chan<- struct{}) {
	if !m.config.WatchHandshake {
		return
	}
	
	m.debugLog("Starting connection watcher (using transfer bytes, timeout: %v)", m.config.HandshakeTimeout)
	
	// Wait for initial connection to stabilize
	time.Sleep(15 * time.Second)
	
	var lastRx, lastTx int64
	var staleCount int
	const maxStaleChecks = 3 // Require 3 consecutive stale checks before failing
	
	ticker := time.NewTicker(30 * time.Second) // Check every 30s (less aggressive)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Method 1: Check transfer bytes (primary method - Grok's advice)
			rx, tx, err := m.getTransferBytes()
			if err != nil {
				// During teardown/reconnection, this is normal - don't spam logs
				if !strings.Contains(err.Error(), "transitioning") {
					m.debugLog("Could not get transfer bytes: %v", err)
				}
				continue
			}
			
			// Check if bytes are increasing (keepalive packets count!)
			bytesChanged := (rx != lastRx) || (tx != lastTx)
			
			if bytesChanged {
				m.debugLog("Transfer active: rx=%d (%+d), tx=%d (%+d)", 
					rx, rx-lastRx, tx, tx-lastTx)
				lastRx = rx
				lastTx = tx
				staleCount = 0
				continue
			}
			
			// No byte changes - check handshake as backup
			lastHandshake, err := m.getLastHandshakeTime()
			if err != nil {
				m.debugLog("Could not get handshake time: %v", err)
				staleCount++
			} else {
				timeSince := time.Since(lastHandshake)
				m.debugLog("No transfer in 30s, handshake was %v ago", timeSince)
				
				// Only fail if handshake is REALLY old (3+ minutes with no bytes)
				if timeSince > m.config.HandshakeTimeout {
					staleCount++
					m.debugLog("Stale check %d/%d", staleCount, maxStaleChecks)
				} else {
					staleCount = 0
				}
			}
			
			// Trigger reconnect only after multiple consecutive failures
			if staleCount >= maxStaleChecks {
				m.showWarning(fmt.Sprintf("Connection appears stale (%d checks) - triggering reconnect", staleCount))
				select {
				case failChan <- struct{}{}:
				default:
				}
				staleCount = 0 // Reset after triggering
			}
			
			lastRx = rx
			lastTx = tx
		}
	}
}

func (m *Monitor) monitorLoop(ctx context.Context) {
	firstCheck := true
	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()
	
	// Channel for instant failure signals (from handshake watcher)
	failChan := make(chan struct{}, 1)
	
	// Start handshake watcher if enabled
	if m.config.WatchHandshake {
		go m.watchHandshakes(ctx, failChan)
	}
	
	// Capture initial server latency once
	if m.metrics != nil {
		latency := m.getServerLatency()
		if latency > 0 {
			m.metrics.SetServerLatency(latency)
		}
	}
	
	// Don't start metrics printer - it fills up logs
	// Users should use the HTTP endpoint instead: curl http://localhost:9090/metrics

	for {
		select {
		case <-ctx.Done():
			m.debugLog("Monitor loop received shutdown signal")
			return
		
		case <-failChan:
			// Instant failure from handshake watcher
			m.mu.Lock()
			m.failureCount = m.config.MaxFailures // Immediately trigger reconnect
			m.consecutiveSuccess = 0
			m.showError("Handshake timeout detected - immediate reconnect")
			m.mu.Unlock()
			m.triggerReconnect()
			m.mu.Lock()
			m.failureCount = 0
			m.mu.Unlock()
			
		case <-ticker.C:
			if firstCheck {
				firstCheck = false
				continue
			}

			result, err := m.checkVPNHealth()
			
			// Update VPN info in metrics if available
			if m.metrics != nil {
				rx, tx, _ := m.getTransferBytes()
				handshake, _ := m.getLastHandshakeTime()
				server := m.getCurrentServer()
				ip := m.getCurrentIP()
				m.metrics.UpdateVPNInfo(server, ip, rx, tx, handshake)
			}
			
			// Record metrics
			if m.metrics != nil {
				m.metrics.RecordCheck(err == nil, result.CheckDuration)
			}
			
			m.mu.Lock()
			if err == nil {
				if m.failureCount > 0 {
					m.showSuccess("VPN connection restored")
					m.failureCount = 0
					m.reconnectAttempts = 0
				}
				m.consecutiveSuccess++
			} else {
				m.failureCount++
				m.consecutiveSuccess = 0

				if m.failureCount < m.config.MaxFailures {
					m.showWarning(fmt.Sprintf("VPN health check failed (%d/%d)", 
						m.failureCount, m.config.MaxFailures))
					
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
					m.showError(fmt.Sprintf("VPN connection lost (%d/%d)", 
						m.failureCount, m.config.MaxFailures))
					m.mu.Unlock()
					m.triggerReconnect()
					m.mu.Lock()
					m.failureCount = 0
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

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	
	go func() {
		<-sigChan
		monitor.debugLog("Received shutdown signal")
		if config.MetricsEnabled {
			monitor.printMetrics()
		}
		cancel()
	}()

	// Ensure we can resolve DNS before starting
	_, err := net.LookupHost("google.com")
	if err != nil {
		log.Printf("Warning: DNS resolution may not be working: %v", err)
	}

	// Start metrics HTTP endpoint if enabled
	if config.MetricsEnabled {
		go startMetricsServer(monitor)
	}

	monitor.monitorLoop(ctx)
}

// HTTP endpoint for metrics (new capability!)
func startMetricsServer(m *Monitor) {
	// Prometheus-compatible metrics endpoint
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if m.metrics == nil {
			http.Error(w, "Metrics not enabled", http.StatusNotFound)
			return
		}
		
		stats := m.metrics.GetStats()
		
		// Check if Prometheus format is requested
		acceptHeader := r.Header.Get("Accept")
		if strings.Contains(acceptHeader, "text/plain") || r.URL.Query().Get("format") == "prometheus" {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			
			// Prometheus format
			fmt.Fprintf(w, "# HELP vpn_uptime_seconds Total uptime in seconds\n")
			fmt.Fprintf(w, "# TYPE vpn_uptime_seconds gauge\n")
			fmt.Fprintf(w, "vpn_uptime_seconds %d\n\n", stats["uptime_seconds"])
			
			fmt.Fprintf(w, "# HELP vpn_health_checks_total Total number of health checks performed\n")
			fmt.Fprintf(w, "# TYPE vpn_health_checks_total counter\n")
			fmt.Fprintf(w, "vpn_health_checks_total %d\n\n", stats["total_checks"])
			
			fmt.Fprintf(w, "# HELP vpn_health_checks_successful_total Total number of successful health checks\n")
			fmt.Fprintf(w, "# TYPE vpn_health_checks_successful_total counter\n")
			fmt.Fprintf(w, "vpn_health_checks_successful_total %d\n\n", stats["successful_checks"])
			
			fmt.Fprintf(w, "# HELP vpn_health_checks_failed_total Total number of failed health checks\n")
			fmt.Fprintf(w, "# TYPE vpn_health_checks_failed_total counter\n")
			fmt.Fprintf(w, "vpn_health_checks_failed_total %d\n\n", stats["failed_checks"])
			
			fmt.Fprintf(w, "# HELP vpn_success_rate Health check success rate (0-1)\n")
			fmt.Fprintf(w, "# TYPE vpn_success_rate gauge\n")
			fmt.Fprintf(w, "vpn_success_rate %.4f\n\n", stats["success_rate_decimal"])
			
			fmt.Fprintf(w, "# HELP vpn_reconnects_total Total number of reconnections\n")
			fmt.Fprintf(w, "# TYPE vpn_reconnects_total counter\n")
			fmt.Fprintf(w, "vpn_reconnects_total %d\n\n", stats["total_reconnects"])
			
			fmt.Fprintf(w, "# HELP vpn_check_duration_milliseconds Last health check duration in milliseconds\n")
			fmt.Fprintf(w, "# TYPE vpn_check_duration_milliseconds gauge\n")
			fmt.Fprintf(w, "vpn_check_duration_milliseconds %d\n\n", stats["last_check_duration_ms"])
			
			fmt.Fprintf(w, "# HELP vpn_check_duration_avg_milliseconds Average health check duration in milliseconds\n")
			fmt.Fprintf(w, "# TYPE vpn_check_duration_avg_milliseconds gauge\n")
			fmt.Fprintf(w, "vpn_check_duration_avg_milliseconds %d\n\n", stats["avg_check_duration_ms"])
			
			fmt.Fprintf(w, "# HELP vpn_bytes_received_total Total bytes received through VPN\n")
			fmt.Fprintf(w, "# TYPE vpn_bytes_received_total counter\n")
			fmt.Fprintf(w, "vpn_bytes_received_total %d\n\n", stats["bytes_received"])
			
			fmt.Fprintf(w, "# HELP vpn_bytes_transmitted_total Total bytes transmitted through VPN\n")
			fmt.Fprintf(w, "# TYPE vpn_bytes_transmitted_total counter\n")
			fmt.Fprintf(w, "vpn_bytes_transmitted_total %d\n\n", stats["bytes_transmitted"])
			
			fmt.Fprintf(w, "# HELP vpn_handshake_age_seconds Time since last WireGuard handshake\n")
			fmt.Fprintf(w, "# TYPE vpn_handshake_age_seconds gauge\n")
			fmt.Fprintf(w, "vpn_handshake_age_seconds %d\n\n", stats["handshake_age_seconds"])
			
			fmt.Fprintf(w, "# HELP vpn_server_latency_milliseconds Initial connection latency to VPN server\n")
			fmt.Fprintf(w, "# TYPE vpn_server_latency_milliseconds gauge\n")
			fmt.Fprintf(w, "vpn_server_latency_milliseconds %d\n\n", stats["server_latency_ms"])
			
			fmt.Fprintf(w, "# HELP vpn_server_uptime_seconds Time connected to current server\n")
			fmt.Fprintf(w, "# TYPE vpn_server_uptime_seconds gauge\n")
			fmt.Fprintf(w, "vpn_server_uptime_seconds %d\n\n", stats["server_uptime_seconds"])
			
			if server, ok := stats["current_server"].(string); ok && server != "" {
				fmt.Fprintf(w, "# HELP vpn_info VPN connection information\n")
				fmt.Fprintf(w, "# TYPE vpn_info gauge\n")
				fmt.Fprintf(w, "vpn_info{server=\"%s\",ip=\"%s\"} 1\n\n", 
					stats["current_server"], stats["current_ip"])
			}
		} else {
			// JSON format (default)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(stats)
		}
	})
	
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		result, err := m.checkVPNHealth()
		
		status := "healthy"
		statusCode := http.StatusOK
		if err != nil {
			status = "unhealthy"
			statusCode = http.StatusServiceUnavailable
		}
		
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":         status,
			"interface_up":   result.InterfaceUp,
			"connectivity":   result.Connectivity,
			"check_duration": result.CheckDuration.String(),
			"error":          fmt.Sprintf("%v", err),
		})
	})
	
	port := os.Getenv("METRICS_PORT")
	if port == "" {
		port = "9090"
	}
	
	// Silent startup - no log output (already shown in run.sh)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Printf("Metrics server error: %v", err)
	}
}

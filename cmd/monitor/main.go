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
	StartupGracePeriod time.Duration
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
	ServerLatency      int64
	ServerUptime       time.Duration
	
	// Performance metrics
	AvgCheckDuration   time.Duration
	MaxCheckDuration   time.Duration
	MinCheckDuration   time.Duration
	
	// WAN check metrics
	WANChecksTotal     int64
	WANChecksFailed    int64
	
	mu                 sync.Mutex
}

type HealthCheckResult struct {
	InterfaceUp    bool
	Connectivity   bool
	HandshakeOK    bool
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

	// Determine if handshake monitoring should be enabled (default: true)
	watchHandshake := true
	if val := os.Getenv("MONITOR_WATCH_HANDSHAKE"); val == "false" {
		watchHandshake = false
	}

	return Config{
		CheckInterval:      getEnvDuration("CHECK_INTERVAL", 15),
		MaxFailures:        getEnvInt("MAX_FAILURES", 3),
		ReconnectDelay:     getEnvDuration("RECONNECT_DELAY", 5),
		MaxReconnectDelay:  getEnvDuration("MAX_RECONNECT_DELAY", 300),
		RestartServices:    os.Getenv("RESTART_SERVICES"),
		DebugMode:          getEnvBool("MONITOR_DEBUG"),
		ParallelChecks:     getEnvBool("MONITOR_PARALLEL_CHECKS"),
		MetricsEnabled:     getEnvBool("METRICS"),
		FastFailMode:       getEnvBool("MONITOR_FAST_FAIL"),
		WatchHandshake:     watchHandshake,
		HandshakeTimeout:   getEnvDuration("HANDSHAKE_TIMEOUT", 360),
		StartupGracePeriod: getEnvDuration("MONITOR_STARTUP_GRACE", 30),
	}
}

func NewMetrics() *Metrics {
	return &Metrics{
		UptimeStart:      time.Now(),
		MinCheckDuration: time.Hour,
	}
}

func (m *Metrics) RecordCheck(success bool, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.TotalChecks++
	m.LastCheckTime = time.Now()
	m.LastCheckDuration = duration
	
	if duration > m.MaxCheckDuration {
		m.MaxCheckDuration = duration
	}
	if duration < m.MinCheckDuration {
		m.MinCheckDuration = duration
	}
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

func (m *Metrics) RecordWANCheck(success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.WANChecksTotal++
	if !success {
		m.WANChecksFailed++
	}
}

func (m *Metrics) UpdateVPNInfo(server, ip string, rx, tx int64, handshake time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
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
	
	wanSuccessRate := float64(100)
	if m.WANChecksTotal > 0 {
		wanSuccessRate = float64(m.WANChecksTotal-m.WANChecksFailed) / float64(m.WANChecksTotal) * 100
	}
	
	return map[string]interface{}{
		"total_checks":           m.TotalChecks,
		"successful_checks":      m.SuccessfulChecks,
		"failed_checks":          m.FailedChecks,
		"success_rate":           fmt.Sprintf("%.2f%%", successRate),
		"success_rate_decimal":   successRate / 100,
		"total_reconnects":       m.TotalReconnects,
		"uptime_seconds":         int(uptime.Seconds()),
		"uptime_formatted":       formatDuration(uptime),
		"last_check":             m.LastCheckTime.Format("2006-01-02 15:04:05"),
		"last_check_duration_ms": m.LastCheckDuration.Milliseconds(),
		"avg_check_duration_ms":  m.AvgCheckDuration.Milliseconds(),
		"max_check_duration_ms":  m.MaxCheckDuration.Milliseconds(),
		"min_check_duration_ms":  m.MinCheckDuration.Milliseconds(),
		"current_server":         m.CurrentServer,
		"current_ip":             m.CurrentIP,
		"bytes_received":         m.BytesReceived,
		"bytes_transmitted":      m.BytesTransmitted,
		"total_bytes":            m.BytesReceived + m.BytesTransmitted,
		"last_handshake":         m.LastHandshakeTime.Format("2006-01-02 15:04:05"),
		"handshake_age_seconds":  int(timeSinceHandshake.Seconds()),
		"server_latency_ms":      m.ServerLatency,
		"server_uptime_seconds":  int(m.ServerUptime.Seconds()),
		"server_uptime_formatted": formatDuration(m.ServerUptime),
		"wan_checks_total":       m.WANChecksTotal,
		"wan_checks_failed":      m.WANChecksFailed,
		"wan_success_rate":       fmt.Sprintf("%.2f%%", wanSuccessRate),
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
	
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "inet ") {
			fields := strings.Fields(line)
			for i, field := range fields {
				if field == "inet" && i+1 < len(fields) {
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
	
	cmd := exec.Command("ip", "link", "show", "pia")
	if err := cmd.Run(); err != nil {
		m.debugLog("Interface not found")
		return false
	}

	cmd = exec.Command("ip", "addr", "show", "pia")
	output, err := cmd.Output()
	if err == nil && strings.Contains(string(output), "inet ") {
		m.debugLog("Interface has IP address")
		return true
	}

	cmd = exec.Command("wg", "show", "pia", "peers")
	output, err = cmd.Output()
	if err == nil && len(strings.TrimSpace(string(output))) > 0 {
		m.debugLog("Interface has WireGuard peers")
		return true
	}

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
	cmd := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "5", host)
	return cmd.Run() == nil
}

func (m *Monitor) checkConnectivityHTTP(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	
	client := &http.Client{
		Timeout: 8 * time.Second,
	}
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		return true
	}
	return false
}

func (m *Monitor) checkExternalConnectivityParallel() bool {
	m.debugLog("Checking external connectivity (parallel mode)")
	
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	
	type checkResult struct {
		name    string
		success bool
	}
	
	results := make(chan checkResult, 3)
	
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
	
	cmd := exec.Command("ping", "-c", "1", "-W", "5", "1.1.1.1")
	if err := cmd.Run(); err == nil {
		m.debugLog("Ping to 1.1.1.1 successful")
		return true
	}

	cmd = exec.Command("ping", "-c", "1", "-W", "5", "8.8.8.8")
	if err := cmd.Run(); err == nil {
		m.debugLog("Ping to 8.8.8.8 successful")
		return true
	}

	client := &http.Client{
		Timeout: 8 * time.Second,
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

// NEW: Temporarily allow WAN connectivity test by bypassing VPN routing
func (m *Monitor) allowWANTest() error {
	m.debugLog("Adding temporary killswitch exception for WAN test")
	
	// Check if using nftables or iptables
	cmd := exec.Command("nft", "list", "table", "inet", "vpn_filter")
	if err := cmd.Run(); err == nil {
		// Using nftables - add temporary rule at high priority
		m.debugLog("Using nftables for WAN exception")
		
		// Add rule to allow traffic to 1.1.1.1:80 (before DROP rules)
		cmd = exec.Command("nft", "insert", "rule", "inet", "vpn_filter", "output", 
			"ip", "daddr", "1.1.1.1", "tcp", "dport", "80", "accept")
		if err := cmd.Run(); err != nil {
			m.debugLog("Failed to add nftables WAN rule: %v", err)
			return err
		}
		
		// Also add 8.8.8.8 as backup
		cmd = exec.Command("nft", "insert", "rule", "inet", "vpn_filter", "output",
			"ip", "daddr", "8.8.8.8", "tcp", "dport", "80", "accept")
		cmd.Run()
		
	} else {
		// Using iptables - add temporary rule at top of chain
		m.debugLog("Using iptables for WAN exception")
		
		cmd = exec.Command("iptables", "-I", "VPN_OUT", "1",
			"-d", "1.1.1.1", "-p", "tcp", "--dport", "80", "-j", "ACCEPT")
		if err := cmd.Run(); err != nil {
			m.debugLog("Failed to add iptables WAN rule: %v", err)
			return err
		}
		
		// Also add 8.8.8.8 as backup
		cmd = exec.Command("iptables", "-I", "VPN_OUT", "1",
			"-d", "8.8.8.8", "-p", "tcp", "--dport", "80", "-j", "ACCEPT")
		cmd.Run()
	}
	
	// CRITICAL: Add routing rules to bypass VPN table for our test IPs
	// This ensures packets go to main routing table instead of broken VPN
	m.debugLog("Adding bypass routing rules for WAN test")
	
	// Route 1.1.1.1 via main table (priority 50, before VPN rules at 200)
	cmd = exec.Command("ip", "rule", "add", "to", "1.1.1.1", "table", "main", "priority", "50")
	if err := cmd.Run(); err != nil {
		m.debugLog("Failed to add routing rule for 1.1.1.1: %v", err)
	}
	
	// Route 8.8.8.8 via main table
	cmd = exec.Command("ip", "rule", "add", "to", "8.8.8.8", "table", "main", "priority", "50")
	if err := cmd.Run(); err != nil {
		m.debugLog("Failed to add routing rule for 8.8.8.8: %v", err)
	}
	
	return nil
}

// NEW: Remove temporary WAN test exception
func (m *Monitor) removeWANTest() {
	m.debugLog("Removing temporary killswitch exception for WAN test")
	
	// Remove routing rules first
	m.debugLog("Removing bypass routing rules")
	
	cmd := exec.Command("ip", "rule", "del", "to", "1.1.1.1", "table", "main", "priority", "50")
	cmd.Run()
	
	cmd = exec.Command("ip", "rule", "del", "to", "8.8.8.8", "table", "main", "priority", "50")
	cmd.Run()
	
	// Check if using nftables
	cmd = exec.Command("nft", "list", "table", "inet", "vpn_filter")
	if err := cmd.Run(); err == nil {
		// Using nftables - remove the rules we added
		m.debugLog("Removing nftables WAN exception")
		
		// Delete 1.1.1.1 rule
		cmd = exec.Command("nft", "delete", "rule", "inet", "vpn_filter", "output",
			"ip", "daddr", "1.1.1.1", "tcp", "dport", "80", "accept")
		cmd.Run()
		
		// Delete 8.8.8.8 rule
		cmd = exec.Command("nft", "delete", "rule", "inet", "vpn_filter", "output",
			"ip", "daddr", "8.8.8.8", "tcp", "dport", "80", "accept")
		cmd.Run()
		
		return
	}
	
	// Using iptables
	m.debugLog("Removing iptables WAN exception")
	
	// Delete 1.1.1.1 rule
	cmd = exec.Command("iptables", "-D", "VPN_OUT",
		"-d", "1.1.1.1", "-p", "tcp", "--dport", "80", "-j", "ACCEPT")
	cmd.Run()
	
	// Delete 8.8.8.8 rule
	cmd = exec.Command("iptables", "-D", "VPN_OUT",
		"-d", "8.8.8.8", "-p", "tcp", "--dport", "80", "-j", "ACCEPT")
	cmd.Run()
}

// NEW: Check WAN connectivity using direct connection (with killswitch exception)
func (m *Monitor) checkWANConnectivity(timeout time.Duration) bool {
	m.debugLog("Checking WAN connectivity (bypass VPN)")
	
	// Try direct connection to Cloudflare
	dialer := &net.Dialer{
		Timeout: timeout,
	}
	
	// Try 1.1.1.1 first
	conn, err := dialer.Dial("tcp", "1.1.1.1:80")
	if err == nil {
		conn.Close()
		m.debugLog("WAN check successful (1.1.1.1)")
		return true
	}
	m.debugLog("1.1.1.1 failed: %v", err)
	
	// Try 8.8.8.8 as backup
	conn, err = dialer.Dial("tcp", "8.8.8.8:80")
	if err == nil {
		conn.Close()
		m.debugLog("WAN check successful (8.8.8.8)")
		return true
	}
	m.debugLog("8.8.8.8 failed: %v", err)
	
	m.debugLog("All WAN checks failed")
	return false
}

// NEW: Wait for WAN with exponential backoff (infinite loop until success)
func (m *Monitor) waitForWAN() bool {
	fmt.Printf("\n%s▶%s Testing WAN before reconnect...\n", colorBlue, colorReset)
	
	// Add temporary killswitch exception for testing
	if err := m.allowWANTest(); err != nil {
		m.debugLog("Could not add WAN test exception: %v", err)
	}
	defer m.removeWANTest()
	
	// Give the firewall rules a moment to apply
	time.Sleep(500 * time.Millisecond)
	
	// Record start time for downtime calculation
	downSince := time.Now()
	
	// Initial quick check
	if m.checkWANConnectivity(5 * time.Second) {
		m.showSuccess("Internet up")
		if m.metrics != nil {
			m.metrics.RecordWANCheck(true)
		}
		return true
	}
	
	// WAN is down, wait with exponential backoff
	m.showError("Internet down, waiting...")
	if m.metrics != nil {
		m.metrics.RecordWANCheck(false)
	}
	
	// Exponential backoff: 5s, 10s, 20s, 40s, 80s, then 160s forever
	delays := []int{5, 10, 20, 40, 80}
	maxDelay := 160
	attempt := 1
	
	// First 5 attempts with exponential backoff
	for _, delay := range delays {
		// Use \r to overwrite the previous line
		fmt.Printf("\r  %s⏳%s Checking again in %ds (attempt %d)...%s", 
			colorYellow, colorReset, delay, attempt, strings.Repeat(" ", 20))
		time.Sleep(time.Duration(delay) * time.Second)
		
		if m.checkWANConnectivity(5 * time.Second) {
			downtime := time.Since(downSince)
			fmt.Printf("\r%s", strings.Repeat(" ", 80)) // Clear the line
			fmt.Printf("\r")
			m.showSuccess(fmt.Sprintf("Internet restored (down for %s)", formatDuration(downtime)))
			if m.metrics != nil {
				m.metrics.RecordWANCheck(true)
			}
			return true
		}
		
		if m.metrics != nil {
			m.metrics.RecordWANCheck(false)
		}
		attempt++
	}
	
	// Continue checking every 160s indefinitely
	for {
		fmt.Printf("\r  %s⏳%s Checking again in %ds (attempt %d)...%s", 
			colorYellow, colorReset, maxDelay, attempt, strings.Repeat(" ", 20))
		time.Sleep(time.Duration(maxDelay) * time.Second)
		
		if m.checkWANConnectivity(5 * time.Second) {
			downtime := time.Since(downSince)
			fmt.Printf("\r%s", strings.Repeat(" ", 80)) // Clear the line
			fmt.Printf("\r")
			m.showSuccess(fmt.Sprintf("Internet restored (down for %s)", formatDuration(downtime)))
			if m.metrics != nil {
				m.metrics.RecordWANCheck(true)
			}
			return true
		}
		
		if m.metrics != nil {
			m.metrics.RecordWANCheck(false)
		}
		attempt++
	}
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
	
	parts := strings.Fields(lines[0])
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("invalid handshake format")
	}
	
	timestamp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	
	now := time.Now().Unix()
	if timestamp == 0 {
		return time.Time{}, fmt.Errorf("no handshake yet (timestamp is 0)")
	}
	if timestamp > now {
		return time.Time{}, fmt.Errorf("invalid timestamp: %d (in future)", timestamp)
	}
	
	return time.Unix(timestamp, 0), nil
}

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
	
	parts := strings.Fields(lines[0])
	if len(parts) < 3 {
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

	if m.config.WatchHandshake {
		lastHandshake, err := m.getLastHandshakeTime()
		if err == nil {
			timeSince := time.Since(lastHandshake)
			m.debugLog("Last handshake was %v ago", timeSince)
			
			if timeSince < m.config.HandshakeTimeout {
				result.HandshakeOK = true
				m.debugLog("Handshake is fresh (within %v threshold)", m.config.HandshakeTimeout)
			} else {
				m.debugLog("Handshake is stale (%v old, threshold: %v)", timeSince, m.config.HandshakeTimeout)
				result.Error = fmt.Errorf("handshake stale (%v old, threshold: %v)", timeSince, m.config.HandshakeTimeout)
				result.CheckDuration = time.Since(start)
				return result, result.Error
			}
		} else {
			m.debugLog("Could not check handshake: %v (ignoring for now)", err)
		}
	}

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

	if m.checkExternalConnectivity() {
		m.debugLog("Connectivity passed")
		result.Connectivity = true
		result.CheckDuration = time.Since(start)
		return result, nil
	}

	m.debugLog("First check failed, retrying...")
	time.Sleep(3 * time.Second)
	
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
	m.mu.Unlock()
	
	// NEW: Check WAN connectivity before reconnecting
	m.waitForWAN()
	
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

func (m *Monitor) watchHandshakes(ctx context.Context, failChan chan<- struct{}) {
	if !m.config.WatchHandshake {
		return
	}
	
	m.debugLog("Starting handshake watcher (timeout: %v)", m.config.HandshakeTimeout)
	
	time.Sleep(15 * time.Second)
	
	var lastRx, lastTx int64
	var staleCount int
	const maxStaleChecks = 3
	
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rx, tx, err := m.getTransferBytes()
			if err != nil {
				if !strings.Contains(err.Error(), "transitioning") {
					m.debugLog("Could not get transfer bytes: %v", err)
				}
				continue
			}
			
			bytesChanged := (rx != lastRx) || (tx != lastTx)
			
			if bytesChanged {
				m.debugLog("Transfer active: rx=%d (+%d), tx=%d (+%d)", 
					rx, rx-lastRx, tx, tx-lastTx)
				lastRx = rx
				lastTx = tx
				staleCount = 0
				continue
			}
			
			lastHandshake, err := m.getLastHandshakeTime()
			if err != nil {
				m.debugLog("Could not get handshake time: %v", err)
				staleCount++
			} else {
				timeSince := time.Since(lastHandshake)
				m.debugLog("No transfer in 30s, handshake was %v ago", timeSince)
				
				if timeSince > m.config.HandshakeTimeout {
					staleCount++
					m.debugLog("Stale check %d/%d", staleCount, maxStaleChecks)
				} else {
					staleCount = 0
				}
			}
			
			if staleCount >= maxStaleChecks {
				m.showWarning(fmt.Sprintf("Connection stale (%d checks) - triggering reconnect", staleCount))
				select {
				case failChan <- struct{}{}:
				default:
				}
				staleCount = 0
			}
			
			lastRx = rx
			lastTx = tx
		}
	}
}

func (m *Monitor) monitorLoop(ctx context.Context) {
	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()
	
	failChan := make(chan struct{}, 1)
	
	if m.config.WatchHandshake {
		go m.watchHandshakes(ctx, failChan)
	}
	
	if m.metrics != nil {
		latency := m.getServerLatency()
		if latency > 0 {
			m.metrics.SetServerLatency(latency)
		}
	}

	graceEnd := time.Now().Add(m.config.StartupGracePeriod)
	if m.config.StartupGracePeriod > 0 {
		m.debugLog("Starting with %v grace period until %v", 
			m.config.StartupGracePeriod, graceEnd.Format("15:04:05"))
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
			m.showError("Handshake timeout detected - immediate reconnect")
			m.mu.Unlock()
			m.triggerReconnect()
			m.mu.Lock()
			m.failureCount = 0
			m.mu.Unlock()
			graceEnd = time.Now().Add(m.config.StartupGracePeriod)
			
		case <-ticker.C:
			if time.Now().Before(graceEnd) {
				m.debugLog("In grace period, skipping check")
				continue
			}

			result, err := m.checkVPNHealth()
			
			if m.metrics != nil {
				rx, tx, _ := m.getTransferBytes()
				handshake, _ := m.getLastHandshakeTime()
				server := m.getCurrentServer()
				ip := m.getCurrentIP()
				m.metrics.UpdateVPNInfo(server, ip, rx, tx, handshake)
			}
			
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
					// Overwrite previous line to show updated count
					fmt.Printf("\r  %s⚠%s VPN health check failed (%d/%d)%s\n", 
						colorYellow, colorReset, m.failureCount, m.config.MaxFailures, strings.Repeat(" ", 20))
					
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
					// Overwrite the last warning with the final error
					fmt.Printf("\r  %s✗%s VPN connection lost (%d/%d)%s\n", 
						colorRed, colorReset, m.failureCount, m.config.MaxFailures, strings.Repeat(" ", 20))
					m.mu.Unlock()
					m.triggerReconnect()
					m.mu.Lock()
					m.failureCount = 0
					m.mu.Unlock()
					graceEnd = time.Now().Add(m.config.StartupGracePeriod)
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
		log.Printf("Warning: DNS resolution may not be working: %v", err)
	}

	if config.MetricsEnabled {
		go startMetricsServer(monitor)
	}

	monitor.monitorLoop(ctx)
}

func startMetricsServer(m *Monitor) {
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if m.metrics == nil {
			http.Error(w, "Metrics not enabled", http.StatusNotFound)
			return
		}
		
		stats := m.metrics.GetStats()
		
		acceptHeader := r.Header.Get("Accept")
		if strings.Contains(acceptHeader, "text/plain") || r.URL.Query().Get("format") == "prometheus" {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			
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
			
			fmt.Fprintf(w, "# HELP vpn_wan_checks_total Total number of WAN connectivity checks\n")
			fmt.Fprintf(w, "# TYPE vpn_wan_checks_total counter\n")
			fmt.Fprintf(w, "vpn_wan_checks_total %d\n\n", stats["wan_checks_total"])
			
			fmt.Fprintf(w, "# HELP vpn_wan_checks_failed_total Total number of failed WAN checks\n")
			fmt.Fprintf(w, "# TYPE vpn_wan_checks_failed_total counter\n")
			fmt.Fprintf(w, "vpn_wan_checks_failed_total %d\n\n", stats["wan_checks_failed"])
			
			if server, ok := stats["current_server"].(string); ok && server != "" {
				fmt.Fprintf(w, "# HELP vpn_info VPN connection information\n")
				fmt.Fprintf(w, "# TYPE vpn_info gauge\n")
				fmt.Fprintf(w, "vpn_info{server=\"%s\",ip=\"%s\"} 1\n\n", 
					stats["current_server"], stats["current_ip"])
			}
		} else {
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
			"handshake_ok":   result.HandshakeOK,
			"check_duration": result.CheckDuration.String(),
			"error":          fmt.Sprintf("%v", err),
		})
	})
	
	port := os.Getenv("METRICS_PORT")
	if port == "" {
		port = "9090"
	}
	
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Printf("Metrics server error: %v", err)
	}
}

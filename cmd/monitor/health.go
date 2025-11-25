package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type HealthCheckResult struct {
	InterfaceUp    bool
	Connectivity   bool
	CheckDuration  time.Duration
	Error          error
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

// Check WAN connectivity using bypass routes (no firewall manipulation needed!)
// These IPs (1.1.1.1, 8.8.8.8) are in routing table 100 and bypass the VPN
func (m *Monitor) checkWANConnectivity(timeout time.Duration) bool {
	m.debugLog("Checking WAN connectivity (bypass routes)")

	dialer := &net.Dialer{Timeout: timeout}

	// NIST time servers on TCP port 13 (DAYTIME)
	targets := []string{
		"129.6.15.28:13",    // time-a-g.nist.gov
		"129.6.15.29:13",    // time-b-g.nist.gov
		"132.163.96.1:13",   // time-a-b.nist.gov
		"132.163.97.1:13",   // time-a-wwv.nist.gov
		"128.138.140.44:13", // utcnist.colorado.edu
	}

	for _, target := range targets {
		conn, err := dialer.Dial("tcp", target)
		if err == nil {
			conn.Close()
			m.debugLog("WAN check successful (%s)", target)
			return true
		}
		m.debugLog("%s failed: %v", target, err)
	}

	m.debugLog("All WAN checks failed")
	return false
}

// Wait for WAN with exponential backoff (infinite loop until success)
func (m *Monitor) waitForWAN() bool {
	fmt.Printf("\n%s▶%s Testing WAN before reconnect...\n", colorBlue, colorReset)

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
		time.Sleep(time.Duration(delay) * time.Second)

		if m.checkWANConnectivity(5 * time.Second) {
			downtime := time.Since(downSince)
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
		time.Sleep(time.Duration(maxDelay) * time.Second)

		if m.checkWANConnectivity(5 * time.Second) {
			downtime := time.Since(downSince)
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

	// Check WAN connectivity before reconnecting
	m.waitForWAN()

	// Write to named pipe (non-blocking, opens and closes immediately)
	if err := os.WriteFile("/tmp/vpn_reconnect_pipe", []byte("health_check_failed\n"), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to signal reconnect via pipe: %v\n", err)
	}

	if m.metrics != nil {
		m.metrics.RecordReconnect()
	}
}

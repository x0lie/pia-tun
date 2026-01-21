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
	InterfaceUp   bool
	Connectivity  bool
	CheckDuration time.Duration
	Error         error
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
	// Try to get public IP from external service
	client := &http.Client{Timeout: 5 * time.Second}

	// Try multiple services in case one is down
	services := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}

	for _, service := range services {
		resp, err := client.Get(service)
		if err == nil {
			defer resp.Body.Close()
			body := make([]byte, 128)
			n, _ := resp.Body.Read(body)
			if n > 0 {
				ip := strings.TrimSpace(string(body[:n]))
				// Validate it looks like an IP
				if len(ip) > 6 && len(ip) < 40 {
					return ip
				}
			}
		}
	}

	// Fallback: return tunnel IP
	cmd := exec.Command("ip", "addr", "show", "pia0")
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

func (m *Monitor) checkConnectivityPing(ctx context.Context, host string) bool {
	cmd := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "2", host)
	return cmd.Run() == nil
}

func (m *Monitor) checkExternalConnectivity(timeout time.Duration) bool {
	m.debugLog("Checking external connectivity via ping")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	type checkResult struct {
		name    string
		success bool
	}

	results := make(chan checkResult, 3)

	// Parallel ping checks - first success wins
	go func() {
		success := m.checkConnectivityPing(ctx, "1.1.1.1")
		results <- checkResult{"1.1.1.1", success}
	}()

	go func() {
		success := m.checkConnectivityPing(ctx, "8.8.8.8")
		results <- checkResult{"8.8.8.8", success}
	}()

	go func() {
		success := m.checkConnectivityPing(ctx, "9.9.9.9")
		results <- checkResult{"9.9.9.9", success}
	}()

	for i := 0; i < 3; i++ {
		result := <-results
		if result.success {
			m.debugLog("Connectivity check passed: %s responded", result.name)
			return true
		}
	}

	m.debugLog("Ping check failed")
	return false
}

// Check WAN connectivity using bypass routes (no firewall manipulation needed!)
// These IPs (1.1.1.1, 8.8.8.8) are in routing table 100 and bypass the VPN
func (m *Monitor) checkWANConnectivity(timeout time.Duration) bool {
	m.debugLog("Checking WAN connectivity (bypass routes, parallel)")

	// NIST time servers on TCP port 13 (DAYTIME)
	targets := []string{
		"129.6.15.28:13",    // time-a-g.nist.gov
		"129.6.15.29:13",    // time-b-g.nist.gov
		"132.163.96.1:13",   // time-a-b.nist.gov
		"132.163.97.1:13",   // time-a-wwv.nist.gov
		"128.138.140.44:13", // utcnist.colorado.edu
	}

	type result struct {
		target  string
		success bool
	}

	results := make(chan result, len(targets))

	// Check all targets in parallel - first success wins
	for _, target := range targets {
		go func(t string) {
			dialer := &net.Dialer{Timeout: timeout}
			conn, err := dialer.Dial("tcp", t)
			if err == nil {
				conn.Close()
				results <- result{t, true}
			} else {
				results <- result{t, false}
			}
		}(target)
	}

	// Wait for all results, return true on first success
	for i := 0; i < len(targets); i++ {
		res := <-results
		if res.success {
			m.debugLog("WAN check successful (%s)", res.target)
			return true
		}
	}

	m.debugLog("All WAN checks failed")
	return false
}

// Wait for WAN with regular polling (infinite loop until success)
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

	// WAN is down, poll every 10s until it comes back
	m.showError("Internet down, waiting...")
	if m.metrics != nil {
		m.metrics.RecordWANCheck(false)
	}

	// Check every 10 seconds indefinitely
	// No aggressive backoff needed - we're disconnected so not stressing anything
	for {

		if m.checkWANConnectivity(10 * time.Second) {
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
	}
}

func (m *Monitor) getTransferBytes() (rx, tx int64, err error) {
	cmd := exec.Command("wg", "show", "pia0", "transfer")
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

func (m *Monitor) checkVPNHealth(timeout time.Duration) (*HealthCheckResult, error) {
	start := time.Now()

	result := &HealthCheckResult{
		InterfaceUp: true, // Assumed if connectivity passes
	}

	if m.checkExternalConnectivity(timeout) {
		result.Connectivity = true
		result.CheckDuration = time.Since(start)
		return result, nil
	}

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

func (m *Monitor) getLastHandshake() int64 {
	cmd := exec.Command("wg", "show", "pia0", "latest-handshakes")
	output, err := cmd.Output()
	if err != nil {
		return 0
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return 0
	}

	parts := strings.Fields(lines[0])
	if len(parts) < 2 {
		return 0
	}

	timestamp, _ := strconv.ParseInt(parts[1], 10, 64)
	return timestamp
}

func (m *Monitor) isKillswitchActive() bool {
	// Check if killswitch flag file exists
	// Created by killswitch.sh when active, removed on cleanup
	_, err := os.Stat("/tmp/killswitch_up")
	return err == nil
}

func (m *Monitor) getPortForwardingPort() int {
	portFile := os.Getenv("PORT_FILE")
	if portFile == "" {
		portFile = "/run/pia-tun/port"
	}

	data, err := os.ReadFile(portFile)
	if err != nil {
		return 0
	}

	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}

	return port
}

func (m *Monitor) isPortForwardingActive() bool {
	// Check if port file exists and has a valid port
	port := m.getPortForwardingPort()
	return port > 0
}

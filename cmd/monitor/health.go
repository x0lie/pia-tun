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
				// Validate it looks like an IP and is not private
				if len(ip) > 6 && len(ip) < 40 && !isPrivateIP(ip) {
					return ip
				}
			}
		}
	}

	// Return empty if we couldn't get a valid public IP
	// Next health check will retry
	return ""
}

// isPrivateIP checks if an IP address is in a private range
func isPrivateIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return parsed.IsPrivate() || parsed.IsLoopback() || parsed.IsLinkLocalUnicast()
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
		return true
	}

	// WAN is down, poll every 10s until it comes back
	m.showError("Internet down, waiting...")

	// Check every 10 seconds indefinitely
	// No aggressive backoff needed - we're disconnected so not stressing anything
	for {

		if m.checkWANConnectivity(10 * time.Second) {
			downtime := time.Since(downSince)
			fmt.Printf("\r")
			m.showSuccess(fmt.Sprintf("Internet restored (down for %s)", formatDuration(downtime)))
			return true
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

// getServerEndpoint returns the VPN server IP from wireguard endpoint
func (m *Monitor) getServerEndpoint() string {
	cmd := exec.Command("wg", "show", "pia0", "endpoints")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return ""
	}

	// Format: <public_key>\t<ip>:<port>
	parts := strings.Fields(lines[0])
	if len(parts) < 2 {
		return ""
	}

	// Extract IP from ip:port
	endpoint := parts[1]
	if idx := strings.LastIndex(endpoint, ":"); idx > 0 {
		return endpoint[:idx]
	}
	return endpoint
}

// pingServerLatency pings the VPN server and returns latency in seconds, or -1 on failure
func (m *Monitor) pingServerLatency(ctx context.Context, serverIP string) float64 {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "2", serverIP)
	if err := cmd.Run(); err != nil {
		return -1
	}
	return time.Since(start).Seconds()
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

// getKillswitchDropStats returns dropped packets and bytes from iptables DROP rules.
// Returns separate counts for inbound (VPN_IN) and outbound (VPN_OUT) chains.
// Includes both IPv4 (iptables) and IPv6 (ip6tables) drops.
func (m *Monitor) getKillswitchDropStats() (packetsIn, bytesIn, packetsOut, bytesOut int64) {
	// Get iptables binaries (supports both -nft and -legacy)
	iptables := "iptables"
	if path := os.Getenv("IPT_CMD"); path != "" {
		iptables = path
	}
	ip6tables := "ip6tables"
	if path := os.Getenv("IP6T_CMD"); path != "" {
		ip6tables = path
	}

	// Helper to parse DROP stats from a chain
	parseChain := func(iptCmd, chain string) (packets, bytes int64) {
		cmd := exec.Command(iptCmd, "-L", chain, "-v", "-n", "-x")
		output, err := cmd.Output()
		if err != nil {
			// Don't log for IPv6 - chain may not exist if IPv6 is disabled
			if iptCmd == iptables {
				m.debugLog("Failed to get iptables stats for %s: %v", chain, err)
			}
			return 0, 0
		}

		// Parse output looking for DROP rules
		// Format: "pkts bytes target prot opt in out source destination"
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[2] == "DROP" {
				if p, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
					packets += p
				}
				if b, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					bytes += b
				}
			}
		}
		return packets, bytes
	}

	// IPv4
	packetsIn, bytesIn = parseChain(iptables, "VPN_IN")
	packetsOut, bytesOut = parseChain(iptables, "VPN_OUT")

	// IPv6 - add to totals (chains are named VPN_IN6/VPN_OUT6)
	p, b := parseChain(ip6tables, "VPN_IN6")
	packetsIn += p
	bytesIn += b
	p, b = parseChain(ip6tables, "VPN_OUT6")
	packetsOut += p
	bytesOut += b

	return packetsIn, bytesIn, packetsOut, bytesOut
}

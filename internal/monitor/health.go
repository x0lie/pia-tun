package monitor

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

// HealthCheckResult holds the result of a VPN health check.
type HealthCheckResult struct {
	InterfaceUp   bool
	Connectivity  bool
	CheckDuration time.Duration
	Error         error
}

func (m *Monitor) getCurrentIP() string {
	client := &http.Client{Timeout: 5 * time.Second}

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
				if len(ip) > 6 && len(ip) < 40 && !isPrivateIP(ip) {
					return ip
				}
			}
		}
	}

	return ""
}

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
	m.log.Debug("Checking external connectivity via ping")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	type checkResult struct {
		name    string
		success bool
	}

	results := make(chan checkResult, 3)

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
			m.log.Debug("Connectivity check passed: %s responded", result.name)
			return true
		}
	}

	m.log.Debug("Ping check failed")
	return false
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
		InterfaceUp: true,
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

func (m *Monitor) triggerReconnect(ctx context.Context) {
	m.mu.Lock()
	m.reconnectAttempts++
	m.mu.Unlock()

	m.wan.WaitForUp(ctx)

	if m.metrics != nil {
		m.metrics.RecordReconnect()
	}

	if m.onReconnect != nil {
		m.log.Debug("Signaling orchestrator to reconnect")
		m.onReconnect()
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

	parts := strings.Fields(lines[0])
	if len(parts) < 2 {
		return ""
	}

	endpoint := parts[1]
	if idx := strings.LastIndex(endpoint, ":"); idx > 0 {
		return endpoint[:idx]
	}
	return endpoint
}

func (m *Monitor) pingServerLatency(ctx context.Context, serverIP string) float64 {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "2", serverIP)
	if err := cmd.Run(); err != nil {
		return -1
	}
	return time.Since(start).Seconds()
}

func (m *Monitor) isKillswitchActive() bool {
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
	port := m.getPortForwardingPort()
	return port > 0
}

func (m *Monitor) getKillswitchDropStats() (packetsIn, bytesIn, packetsOut, bytesOut int64) {
	iptables := "iptables"
	if path := os.Getenv("IPT_CMD"); path != "" {
		iptables = path
	}
	ip6tables := "ip6tables"
	if path := os.Getenv("IP6T_CMD"); path != "" {
		ip6tables = path
	}

	parseChain := func(iptCmd, chain string) (packets, bytes int64) {
		cmd := exec.Command(iptCmd, "-L", chain, "-v", "-n", "-x")
		output, err := cmd.Output()
		if err != nil {
			if iptCmd == iptables {
				m.log.Debug("Failed to get iptables stats for %s: %v", chain, err)
			}
			return 0, 0
		}

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

	packetsIn, bytesIn = parseChain(iptables, "VPN_IN")
	packetsOut, bytesOut = parseChain(iptables, "VPN_OUT")

	p, b := parseChain(ip6tables, "VPN_IN6")
	packetsIn += p
	bytesIn += b
	p, b = parseChain(ip6tables, "VPN_OUT6")
	packetsOut += p
	bytesOut += b

	return packetsIn, bytesIn, packetsOut, bytesOut
}

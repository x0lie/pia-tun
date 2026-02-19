package monitor

import (
	"context"
	"fmt"
	"net"
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

func (m *Monitor) checkConnectivity(ctx context.Context, host string) bool {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (m *Monitor) checkExternalConnectivity(timeout time.Duration) bool {
	m.log.Trace("Checking external connectivity via tcp")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	type checkResult struct {
		name    string
		success bool
	}

	results := make(chan checkResult, 3)

	go func() {
		success := m.checkConnectivity(ctx, "1.1.1.1:443")
		results <- checkResult{"1.1.1.1:443", success}
	}()

	go func() {
		success := m.checkConnectivity(ctx, "8.8.8.8:443")
		results <- checkResult{"8.8.8.8:443", success}
	}()

	go func() {
		success := m.checkConnectivity(ctx, "9.9.9.9:443")
		results <- checkResult{"9.9.9.9:443", success}
	}()

	for i := 0; i < 3; i++ {
		result := <-results
		if result.success {
			m.log.Trace("Connectivity check passed: %s responded", result.name)
			return true
		}
	}

	m.log.Debug("Connectivity check failed")
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

func (m *Monitor) triggerReconnect() {
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

func (m *Monitor) getKillswitchDropStats() (packetsIn, bytesIn, packetsOut, bytesOut int64) {
	iptables := m.firewall.Ipt4Cmd
	ip6tables := m.firewall.Ipt6Cmd

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

package monitor

import (
	"context"
	"fmt"
	"net"
	"os/exec"
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
		if m.state != nil {
			m.state.Pause()
		}
		m.onReconnect()
	}
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

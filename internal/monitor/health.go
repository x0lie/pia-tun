package monitor

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"time"
)

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

func (m *Monitor) checkVPNHealth(timeout time.Duration) (time.Duration, error) {
	start := time.Now()

	if m.checkExternalConnectivity(timeout) {
		return time.Since(start), nil
	}
	return time.Since(start), fmt.Errorf("connectivity check failed")
}

func (m *Monitor) triggerReconnect() {
	if m.onReconnect != nil {
		m.log.Debug("Signaling orchestrator to reconnect")
		m.onReconnect()
	}
}

func (m *Monitor) pingServerLatency(ctx context.Context, serverIP string) float64 {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "2", serverIP)
	if err := cmd.Run(); err != nil {
		return -1
	}
	return time.Since(start).Seconds()
}

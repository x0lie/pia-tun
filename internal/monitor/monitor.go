package monitor

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/x0lie/pia-tun/internal/apperrors"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/metrics"
)

// Config holds monitor configuration.
type Config struct {
	Interval      time.Duration
	FailureWindow time.Duration
}

// Monitor manages VPN health monitoring.
type Monitor struct {
	config   *Config
	log      *log.Logger
	metrics  *metrics.Metrics
	serverIP string
}

func Run(ctx context.Context, cfg *Config, metrics *metrics.Metrics, serverIP string) error {
	m := &Monitor{
		config:   cfg,
		log:      log.New("monitor"),
		metrics:  metrics,
		serverIP: serverIP,
	}

	m.log.Debug("Loop starting with %s interval and %s failure tolerance", m.config.Interval, m.config.FailureWindow)

	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()

	if err := m.performCheck(ctx); err != nil {
		return fmt.Errorf("%w: %w", err, apperrors.ErrReconnect)
	}

	for {
		select {
		case <-ctx.Done():
			m.log.Debug("Received shutdown signal")
			return nil

		case <-ticker.C:
			if err := m.performCheck(ctx); err != nil {
				return fmt.Errorf("%w: %w", err, apperrors.ErrReconnect)
			}
		}
	}
}

const timeout = 3 * time.Second

func (m *Monitor) performCheck(ctx context.Context) error {
	var serverLatencyChan chan float64

	// Server latency metric gathering
	if m.metrics.Enabled() {
		serverLatencyChan = make(chan float64, 1)
		go func() {
			if m.serverIP == "" {
				serverLatencyChan <- -1
				return
			}
			serverLatencyChan <- m.checkServerLatency(ctx, m.serverIP)
		}()
	}

	// Normal health check
	duration, err := m.checkConnectivity(ctx, timeout)

	if m.metrics.Enabled() {
		m.metrics.RecordCheck(err == nil, duration)

		if serverLatencyChan != nil {
			latency := <-serverLatencyChan
			if latency > 0 {
				m.metrics.ObserveServerLatency(latency)
			}
		}
	}

	if ctx.Err() != nil {
		return nil
	}

	// If check failed, enter rapid check mode
	if err != nil {
		if err = m.performRapidChecks(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (m *Monitor) performRapidChecks(ctx context.Context) error {
	m.log.Debug("Entering rapid check mode (failure window: %s)", m.config.FailureWindow)

	recovered := false
	failureStart := time.Now()
	t := timeout

	for {
		if ctx.Err() != nil {
			return nil
		}

		remaining := m.config.FailureWindow - time.Since(failureStart)
		if remaining <= 0 {
			break
		}

		if remaining < t {
			t = remaining
		}

		_, rapidErr := m.checkConnectivity(ctx, t)

		elapsed := time.Since(failureStart)
		if rapidErr == nil {
			m.log.Debug("Connectivity recovered after %s", elapsed.Round(time.Second))
			recovered = true
			break
		}

		if elapsed >= m.config.FailureWindow {
			m.log.Debug("Rapid checks failed after %s", elapsed.Round(time.Second))
			break
		}

		m.log.Trace("Rapid check failed (elapsed: %s/%s)",
			elapsed.Round(time.Second),
			m.config.FailureWindow)
	}

	if !recovered {
		return fmt.Errorf("connection down for more than %s", m.config.FailureWindow)
	}
	return nil
}

func (m *Monitor) checkConnectivity(ctx context.Context, timeout time.Duration) (time.Duration, error) {
	m.log.Trace("Checking external connectivity via tcp")

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type checkResult struct {
		name    string
		success bool
	}

	results := make(chan checkResult, 3)
	start := time.Now()

	go func() {
		success := m.dialHost(ctx, "1.1.1.1:443")
		results <- checkResult{"1.1.1.1:443", success}
	}()

	go func() {
		success := m.dialHost(ctx, "8.8.8.8:443")
		results <- checkResult{"8.8.8.8:443", success}
	}()

	go func() {
		success := m.dialHost(ctx, "9.9.9.9:443")
		results <- checkResult{"9.9.9.9:443", success}
	}()

	for i := 0; i < 3; i++ {
		result := <-results
		if result.success {
			m.log.Trace("Connectivity check passed: %s responded", result.name)
			return time.Since(start), nil
		}
	}

	return time.Since(start), fmt.Errorf("connectivity check failed")
}

func (m *Monitor) checkServerLatency(ctx context.Context, serverIP string) float64 {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	if m.dialHost(ctx, serverIP+":443") {
		return time.Since(start).Seconds()
	}
	return -1
}

func (m *Monitor) dialHost(ctx context.Context, host string) bool {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", host)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

package monitor

import (
	"context"
	"fmt"
	"time"

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
	config      *Config
	log         *log.Logger
	metrics     *metrics.Metrics
	onReconnect func()
	serverIP    string
}

func (m *Monitor) monitorLoop(ctx context.Context) {
	m.log.Debug("Loop starting with %s interval and %s failure tolerance", m.config.Interval, m.config.FailureWindow)
	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()

	m.performCheck()

	for {
		select {
		case <-ctx.Done():
			m.log.Debug("Loop received shutdown signal")
			return

		case <-ticker.C:
			m.performCheck()
		}
	}
}

func (m *Monitor) performCheck() {
	const normalTimeout = 5 * time.Second
	const rapidTimeout = 2 * time.Second

	// Start server latency ping in parallel
	var serverLatencyChan chan float64
	if m.metrics.Enabled() {
		serverLatencyChan = make(chan float64, 1)
		go func() {
			if m.serverIP == "" {
				serverLatencyChan <- -1
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			serverLatencyChan <- m.pingServerLatency(ctx, m.serverIP)
		}()
	}

	// Normal health check
	duration, err := m.checkVPNHealth(normalTimeout)

	if m.metrics.Enabled() {
		m.metrics.RecordCheck(err == nil, duration)

		if serverLatencyChan != nil {
			latency := <-serverLatencyChan
			if latency > 0 {
				m.metrics.ObserveServerLatency(latency)
			}
		}
	}

	// If check failed, enter rapid check mode
	if err != nil {
		m.log.Debug("Entering rapid check mode (failure window: %s)", m.config.FailureWindow)

		failureStart := time.Now()
		recovered := false

		for {
			_, rapidErr := m.checkVPNHealth(rapidTimeout)

			if rapidErr == nil {
				m.log.Debug("Connectivity recovered during rapid checks")
				recovered = true
				break
			}

			elapsed := time.Since(failureStart)
			if elapsed >= m.config.FailureWindow {
				m.log.Debug("Rapid check failed (elapsed: %s/%s)",
					elapsed.Round(time.Second),
					m.config.FailureWindow)
				break
			}

			m.log.Debug("Rapid check failed (elapsed: %s/%s)",
				elapsed.Round(time.Second),
				m.config.FailureWindow)
		}

		if !recovered {
			log.Info("")
			log.Error(fmt.Sprintf("VPN connection lost (down for more than %s)", m.config.FailureWindow))
			m.triggerReconnect()
		}
	}
}

func Run(ctx context.Context, cfg *Config, onReconnect func(), m *metrics.Metrics, serverIP string) error {

	monitor := &Monitor{
		config:      cfg,
		log:         log.New("monitor"),
		metrics:     m,
		onReconnect: onReconnect,
		serverIP:    serverIP,
	}

	monitor.monitorLoop(ctx)
	return nil
}

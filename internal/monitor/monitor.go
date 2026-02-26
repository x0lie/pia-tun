package monitor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/metrics"
)

// Config holds monitor configuration.
type Config struct {
	Interval      time.Duration
	FailureWindow time.Duration
}

// State allows the orchestrator to communicate with the monitor
// without filesystem flags or named pipes. Nil in standalone mode.
type State struct {
	paused  atomic.Bool
	resumed chan struct{}
}

func NewState() *State {
	s := &State{resumed: make(chan struct{}, 1)}
	s.paused.Store(true)
	return s
}

func (s *State) Pause() {
	s.paused.Store(true)
	// Drain any buffered resume signal so the monitor doesn't
	// immediately wake up from a stale signal.
	select {
	case <-s.resumed:
	default:
	}
}

func (s *State) Resume() {
	s.paused.Store(false)
	select {
	case s.resumed <- struct{}{}:
	default:
	}
}

// Monitor manages VPN health monitoring.
type Monitor struct {
	config   *Config
	log      *log.Logger
	metrics  *metrics.Metrics
	firewall *firewall.Firewall
	mu       sync.Mutex

	// Reconnect callback for orchestrated mode.
	// When set, triggerReconnect calls this instead of writing to a pipe file.
	onReconnect func()

	// Orchestrator state for pause/reconnect signaling. Nil in standalone mode.
	state *State
}

func (m *Monitor) monitorLoop(ctx context.Context) {
	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()

	m.performCheck()

	for {
		select {
		case <-ctx.Done():
			m.log.Debug("Monitor loop received shutdown signal")
			return

		case <-ticker.C:
			if m.state != nil && m.state.paused.Load() {
				m.log.Debug("Health checks paused")
				ticker.Stop()
				select {
				case <-ctx.Done():
					m.log.Debug("Monitor loop received shutdown signal")
					return
				case <-m.state.resumed:
				}
				m.log.Debug("Health checks resumed")
				ticker.Reset(m.config.Interval)
				m.performCheck()
				continue
			}
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
			serverIP := m.getServerEndpoint()
			if serverIP == "" {
				serverLatencyChan <- -1
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			serverLatencyChan <- m.pingServerLatency(ctx, serverIP)
		}()
	}

	// Normal health check
	result, err := m.checkVPNHealth(normalTimeout)

	m.updateMetrics()

	if m.metrics.Enabled() {
		m.metrics.RecordCheck(err == nil, result.CheckDuration)

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
			// Exit rapid checks if reconnect is already in progress
			if m.state != nil && m.state.paused.Load() {
				m.log.Debug("Reconnect in progress, exiting rapid checks")
				recovered = true
				break
			}

			_, rapidErr := m.checkVPNHealth(rapidTimeout)

			m.updateMetrics()

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

func (m *Monitor) updateMetrics() {
	if m.metrics.Enabled() {
		rx, tx, _ := m.getTransferBytes()

		m.metrics.UpdateTransferBytes(rx, tx)
		m.metrics.UpdateKillswitchStatus(m.isKillswitchActive())
		m.metrics.UpdateLastHandshake(m.getLastHandshake())

		pktsIn, bytesIn, pktsOut, bytesOut := m.getKillswitchDropStats()
		m.metrics.UpdateKillswitchDrops(pktsIn, bytesIn, pktsOut, bytesOut)
	}
}

// Run starts the monitor. This is the main entry point called by the dispatcher.
// onReconnect is an optional callback for orchestrated mode. When set, the monitor
// calls it instead of writing to a pipe file when a reconnect is needed.
// state provides orchestrator pause/reconnect signaling. Pass nil for both
// in standalone mode.
func Run(ctx context.Context, cfg *Config, onReconnect func(), state *State, m *metrics.Metrics, fw *firewall.Firewall) error {

	monitor := &Monitor{
		config:      cfg,
		log:         log.New("monitor"),
		metrics:     m,
		onReconnect: onReconnect,
		state:       state,
		firewall:    fw,
	}

	monitor.monitorLoop(ctx)
	return nil
}

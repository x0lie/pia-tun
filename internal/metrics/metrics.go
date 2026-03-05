package metrics

import (
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/x0lie/pia-tun/internal/log"
)

type Config struct {
	Enabled bool
	Port    int
	Name    string
}

// Metrics tracks VPN health metrics and exposes them via Prometheus.
type Metrics struct {
	Config *Config
	log    *log.Logger

	// Internal state tracking (for JSON endpoint)
	TotalChecks       int64
	FailedChecks      int64
	SuccessfulChecks  int64
	TotalReconnects   int64
	LastCheckTime     time.Time
	LastCheckDuration time.Duration
	UptimeStart       time.Time

	// VPN-specific state
	CurrentServer    string
	CurrentIP        string
	BytesReceived    int64
	BytesTransmitted int64
	ConnectedAt      time.Time

	// Status tracking (for JSON endpoint)
	ServerLatency        int64
	Version              string
	ConnectionUp         bool
	WANUp                bool
	KillswitchActive     bool
	PortForwardingActive bool
	PortForwardingPort   int

	mu sync.Mutex

	// Prometheus metrics
	healthChecksTotal      prometheus.Counter
	healthChecksSuccess    prometheus.Counter
	reconnectsTotal        prometheus.Counter
	checkDurationHistogram prometheus.Histogram
	bytesReceivedTotal     prometheus.Counter
	bytesTransmittedTotal  prometheus.Counter
	serverLatencyHistogram prometheus.Histogram
	sessionUptimeGauge     prometheus.Gauge
	vpnInfo                *prometheus.GaugeVec
	wanUp                  prometheus.Gauge

	// Info metrics
	buildInfo *prometheus.GaugeVec

	// New metrics
	connectionUp         prometheus.Gauge
	killswitchActive     prometheus.Gauge
	lastHandshake        prometheus.Gauge
	portForwardingActive prometheus.Gauge
	portForwardingPort   prometheus.Gauge

	// Killswitch drop counters
	killswitchPacketsDropped *prometheus.CounterVec
	killswitchBytesDropped   *prometheus.CounterVec

	// Internal tracking for drop stats
	lastDroppedPacketsIn  int64
	lastDroppedBytesIn    int64
	lastDroppedPacketsOut int64
	lastDroppedBytesOut   int64

	registry *prometheus.Registry
}

// NewMetrics creates and registers all Prometheus metrics.
func New(cfg Config, version string) *Metrics {
	m := &Metrics{
		Config:      &cfg,
		log:         log.New("Metrics"),
		Version:     version,
		WANUp:       true,
		UptimeStart: time.Now(),
	}

	m.healthChecksTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pia_tun_health_checks_total",
		Help: "Total number of health checks performed",
	})

	m.healthChecksSuccess = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pia_tun_health_checks_successful_total",
		Help: "Total number of successful health checks",
	})

	m.reconnectsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pia_tun_reconnects_total",
		Help: "Total number of VPN reconnections",
	})

	m.checkDurationHistogram = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "pia_tun_health_checks_duration_seconds",
		Help: "Histogram of health check durations in seconds",
		Buckets: []float64{
			0.010, 0.020, 0.030, 0.040, 0.050, 0.060, 0.070, 0.080,
			0.090, 0.100, 0.120, 0.150, 0.200, 0.300, 0.500, 1.0, 2.0, 5.0,
		},
	})

	m.bytesReceivedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pia_tun_bytes_received_total",
		Help: "Total bytes received through VPN",
	})

	m.bytesTransmittedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pia_tun_bytes_transmitted_total",
		Help: "Total bytes transmitted through VPN",
	})

	m.serverLatencyHistogram = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "pia_tun_server_latency_seconds",
		Help: "Histogram of VPN server ping latency in seconds",
		Buckets: []float64{
			0.005, 0.010, 0.025, 0.050, 0.075, 0.100, 0.150, 0.200, 0.300, 0.500, 1.0, 2.0,
		},
	})

	m.sessionUptimeGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pia_tun_session_start_timestamp_seconds",
		Help: "Unix timestamp when current VPN session started",
	})

	m.vpnInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pia_tun_info",
			Help: "VPN connection information (server and IP as labels)",
		},
		[]string{"server", "ip"},
	)

	m.wanUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pia_tun_wan_up",
		Help: "WAN connectivity status (1=up, 0=down)",
	})

	m.buildInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pia_tun_build_info",
			Help: "Build information with version as label",
		},
		[]string{"version"},
	)

	m.connectionUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pia_tun_connection_up",
		Help: "VPN connection status (1=up, 0=down)",
	})

	m.killswitchActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pia_tun_killswitch_active",
		Help: "Killswitch status (1=active, 0=inactive)",
	})

	m.lastHandshake = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pia_tun_last_handshake_timestamp_seconds",
		Help: "Unix timestamp of last WireGuard handshake",
	})

	m.portForwardingActive = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "pia_tun_port_forwarding_active",
			Help: "Port forwarding status (1=active, 0=inactive)",
		},
	)

	m.portForwardingPort = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "pia_tun_port_forwarding_port",
			Help: "Current forwarded port number (0 if inactive)",
		},
	)

	m.killswitchPacketsDropped = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pia_tun_killswitch_packets_dropped_total",
			Help: "Total packets dropped by killswitch firewall rules",
		},
		[]string{"direction"},
	)

	m.killswitchBytesDropped = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pia_tun_killswitch_bytes_dropped_total",
			Help: "Total bytes dropped by killswitch firewall rules",
		},
		[]string{"direction"},
	)

	m.registry = prometheus.NewRegistry()

	m.registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m.registry.MustRegister(collectors.NewGoCollector(
		collectors.WithGoCollectorMemStatsMetricsDisabled(),
		collectors.WithGoCollectorRuntimeMetrics(
			collectors.GoRuntimeMetricsRule{
				Matcher: regexp.MustCompile(`^/gc/`),
			},
			collectors.GoRuntimeMetricsRule{
				Matcher: regexp.MustCompile(`^/sched/`),
			},
		),
	))

	var registerer prometheus.Registerer
	if cfg.Name != "" {
		registerer = prometheus.WrapRegistererWith(
			prometheus.Labels{"name": cfg.Name},
			m.registry,
		)
	} else {
		registerer = m.registry
	}

	registerer.MustRegister(
		m.healthChecksTotal,
		m.healthChecksSuccess,
		m.reconnectsTotal,
		m.checkDurationHistogram,
		m.bytesReceivedTotal,
		m.bytesTransmittedTotal,
		m.serverLatencyHistogram,
		m.sessionUptimeGauge,
		m.vpnInfo,
		m.wanUp,
		m.buildInfo,
		m.connectionUp,
		m.killswitchActive,
		m.lastHandshake,
		m.portForwardingActive,
		m.portForwardingPort,
		m.killswitchPacketsDropped,
		m.killswitchBytesDropped,
	)

	m.killswitchPacketsDropped.WithLabelValues("in")
	m.killswitchPacketsDropped.WithLabelValues("out")
	m.killswitchBytesDropped.WithLabelValues("in")
	m.killswitchBytesDropped.WithLabelValues("out")

	m.buildInfo.WithLabelValues(version).Set(1)

	m.wanUp.Set(1)

	registerer.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "pia_tun_container_start_timestamp_seconds",
			Help: "Unix timestamp when container started",
		},
		func() float64 {
			return float64(m.UptimeStart.Unix())
		},
	))

	return m
}

func (m *Metrics) Enabled() bool { return m.Config.Enabled }

func (m *Metrics) RecordCheck(success bool, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TotalChecks++
	m.LastCheckTime = time.Now()
	m.LastCheckDuration = duration

	if success {
		m.SuccessfulChecks++
	} else {
		m.FailedChecks++
	}

	m.healthChecksTotal.Inc()
	if success {
		m.healthChecksSuccess.Inc()
	}
	m.checkDurationHistogram.Observe(duration.Seconds())
}

func (m *Metrics) UpdateTransferBytes(rx, tx int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if rx >= m.BytesReceived {
		delta := rx - m.BytesReceived
		if delta > 0 {
			m.bytesReceivedTotal.Add(float64(delta))
		}
	} else {
		m.bytesReceivedTotal.Add(float64(rx))
	}

	if tx >= m.BytesTransmitted {
		delta := tx - m.BytesTransmitted
		if delta > 0 {
			m.bytesTransmittedTotal.Add(float64(delta))
		}
	} else {
		m.bytesTransmittedTotal.Add(float64(tx))
	}

	m.BytesReceived = rx
	m.BytesTransmitted = tx
}

func (m *Metrics) ObserveServerLatency(latencySeconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ServerLatency = int64(latencySeconds * 1000)
	m.serverLatencyHistogram.Observe(latencySeconds)
}

func (m *Metrics) RecordReconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TotalReconnects++
	m.reconnectsTotal.Inc()
}

func (m *Metrics) RecordNewConnection(server, ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if server == "" || ip == "" {
		return
	}

	m.ConnectedAt = time.Now()
	m.CurrentServer = server
	m.CurrentIP = ip

	m.vpnInfo.Reset()
	m.vpnInfo.WithLabelValues(server, ip).Set(1)

	m.sessionUptimeGauge.Set(float64(m.ConnectedAt.Unix()))
}

func (m *Metrics) UpdateConnectionStatus(connected bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ConnectionUp = connected
	if connected {
		m.connectionUp.Set(1)
	} else {
		m.connectionUp.Set(0)
	}
}

func (m *Metrics) UpdateKillswitchStatus(active bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.KillswitchActive = active
	if active {
		m.killswitchActive.Set(1)
	} else {
		m.killswitchActive.Set(0)
	}
}

func (m *Metrics) UpdateWANStatus(up bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.WANUp = up
	if up {
		m.wanUp.Set(1)
	} else {
		m.wanUp.Set(0)
	}
}

func (m *Metrics) UpdateLastHandshake(timestamp int64) {
	m.lastHandshake.Set(float64(timestamp))
}

func (m *Metrics) UpdatePortForwarding(active bool, port int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.PortForwardingActive = active
	m.PortForwardingPort = port
	if active {
		m.portForwardingActive.Set(1)
	} else {
		m.portForwardingActive.Set(0)
	}
	m.portForwardingPort.Set(float64(port))
}

func (m *Metrics) UpdateKillswitchDrops(packetsIn, bytesIn, packetsOut, bytesOut int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if packetsIn >= m.lastDroppedPacketsIn {
		if delta := packetsIn - m.lastDroppedPacketsIn; delta > 0 {
			m.killswitchPacketsDropped.WithLabelValues("in").Add(float64(delta))
		}
	} else {
		m.killswitchPacketsDropped.WithLabelValues("in").Add(float64(packetsIn))
	}

	if bytesIn >= m.lastDroppedBytesIn {
		if delta := bytesIn - m.lastDroppedBytesIn; delta > 0 {
			m.killswitchBytesDropped.WithLabelValues("in").Add(float64(delta))
		}
	} else {
		m.killswitchBytesDropped.WithLabelValues("in").Add(float64(bytesIn))
	}

	if packetsOut >= m.lastDroppedPacketsOut {
		if delta := packetsOut - m.lastDroppedPacketsOut; delta > 0 {
			m.killswitchPacketsDropped.WithLabelValues("out").Add(float64(delta))
		}
	} else {
		m.killswitchPacketsDropped.WithLabelValues("out").Add(float64(packetsOut))
	}

	if bytesOut >= m.lastDroppedBytesOut {
		if delta := bytesOut - m.lastDroppedBytesOut; delta > 0 {
			m.killswitchBytesDropped.WithLabelValues("out").Add(float64(delta))
		}
	} else {
		m.killswitchBytesDropped.WithLabelValues("out").Add(float64(bytesOut))
	}

	m.lastDroppedPacketsIn = packetsIn
	m.lastDroppedBytesIn = bytesIn
	m.lastDroppedPacketsOut = packetsOut
	m.lastDroppedBytesOut = bytesOut
}

func (m *Metrics) GetStats() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	containerUptime := time.Since(m.UptimeStart)
	var sessionUptime time.Duration
	if !m.ConnectedAt.IsZero() {
		sessionUptime = time.Since(m.ConnectedAt)
	}
	var successRate string
	if m.TotalChecks > 0 {
		successRate = fmt.Sprintf("%.2f%%", float64(m.SuccessfulChecks)/float64(m.TotalChecks)*100)
	} else {
		successRate = "N/A"
	}

	return map[string]interface{}{
		"version":                    m.Version,
		"connection_up":              m.ConnectionUp,
		"wan_up":                     m.WANUp,
		"killswitch_active":          m.KillswitchActive,
		"current_server":             m.CurrentServer,
		"current_ip":                 m.CurrentIP,
		"session_uptime":             log.FormatDuration(sessionUptime),
		"container_uptime":           log.FormatDuration(containerUptime),
		"server_latency_ms":          m.ServerLatency,
		"bytes_received":             m.BytesReceived,
		"bytes_transmitted":          m.BytesTransmitted,
		"bytes_total":                m.BytesReceived + m.BytesTransmitted,
		"port_forwarding_active":     m.PortForwardingActive,
		"port_forwarding_port":       m.PortForwardingPort,
		"reconnects_total":           m.TotalReconnects,
		"health_checks_total":        m.TotalChecks,
		"health_checks_successful":   m.SuccessfulChecks,
		"health_checks_failed":       m.FailedChecks,
		"health_checks_success_rate": successRate,
		"health_checks_latency_ms":   m.LastCheckDuration.Milliseconds(),
	}
}

func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

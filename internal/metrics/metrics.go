package metrics

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/x0lie/pia-tun/internal/log"
)

func getInstanceName() string {
	if name := os.Getenv("INSTANCE_NAME"); name != "" {
		return name
	}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		return hostname
	}
	return "pia-tun"
}

// Metrics tracks VPN health metrics and exposes them via Prometheus.
type Metrics struct {
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

	// Server performance tracking
	ServerLatency int64
	ServerUptime  time.Duration

	mu sync.Mutex

	// Prometheus metrics
	healthChecksTotal      prometheus.Counter
	healthChecksSuccess    prometheus.Counter
	healthChecksFailed     prometheus.Counter
	reconnectsTotal        prometheus.Counter
	checkDurationHistogram prometheus.Histogram
	bytesReceivedTotal     *prometheus.CounterVec
	bytesTransmittedTotal  *prometheus.CounterVec
	serverLatencyHistogram prometheus.Histogram
	sessionUptimeGauge     *prometheus.GaugeVec
	vpnInfo                *prometheus.GaugeVec
	wanUp                  prometheus.Gauge

	// Info metrics
	buildInfo *prometheus.GaugeVec

	// New metrics
	connectionUp         *prometheus.GaugeVec
	killswitchActive     prometheus.Gauge
	lastHandshake        *prometheus.GaugeVec
	portForwardingStatus *prometheus.GaugeVec
	lastForwardedPort    int

	// Killswitch drop counters
	killswitchPacketsDropped *prometheus.CounterVec
	killswitchBytesDropped   *prometheus.CounterVec

	// Internal tracking for drop stats
	lastDroppedPacketsIn  int64
	lastDroppedBytesIn    int64
	lastDroppedPacketsOut int64
	lastDroppedBytesOut   int64

	vpnInfoNeedsUpdate bool

	registry *prometheus.Registry
}

// NewMetrics creates and registers all Prometheus metrics.
func New() *Metrics {
	m := &Metrics{
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

	m.healthChecksFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pia_tun_health_checks_failed_total",
		Help: "Total number of failed health checks",
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

	m.bytesReceivedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pia_tun_bytes_received_total",
			Help: "Total bytes received through VPN",
		},
		[]string{"interface"},
	)

	m.bytesTransmittedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pia_tun_bytes_transmitted_total",
			Help: "Total bytes transmitted through VPN",
		},
		[]string{"interface"},
	)

	m.serverLatencyHistogram = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "pia_tun_server_latency_seconds",
		Help: "Histogram of VPN server ping latency in seconds",
		Buckets: []float64{
			0.005, 0.010, 0.025, 0.050, 0.075, 0.100, 0.150, 0.200, 0.300, 0.500, 1.0, 2.0,
		},
	})

	m.sessionUptimeGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pia_tun_session_start_timestamp_seconds",
			Help: "Unix timestamp when current VPN session started",
		},
		[]string{"interface"},
	)

	m.vpnInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pia_tun_info",
			Help: "VPN connection information (server and IP as labels)",
		},
		[]string{"interface", "server", "ip"},
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

	m.connectionUp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pia_tun_connection_up",
			Help: "VPN connection status (1=up, 0=down)",
		},
		[]string{"interface"},
	)

	m.killswitchActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pia_tun_killswitch_active",
		Help: "Killswitch status (1=active, 0=inactive)",
	})

	m.lastHandshake = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pia_tun_last_handshake_timestamp_seconds",
			Help: "Unix timestamp of last WireGuard handshake",
		},
		[]string{"interface"},
	)

	m.portForwardingStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pia_tun_port_forwarding_status",
			Help: "Port forwarding status (1=active, 0=inactive) with port as label",
		},
		[]string{"port"},
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

	registerer := prometheus.WrapRegistererWith(
		prometheus.Labels{"name": getInstanceName()},
		m.registry,
	)

	registerer.MustRegister(
		m.healthChecksTotal,
		m.healthChecksSuccess,
		m.healthChecksFailed,
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
		m.portForwardingStatus,
		m.killswitchPacketsDropped,
		m.killswitchBytesDropped,
	)

	m.killswitchPacketsDropped.WithLabelValues("in")
	m.killswitchPacketsDropped.WithLabelValues("out")
	m.killswitchBytesDropped.WithLabelValues("in")
	m.killswitchBytesDropped.WithLabelValues("out")

	version := os.Getenv("VERSION")
	if version == "" {
		version = "local"
	}
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
	} else {
		m.healthChecksFailed.Inc()
	}
	m.checkDurationHistogram.Observe(duration.Seconds())
}

func (m *Metrics) UpdateVPNInfo(iface, ip string, rx, tx int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ipChanged := ip != "" && ip != m.CurrentIP

	if ipChanged {
		m.CurrentIP = ip
	}

	if ipChanged && m.CurrentServer != "" && m.CurrentIP != "" {
		m.vpnInfo.Reset()
		m.vpnInfo.WithLabelValues(iface, m.CurrentServer, m.CurrentIP).Set(1)
	}

	if m.CurrentServer != "" {
		m.ServerUptime = time.Since(m.ConnectedAt)
		m.sessionUptimeGauge.WithLabelValues(iface).Set(float64(m.ConnectedAt.Unix()))
	}

	if rx >= m.BytesReceived {
		delta := rx - m.BytesReceived
		if delta > 0 {
			m.bytesReceivedTotal.WithLabelValues(iface).Add(float64(delta))
		}
	} else {
		m.bytesReceivedTotal.WithLabelValues(iface).Add(float64(rx))
	}

	if tx >= m.BytesTransmitted {
		delta := tx - m.BytesTransmitted
		if delta > 0 {
			m.bytesTransmittedTotal.WithLabelValues(iface).Add(float64(delta))
		}
	} else {
		m.bytesTransmittedTotal.WithLabelValues(iface).Add(float64(tx))
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

func (m *Metrics) ResetSession() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ConnectedAt = time.Now()
	m.CurrentServer = ""
	m.CurrentIP = ""
}

func (m *Metrics) RecordNewConnection(iface, server, ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if server == "" || ip == "" {
		return
	}

	m.ConnectedAt = time.Now()
	m.CurrentServer = server
	m.CurrentIP = ip

	m.vpnInfo.Reset()
	m.vpnInfo.WithLabelValues(iface, server, ip).Set(1)

	m.ServerUptime = 0
	m.sessionUptimeGauge.WithLabelValues(iface).Set(float64(m.ConnectedAt.Unix()))
}

func (m *Metrics) UpdateConnectionStatus(iface string, connected bool) {
	if connected {
		m.connectionUp.WithLabelValues(iface).Set(1)
	} else {
		m.connectionUp.WithLabelValues(iface).Set(0)
	}
}

func (m *Metrics) UpdateKillswitchStatus(active bool) {
	if active {
		m.killswitchActive.Set(1)
	} else {
		m.killswitchActive.Set(0)
	}
}

func (m *Metrics) UpdateWANStatus(up bool) {
	if up {
		m.wanUp.Set(1)
	} else {
		m.wanUp.Set(0)
	}
}

func (m *Metrics) UpdateLastHandshake(iface string, timestamp int64) {
	m.lastHandshake.WithLabelValues(iface).Set(float64(timestamp))
}

func (m *Metrics) UpdatePortForwarding(active bool, port int) {
	portStr := strconv.Itoa(port)

	if m.lastForwardedPort != 0 && m.lastForwardedPort != port {
		m.portForwardingStatus.DeleteLabelValues(strconv.Itoa(m.lastForwardedPort))
	}

	if active && port > 0 {
		m.portForwardingStatus.WithLabelValues(portStr).Set(1)
		m.lastForwardedPort = port
	} else {
		m.portForwardingStatus.WithLabelValues("0").Set(0)
		m.lastForwardedPort = 0
	}
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

	uptime := time.Since(m.UptimeStart)
	successRate := float64(0)
	if m.TotalChecks > 0 {
		successRate = float64(m.SuccessfulChecks) / float64(m.TotalChecks) * 100
	}

	return map[string]interface{}{
		"total_checks":            m.TotalChecks,
		"successful_checks":       m.SuccessfulChecks,
		"failed_checks":           m.FailedChecks,
		"success_rate":            fmt.Sprintf("%.2f%%", successRate),
		"success_rate_decimal":    successRate / 100,
		"total_reconnects":        m.TotalReconnects,
		"uptime_seconds":          int(uptime.Seconds()),
		"uptime_formatted":        log.FormatDuration(uptime),
		"last_check":              m.LastCheckTime.Format("2006-01-02 15:04:05"),
		"last_check_duration_ms":  m.LastCheckDuration.Milliseconds(),
		"current_server":          m.CurrentServer,
		"current_ip":              m.CurrentIP,
		"bytes_received":          m.BytesReceived,
		"bytes_transmitted":       m.BytesTransmitted,
		"total_bytes":             m.BytesReceived + m.BytesTransmitted,
		"server_latency_ms":       m.ServerLatency,
		"server_uptime_seconds":   int(m.ServerUptime.Seconds()),
		"server_uptime_formatted": log.FormatDuration(m.ServerUptime),
	}
}

func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

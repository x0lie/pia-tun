package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func getInstanceName() string {
	// Explicit override takes priority
	if name := os.Getenv("INSTANCE_NAME"); name != "" {
		return name
	}
	// Fallback to hostname (usually unique in containerized environments)
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		return hostname
	}
	return "pia-tun"
}

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
	connectionUp     *prometheus.GaugeVec
	killswitchActive prometheus.Gauge
	lastHandshake        *prometheus.GaugeVec
	portForwardingStatus *prometheus.GaugeVec
	lastForwardedPort    int // Track last port to handle label changes

	// Killswitch drop counters (with direction label)
	killswitchPacketsDropped *prometheus.CounterVec
	killswitchBytesDropped   *prometheus.CounterVec

	// Internal tracking for drop stats (to handle counter resets)
	lastDroppedPacketsIn  int64
	lastDroppedBytesIn    int64
	lastDroppedPacketsOut int64
	lastDroppedBytesOut   int64

	// Flag to track pending vpnInfo update after reconnect
	vpnInfoNeedsUpdate bool

	// Custom registry (excludes go_memstats_* metrics)
	registry *prometheus.Registry
}

func NewMetrics() *Metrics {
	m := &Metrics{
		UptimeStart: time.Now(),
	}

	// Create Prometheus metrics
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

	// New metrics
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

	// Create a custom registry (excludes go_memstats_* metrics)
	m.registry = prometheus.NewRegistry()

	// Add process collector (keeps process_* metrics)
	m.registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Add Go collector with only gc/sched runtime metrics (excludes all go_memstats_*)
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

	// Create a registerer with the name label applied to all pia_tun_* metrics
	registerer := prometheus.WrapRegistererWith(
		prometheus.Labels{"name": getInstanceName()},
		m.registry,
	)

	// Register all metrics with name label
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

	// Initialize killswitch drop counters so both directions always appear in output
	m.killswitchPacketsDropped.WithLabelValues("in")
	m.killswitchPacketsDropped.WithLabelValues("out")
	m.killswitchBytesDropped.WithLabelValues("in")
	m.killswitchBytesDropped.WithLabelValues("out")

	// Set build info from VERSION environment variable (set by Dockerfile)
	version := os.Getenv("VERSION")
	if version == "" {
		version = "local"
	}
	m.buildInfo.WithLabelValues(version).Set(1)

	// Assume WAN is up at startup (container wouldn't have connected otherwise)
	m.wanUp.Set(1)

	// Add container start timestamp as a gauge function (also with name label)
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

	// Update internal state
	m.TotalChecks++
	m.LastCheckTime = time.Now()
	m.LastCheckDuration = duration

	if success {
		m.SuccessfulChecks++
	} else {
		m.FailedChecks++
	}

	// Update Prometheus metrics
	m.healthChecksTotal.Inc()
	if success {
		m.healthChecksSuccess.Inc()
	} else {
		m.healthChecksFailed.Inc()
	}
	m.checkDurationHistogram.Observe(duration.Seconds())
}

func (m *Metrics) UpdateVPNInfo(iface, server, ip string, rx, tx int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Track what changed
	serverChanged := server != "" && server != m.CurrentServer
	ipChanged := ip != "" && ip != m.CurrentIP

	// Update internal state
	if serverChanged {
		m.ConnectedAt = time.Now()
		m.CurrentServer = server
	}

	if ipChanged {
		m.CurrentIP = ip
	}

	// Update pia_tun_info when either server or IP changes (requires both to be valid)
	if (serverChanged || ipChanged) && m.CurrentServer != "" && m.CurrentIP != "" {
		m.vpnInfo.Reset()
		m.vpnInfo.WithLabelValues(iface, m.CurrentServer, m.CurrentIP).Set(1)
	}

	if m.CurrentServer != "" {
		m.ServerUptime = time.Since(m.ConnectedAt) // Keep for JSON endpoint
		m.sessionUptimeGauge.WithLabelValues(iface).Set(float64(m.ConnectedAt.Unix()))
	}

	// Update bytes counters (handle resets from container restarts)
	if rx >= m.BytesReceived {
		// Normal case: increment
		delta := rx - m.BytesReceived
		if delta > 0 {
			m.bytesReceivedTotal.WithLabelValues(iface).Add(float64(delta))
		}
	} else {
		// Counter reset (reconnection or restart)
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

	m.ServerLatency = int64(latencySeconds * 1000) // Keep internal state in ms for JSON endpoint
	m.serverLatencyHistogram.Observe(latencySeconds)
}

func (m *Metrics) RecordReconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TotalReconnects++
	m.reconnectsTotal.Inc()
}

// ResetSession resets session-related metrics after a reconnect.
// This ensures session_uptime restarts and vpnInfo updates even when
// reconnecting to the same server.
func (m *Metrics) ResetSession() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ConnectedAt = time.Now()
	m.CurrentServer = "" // Force server change detection on next UpdateVPNInfo call
	m.CurrentIP = ""     // Force IP update on next UpdateVPNInfo call (when valid IP is available)
}

// UpdateVPNInfoFromPipe updates the VPN info metric directly from pipe data.
// This bypasses the normal change detection and always updates the metric,
// ensuring reconnections are always reflected even if server/IP are the same.
func (m *Metrics) UpdateVPNInfoFromPipe(iface, server, ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if server == "" || ip == "" {
		return
	}

	// Always update on pipe signal - this confirms the connection is fresh
	m.ConnectedAt = time.Now()
	m.CurrentServer = server
	m.CurrentIP = ip

	// Reset and update the metric (ensures old labels are cleared)
	m.vpnInfo.Reset()
	m.vpnInfo.WithLabelValues(iface, server, ip).Set(1)

	// Set session start timestamp for the new connection
	m.ServerUptime = 0
	m.sessionUptimeGauge.WithLabelValues(iface).Set(float64(m.ConnectedAt.Unix()))
}

// StartConnectionPipeListener starts a goroutine that listens for connection
// events from the bash scripts via a named pipe. This provides reliable,
// event-driven updates to VPN info metrics without polling.
func (m *Metrics) StartConnectionPipeListener(debugLog func(string, ...interface{})) {
	go func() {
		pipePath := "/tmp/vpn_connection_pipe"

		for {
			// Open pipe - this blocks until a writer opens the other end
			file, err := os.Open(pipePath)
			if err != nil {
				// Pipe may not exist yet during startup
				if os.IsNotExist(err) {
					debugLog("Connection pipe not found, waiting...")
				} else {
					debugLog("Failed to open connection pipe: %v", err)
				}
				time.Sleep(5 * time.Second)
				continue
			}

			// Read lines from the pipe
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}

				// Parse: server|ip|timestamp
				parts := strings.Split(line, "|")
				if len(parts) >= 2 {
					server := strings.TrimSpace(parts[0])
					ip := strings.TrimSpace(parts[1])

					if server != "" && ip != "" {
						m.UpdateVPNInfoFromPipe("pia0", server, ip)
						debugLog("Connection pipe: updated VPN info - server=%s, ip=%s", server, ip)
					}
				} else {
					debugLog("Connection pipe: invalid data format: %s", line)
				}
			}

			if err := scanner.Err(); err != nil {
				debugLog("Connection pipe read error: %v", err)
			}

			file.Close()
			// Small delay before reopening to prevent tight loop on errors
			time.Sleep(100 * time.Millisecond)
		}
	}()
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

	// If port changed, reset the old metric to avoid stale labels
	if m.lastForwardedPort != 0 && m.lastForwardedPort != port {
		m.portForwardingStatus.DeleteLabelValues(strconv.Itoa(m.lastForwardedPort))
	}

	if active && port > 0 {
		m.portForwardingStatus.WithLabelValues(portStr).Set(1)
		m.lastForwardedPort = port
	} else {
		// When inactive, show port=0 with value 0
		m.portForwardingStatus.WithLabelValues("0").Set(0)
		m.lastForwardedPort = 0
	}
}

func (m *Metrics) UpdateKillswitchDrops(packetsIn, bytesIn, packetsOut, bytesOut int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Handle inbound counter increments
	if packetsIn >= m.lastDroppedPacketsIn {
		if delta := packetsIn - m.lastDroppedPacketsIn; delta > 0 {
			m.killswitchPacketsDropped.WithLabelValues("in").Add(float64(delta))
		}
	} else {
		// Counter reset (reconnection cleared iptables rules)
		m.killswitchPacketsDropped.WithLabelValues("in").Add(float64(packetsIn))
	}

	if bytesIn >= m.lastDroppedBytesIn {
		if delta := bytesIn - m.lastDroppedBytesIn; delta > 0 {
			m.killswitchBytesDropped.WithLabelValues("in").Add(float64(delta))
		}
	} else {
		m.killswitchBytesDropped.WithLabelValues("in").Add(float64(bytesIn))
	}

	// Handle outbound counter increments
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
		"uptime_formatted":        formatDuration(uptime),
		"last_check":              m.LastCheckTime.Format("2006-01-02 15:04:05"),
		"last_check_duration_ms":  m.LastCheckDuration.Milliseconds(),
		"current_server":          m.CurrentServer,
		"current_ip":              m.CurrentIP,
		"bytes_received":          m.BytesReceived,
		"bytes_transmitted":       m.BytesTransmitted,
		"total_bytes":             m.BytesReceived + m.BytesTransmitted,
		"server_latency_ms":       m.ServerLatency,
		"server_uptime_seconds":   int(m.ServerUptime.Seconds()),
		"server_uptime_formatted": formatDuration(m.ServerUptime),
	}
}

func startHTTPServer(m *Monitor) {
	mux := http.NewServeMux()

	// Health endpoint - always available for Docker HEALTHCHECK
	mux.Handle("/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.isHealthy() {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("healthy\n"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("unhealthy\n"))
		}
	}))

	// Metrics endpoint - only available when METRICS=true
	mux.Handle("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.metrics == nil {
			http.Error(w, "Metrics not enabled", http.StatusNotFound)
			return
		}

		// Check if JSON format requested
		if r.URL.Query().Get("format") == "json" {
			w.Header().Set("Content-Type", "application/json")
			stats := m.metrics.GetStats()
			json.NewEncoder(w).Encode(stats)
			return
		}

		// Default: Prometheus format (use custom registry)
		promhttp.HandlerFor(m.metrics.registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
	}))

	port := os.Getenv("METRICS_PORT")
	if port == "" {
		port = "9090"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
	}
}

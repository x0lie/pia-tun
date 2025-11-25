package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	// Internal state tracking (for JSON endpoint)
	TotalChecks        int64
	FailedChecks       int64
	SuccessfulChecks   int64
	TotalReconnects    int64
	LastCheckTime      time.Time
	LastCheckDuration  time.Duration
	UptimeStart        time.Time

	// VPN-specific state
	CurrentServer      string
	CurrentIP          string
	BytesReceived      int64
	BytesTransmitted   int64
	ConnectedAt        time.Time

	// Server performance tracking
	ServerLatency      int64
	ServerUptime       time.Duration

	// WAN check state
	WANChecksTotal     int64
	WANChecksFailed    int64

	mu sync.Mutex

	// Prometheus metrics
	healthChecksTotal       prometheus.Counter
	healthChecksSuccess     prometheus.Counter
	healthChecksFailed      prometheus.Counter
	reconnectsTotal         prometheus.Counter
	checkDurationHistogram  prometheus.Histogram
	successRate             prometheus.Gauge
	bytesReceivedTotal      prometheus.Counter
	bytesTransmittedTotal   prometheus.Counter
	serverLatencyGauge      prometheus.Gauge
	serverUptimeGauge       prometheus.Gauge
	wanChecksTotal          prometheus.Counter
	wanChecksFailed         prometheus.Counter
	vpnInfo                 *prometheus.GaugeVec
}

func NewMetrics() *Metrics {
	m := &Metrics{
		UptimeStart: time.Now(),
	}

	// Create Prometheus metrics
	m.healthChecksTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_health_checks_total",
		Help: "Total number of health checks performed",
	})

	m.healthChecksSuccess = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_health_checks_successful_total",
		Help: "Total number of successful health checks",
	})

	m.healthChecksFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_health_checks_failed_total",
		Help: "Total number of failed health checks",
	})

	m.reconnectsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_reconnects_total",
		Help: "Total number of VPN reconnections",
	})

	m.checkDurationHistogram = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "vpn_check_duration_seconds",
		Help:    "Histogram of health check durations in seconds",
		Buckets: prometheus.ExponentialBuckets(0.01, 2, 10), // 10ms to ~10s
	})

	m.successRate = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vpn_success_rate",
		Help: "Health check success rate (0-1)",
	})

	m.bytesReceivedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_bytes_received_total",
		Help: "Total bytes received through VPN",
	})

	m.bytesTransmittedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_bytes_transmitted_total",
		Help: "Total bytes transmitted through VPN",
	})

	m.serverLatencyGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vpn_server_latency_milliseconds",
		Help: "Initial connection latency to VPN server",
	})

	m.serverUptimeGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vpn_server_uptime_seconds",
		Help: "Time connected to current server",
	})

	m.wanChecksTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_wan_checks_total",
		Help: "Total number of WAN connectivity checks",
	})

	m.wanChecksFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_wan_checks_failed_total",
		Help: "Total number of failed WAN checks",
	})

	m.vpnInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "vpn_info",
			Help: "VPN connection information (server and IP as labels)",
		},
		[]string{"server", "ip"},
	)

	// Register all metrics
	prometheus.MustRegister(
		m.healthChecksTotal,
		m.healthChecksSuccess,
		m.healthChecksFailed,
		m.reconnectsTotal,
		m.checkDurationHistogram,
		m.successRate,
		m.bytesReceivedTotal,
		m.bytesTransmittedTotal,
		m.serverLatencyGauge,
		m.serverUptimeGauge,
		m.wanChecksTotal,
		m.wanChecksFailed,
		m.vpnInfo,
	)

	// Add uptime as a gauge function
	prometheus.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "vpn_uptime_seconds",
			Help: "Total uptime in seconds",
		},
		func() float64 {
			return time.Since(m.UptimeStart).Seconds()
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

	// Update success rate
	if m.TotalChecks > 0 {
		rate := float64(m.SuccessfulChecks) / float64(m.TotalChecks)
		m.successRate.Set(rate)
	}
}

func (m *Metrics) RecordWANCheck(success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.WANChecksTotal++
	if !success {
		m.WANChecksFailed++
	}

	// Update Prometheus metrics
	m.wanChecksTotal.Inc()
	if !success {
		m.wanChecksFailed.Inc()
	}
}

func (m *Metrics) UpdateVPNInfo(server, ip string, rx, tx int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Update internal state
	if m.CurrentServer != server && server != "" {
		m.ConnectedAt = time.Now()
		m.CurrentServer = server

		// Update vpn_info label (reset old, set new)
		m.vpnInfo.Reset()
		if server != "" && ip != "" {
			m.vpnInfo.WithLabelValues(server, ip).Set(1)
		}
	}

	if m.CurrentServer != "" {
		m.ServerUptime = time.Since(m.ConnectedAt)
		m.serverUptimeGauge.Set(m.ServerUptime.Seconds())
	}

	m.CurrentIP = ip

	// Update bytes counters (handle resets from container restarts)
	if rx >= m.BytesReceived {
		// Normal case: increment
		delta := rx - m.BytesReceived
		if delta > 0 {
			m.bytesReceivedTotal.Add(float64(delta))
		}
	} else {
		// Counter reset (reconnection or restart)
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

func (m *Metrics) SetServerLatency(latency int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ServerLatency = latency
	m.serverLatencyGauge.Set(float64(latency))
}

func (m *Metrics) RecordReconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TotalReconnects++
	m.reconnectsTotal.Inc()
}

func (m *Metrics) GetStats() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	uptime := time.Since(m.UptimeStart)
	successRate := float64(0)
	if m.TotalChecks > 0 {
		successRate = float64(m.SuccessfulChecks) / float64(m.TotalChecks) * 100
	}

	wanSuccessRate := float64(100)
	if m.WANChecksTotal > 0 {
		wanSuccessRate = float64(m.WANChecksTotal-m.WANChecksFailed) / float64(m.WANChecksTotal) * 100
	}

	return map[string]interface{}{
		"total_checks":           m.TotalChecks,
		"successful_checks":      m.SuccessfulChecks,
		"failed_checks":          m.FailedChecks,
		"success_rate":           fmt.Sprintf("%.2f%%", successRate),
		"success_rate_decimal":   successRate / 100,
		"total_reconnects":       m.TotalReconnects,
		"uptime_seconds":         int(uptime.Seconds()),
		"uptime_formatted":       formatDuration(uptime),
		"last_check":             m.LastCheckTime.Format("2006-01-02 15:04:05"),
		"last_check_duration_ms": m.LastCheckDuration.Milliseconds(),
		"current_server":         m.CurrentServer,
		"current_ip":             m.CurrentIP,
		"bytes_received":         m.BytesReceived,
		"bytes_transmitted":      m.BytesTransmitted,
		"total_bytes":            m.BytesReceived + m.BytesTransmitted,
		"server_latency_ms":      m.ServerLatency,
		"server_uptime_seconds":  int(m.ServerUptime.Seconds()),
		"server_uptime_formatted": formatDuration(m.ServerUptime),
		"wan_checks_total":       m.WANChecksTotal,
		"wan_checks_failed":      m.WANChecksFailed,
		"wan_success_rate":       fmt.Sprintf("%.2f%%", wanSuccessRate),
	}
}

func startMetricsServer(m *Monitor) {
	mux := http.NewServeMux()

	// Default /metrics endpoint - Prometheus format
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

		// Default: Prometheus format
		promhttp.Handler().ServeHTTP(w, r)
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
		fmt.Fprintf(os.Stderr, "Metrics server error: %v\n", err)
	}
}

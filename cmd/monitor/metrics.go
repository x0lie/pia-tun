package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Metrics struct {
	TotalChecks        int64
	FailedChecks       int64
	SuccessfulChecks   int64
	TotalReconnects    int64
	LastCheckTime      time.Time
	LastCheckDuration  time.Duration
	UptimeStart        time.Time

	// VPN-specific metrics
	CurrentServer      string
	CurrentIP          string
	BytesReceived      int64
	BytesTransmitted   int64
	ConnectedAt        time.Time

	// Server performance tracking
	ServerLatency      int64
	ServerUptime       time.Duration

	// Performance metrics
	AvgCheckDuration   time.Duration
	MaxCheckDuration   time.Duration
	MinCheckDuration   time.Duration

	// WAN check metrics
	WANChecksTotal     int64
	WANChecksFailed    int64

	mu                 sync.Mutex
}

func NewMetrics() *Metrics {
	return &Metrics{
		UptimeStart:      time.Now(),
		MinCheckDuration: time.Hour,
	}
}

func (m *Metrics) RecordCheck(success bool, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TotalChecks++
	m.LastCheckTime = time.Now()
	m.LastCheckDuration = duration

	if duration > m.MaxCheckDuration {
		m.MaxCheckDuration = duration
	}
	if duration < m.MinCheckDuration {
		m.MinCheckDuration = duration
	}
	if m.AvgCheckDuration == 0 {
		m.AvgCheckDuration = duration
	} else {
		m.AvgCheckDuration = (m.AvgCheckDuration*9 + duration) / 10
	}

	if success {
		m.SuccessfulChecks++
	} else {
		m.FailedChecks++
	}
}

func (m *Metrics) RecordWANCheck(success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.WANChecksTotal++
	if !success {
		m.WANChecksFailed++
	}
}

func (m *Metrics) UpdateVPNInfo(server, ip string, rx, tx int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.CurrentServer != server && server != "" {
		m.ConnectedAt = time.Now()
		m.CurrentServer = server
	}

	if m.CurrentServer != "" {
		m.ServerUptime = time.Since(m.ConnectedAt)
	}

	m.CurrentIP = ip
	m.BytesReceived = rx
	m.BytesTransmitted = tx
}

func (m *Metrics) SetServerLatency(latency int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ServerLatency = latency
}

func (m *Metrics) RecordReconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TotalReconnects++
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
		"avg_check_duration_ms":  m.AvgCheckDuration.Milliseconds(),
		"max_check_duration_ms":  m.MaxCheckDuration.Milliseconds(),
		"min_check_duration_ms":  m.MinCheckDuration.Milliseconds(),
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
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if m.metrics == nil {
			http.Error(w, "Metrics not enabled", http.StatusNotFound)
			return
		}

		stats := m.metrics.GetStats()

		acceptHeader := r.Header.Get("Accept")
		if strings.Contains(acceptHeader, "text/plain") || r.URL.Query().Get("format") == "prometheus" {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")

			fmt.Fprintf(w, "# HELP vpn_uptime_seconds Total uptime in seconds\n")
			fmt.Fprintf(w, "# TYPE vpn_uptime_seconds gauge\n")
			fmt.Fprintf(w, "vpn_uptime_seconds %d\n\n", stats["uptime_seconds"])

			fmt.Fprintf(w, "# HELP vpn_health_checks_total Total number of health checks performed\n")
			fmt.Fprintf(w, "# TYPE vpn_health_checks_total counter\n")
			fmt.Fprintf(w, "vpn_health_checks_total %d\n\n", stats["total_checks"])

			fmt.Fprintf(w, "# HELP vpn_health_checks_successful_total Total number of successful health checks\n")
			fmt.Fprintf(w, "# TYPE vpn_health_checks_successful_total counter\n")
			fmt.Fprintf(w, "vpn_health_checks_successful_total %d\n\n", stats["successful_checks"])

			fmt.Fprintf(w, "# HELP vpn_health_checks_failed_total Total number of failed health checks\n")
			fmt.Fprintf(w, "# TYPE vpn_health_checks_failed_total counter\n")
			fmt.Fprintf(w, "vpn_health_checks_failed_total %d\n\n", stats["failed_checks"])

			fmt.Fprintf(w, "# HELP vpn_success_rate Health check success rate (0-1)\n")
			fmt.Fprintf(w, "# TYPE vpn_success_rate gauge\n")
			fmt.Fprintf(w, "vpn_success_rate %.4f\n\n", stats["success_rate_decimal"])

			fmt.Fprintf(w, "# HELP vpn_reconnects_total Total number of reconnections\n")
			fmt.Fprintf(w, "# TYPE vpn_reconnects_total counter\n")
			fmt.Fprintf(w, "vpn_reconnects_total %d\n\n", stats["total_reconnects"])

			fmt.Fprintf(w, "# HELP vpn_check_duration_milliseconds Last health check duration in milliseconds\n")
			fmt.Fprintf(w, "# TYPE vpn_check_duration_milliseconds gauge\n")
			fmt.Fprintf(w, "vpn_check_duration_milliseconds %d\n\n", stats["last_check_duration_ms"])

			fmt.Fprintf(w, "# HELP vpn_check_duration_avg_milliseconds Average health check duration in milliseconds\n")
			fmt.Fprintf(w, "# TYPE vpn_check_duration_avg_milliseconds gauge\n")
			fmt.Fprintf(w, "vpn_check_duration_avg_milliseconds %d\n\n", stats["avg_check_duration_ms"])

			fmt.Fprintf(w, "# HELP vpn_bytes_received_total Total bytes received through VPN\n")
			fmt.Fprintf(w, "# TYPE vpn_bytes_received_total counter\n")
			fmt.Fprintf(w, "vpn_bytes_received_total %d\n\n", stats["bytes_received"])

			fmt.Fprintf(w, "# HELP vpn_bytes_transmitted_total Total bytes transmitted through VPN\n")
			fmt.Fprintf(w, "# TYPE vpn_bytes_transmitted_total counter\n")
			fmt.Fprintf(w, "vpn_bytes_transmitted_total %d\n\n", stats["bytes_transmitted"])

			fmt.Fprintf(w, "# HELP vpn_server_latency_milliseconds Initial connection latency to VPN server\n")
			fmt.Fprintf(w, "# TYPE vpn_server_latency_milliseconds gauge\n")
			fmt.Fprintf(w, "vpn_server_latency_milliseconds %d\n\n", stats["server_latency_ms"])

			fmt.Fprintf(w, "# HELP vpn_server_uptime_seconds Time connected to current server\n")
			fmt.Fprintf(w, "# TYPE vpn_server_uptime_seconds gauge\n")
			fmt.Fprintf(w, "vpn_server_uptime_seconds %d\n\n", stats["server_uptime_seconds"])

			fmt.Fprintf(w, "# HELP vpn_wan_checks_total Total number of WAN connectivity checks\n")
			fmt.Fprintf(w, "# TYPE vpn_wan_checks_total counter\n")
			fmt.Fprintf(w, "vpn_wan_checks_total %d\n\n", stats["wan_checks_total"])

			fmt.Fprintf(w, "# HELP vpn_wan_checks_failed_total Total number of failed WAN checks\n")
			fmt.Fprintf(w, "# TYPE vpn_wan_checks_failed_total counter\n")
			fmt.Fprintf(w, "vpn_wan_checks_failed_total %d\n\n", stats["wan_checks_failed"])

			if server, ok := stats["current_server"].(string); ok && server != "" {
				fmt.Fprintf(w, "# HELP vpn_info VPN connection information\n")
				fmt.Fprintf(w, "# TYPE vpn_info gauge\n")
				fmt.Fprintf(w, "vpn_info{server=\"%s\",ip=\"%s\"} 1\n\n",
					stats["current_server"], stats["current_ip"])
			}
		} else {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(stats)
		}
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		result, err := m.checkVPNHealth()

		status := "healthy"
		statusCode := http.StatusOK
		if err != nil {
			status = "unhealthy"
			statusCode = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":         status,
			"interface_up":   result.InterfaceUp,
			"connectivity":   result.Connectivity,
			"check_duration": result.CheckDuration.String(),
			"error":          fmt.Sprintf("%v", err),
		})
	})

	port := os.Getenv("METRICS_PORT")
	if port == "" {
		port = "9090"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      nil,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "Metrics server error: %v\n", err)
	}
}

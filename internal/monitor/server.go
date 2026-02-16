package monitor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func startHTTPServer(m *Monitor) {
	mux := http.NewServeMux()

	mux.Handle("/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.isHealthy() {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("healthy\n"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("unhealthy\n"))
		}
	}))

	mux.Handle("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.config.MetricsEnabled {
			http.Error(w, "Metrics not enabled", http.StatusNotFound)
			return
		}

		if r.URL.Query().Get("format") == "json" {
			w.Header().Set("Content-Type", "application/json")
			stats := m.metrics.GetStats()
			json.NewEncoder(w).Encode(stats)
			return
		}

		promhttp.HandlerFor(m.metrics.Registry(), promhttp.HandlerOpts{}).ServeHTTP(w, r)
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

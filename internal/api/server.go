package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/metrics"
)

type Server struct {
	server *http.Server
	log    *log.Logger
}

func New(port int, killswitchFn func() bool, connectionFn func() bool, m *metrics.Metrics) *Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if killswitchFn() {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ready\n"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("not ready\n"))
		}
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if connectionFn() {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("healthy\n"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("unhealthy\n"))
		}
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if !m.Enabled() {
			http.Error(w, "Metrics not enabled", http.StatusNotFound)
			return
		}

		if r.URL.Query().Get("format") == "json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(m.GetStats())
			return
		}

		promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{}).ServeHTTP(w, r)
	})

	return &Server{
		server: &http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: mux,
		},
		log: log.New("api"),
	}
}

func (s *Server) Start() {
	s.log.Debug("HTTP server listening on %s", s.server.Addr)
	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error(fmt.Sprintf("HTTP server error: %v", err))
	}
}

func (s *Server) Shutdown() {
	if err := s.server.Shutdown(context.Background()); err != nil {
		s.log.Debug("HTTP server shutdown error: %v", err)
	}
}

package proxy

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func (c *Config) StartHTTPProxy(ctx context.Context) error {
	// Start HTTP proxy (blocks until context is done or error)
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", c.HTTPPort),
		Handler:      c,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown on context cancellation
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP proxy error: %w", err)
	}

	return nil
}

// ServeHTTP implements the HTTP proxy handler.
func (c *Config) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if c.User != "" && c.Pass != "" {
		auth := r.Header.Get("Proxy-Authorization")
		if !c.checkAuth(auth) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="Proxy"`)
			http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
			return
		}
	}

	if r.Method == http.MethodConnect {
		c.handleTunneling(w, r)
		return
	}

	c.handleHTTP(w, r)
}

func (c *Config) checkAuth(auth string) bool {
	if !strings.HasPrefix(auth, "Basic ") {
		return false
	}

	payload, err := base64.StdEncoding.DecodeString(auth[6:])
	if err != nil {
		return false
	}

	pair := strings.SplitN(string(payload), ":", 2)
	if len(pair) != 2 {
		return false
	}

	userMatch := subtle.ConstantTimeCompare([]byte(pair[0]), []byte(c.User))
	passMatch := subtle.ConstantTimeCompare([]byte(pair[1]), []byte(c.Pass))

	return userMatch == 1 && passMatch == 1
}

func (c *Config) handleTunneling(w http.ResponseWriter, r *http.Request) {
	destConn, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer destConn.Close()

	w.WriteHeader(http.StatusOK)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer clientConn.Close()

	go transfer(destConn, clientConn)
	transfer(clientConn, destConn)
}

func (c *Config) handleHTTP(w http.ResponseWriter, r *http.Request) {
	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Proxy-Connection")

	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

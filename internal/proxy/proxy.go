package proxy

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/config"
)

// Proxy holds the proxy server configuration and state.
type Proxy struct {
	user      string
	pass      string
	httpPort  string
	socksPort string
}

// Run starts the HTTP and SOCKS5 proxy servers.
// This is the main entry point called by the dispatcher.
func Run(ctx context.Context) error {
	p := &Proxy{
		user:      config.GetSecret("PROXY_USER", "/run/secrets/proxy_user"),
		pass:      config.GetSecret("PROXY_PASS", "/run/secrets/proxy_pass"),
		httpPort:  config.GetEnvOrDefault("HTTP_PROXY_PORT", "8888"),
		socksPort: config.GetEnvOrDefault("SOCKS5_PORT", "1080"),
	}

	// Start SOCKS5 proxy in goroutine
	go p.startSOCKS5()

	// Start HTTP proxy (blocks until context is done or error)
	server := &http.Server{
		Addr:         ":" + p.httpPort,
		Handler:      p,
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
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.user != "" && p.pass != "" {
		auth := r.Header.Get("Proxy-Authorization")
		if !p.checkAuth(auth) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="Proxy"`)
			http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
			return
		}
	}

	if r.Method == http.MethodConnect {
		p.handleTunneling(w, r)
		return
	}

	p.handleHTTP(w, r)
}

func (p *Proxy) checkAuth(auth string) bool {
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

	userMatch := subtle.ConstantTimeCompare([]byte(pair[0]), []byte(p.user))
	passMatch := subtle.ConstantTimeCompare([]byte(pair[1]), []byte(p.pass))

	return userMatch == 1 && passMatch == 1
}

func (p *Proxy) handleTunneling(w http.ResponseWriter, r *http.Request) {
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

func transfer(dst io.Writer, src io.Reader) {
	io.Copy(dst, src)
}

func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
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

func (p *Proxy) startSOCKS5() {
	listener, err := net.Listen("tcp", ":"+p.socksPort)
	if err != nil {
		log.Fatalf("Failed to start SOCKS5 proxy: %v", err)
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("SOCKS5 accept error: %v", err)
			continue
		}
		go p.handleSOCKS5(conn)
	}
}

func (p *Proxy) handleSOCKS5(conn net.Conn) {
	defer conn.Close()

	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil || n < 2 {
		return
	}

	if buf[0] != 0x05 {
		return
	}

	authRequired := p.user != "" && p.pass != ""

	if authRequired {
		conn.Write([]byte{0x05, 0x02})

		n, err = conn.Read(buf)
		if err != nil || n < 3 {
			return
		}

		userLen := int(buf[1])
		if n < 2+userLen+1 {
			return
		}
		user := string(buf[2 : 2+userLen])
		passLen := int(buf[2+userLen])
		if n < 2+userLen+1+passLen {
			return
		}
		pass := string(buf[2+userLen+1 : 2+userLen+1+passLen])

		if user != p.user || pass != p.pass {
			conn.Write([]byte{0x01, 0x01})
			return
		}

		conn.Write([]byte{0x01, 0x00})

		n, err = conn.Read(buf)
		if err != nil || n < 4 {
			return
		}
	} else {
		conn.Write([]byte{0x05, 0x00})

		n, err = conn.Read(buf)
		if err != nil || n < 4 {
			return
		}
	}

	if buf[1] != 0x01 {
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	var host string
	var port uint16

	switch buf[3] {
	case 0x01: // IPv4
		host = fmt.Sprintf("%d.%d.%d.%d", buf[4], buf[5], buf[6], buf[7])
		port = uint16(buf[8])<<8 | uint16(buf[9])
	case 0x03: // Domain
		hostLen := int(buf[4])
		host = string(buf[5 : 5+hostLen])
		port = uint16(buf[5+hostLen])<<8 | uint16(buf[6+hostLen])
	case 0x04: // IPv6
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	destConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 10*time.Second)
	if err != nil {
		conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer destConn.Close()

	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	go transfer(destConn, conn)
	transfer(conn, destConn)
}

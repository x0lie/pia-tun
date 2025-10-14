package main

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	proxyUser = os.Getenv("PROXY_USER")
	proxyPass = os.Getenv("PROXY_PASS")
	httpPort  = getEnvOrDefault("HTTP_PROXY_PORT", "8888")
	socksPort = getEnvOrDefault("SOCKS5_PORT", "1080")
)

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// HTTP Proxy Handler
type HTTPProxyHandler struct{}

func (h *HTTPProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check authentication if configured
	if proxyUser != "" && proxyPass != "" {
		auth := r.Header.Get("Proxy-Authorization")
		if !checkAuth(auth) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="Proxy"`)
			http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
			return
		}
	}

	// Handle CONNECT for HTTPS
	if r.Method == http.MethodConnect {
		handleTunneling(w, r)
		return
	}

	// Handle HTTP
	handleHTTP(w, r)
}

func checkAuth(auth string) bool {
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

	// Constant-time comparison to prevent timing attacks
	userMatch := subtle.ConstantTimeCompare([]byte(pair[0]), []byte(proxyUser))
	passMatch := subtle.ConstantTimeCompare([]byte(pair[1]), []byte(proxyPass))

	return userMatch == 1 && passMatch == 1
}

func handleTunneling(w http.ResponseWriter, r *http.Request) {
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

	// Bidirectional copy
	go transfer(destConn, clientConn)
	transfer(clientConn, destConn)
}

func transfer(dst io.Writer, src io.Reader) {
	io.Copy(dst, src)
}

func handleHTTP(w http.ResponseWriter, r *http.Request) {
	// Remove proxy headers
	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Proxy-Connection")

	// Create new request
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// SOCKS5 Proxy Handler
func handleSOCKS5(conn net.Conn) {
	defer conn.Close()

	// SOCKS5 handshake
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil || n < 2 {
		return
	}

	// Check version
	if buf[0] != 0x05 {
		return
	}

	// Check if auth is required
	authRequired := proxyUser != "" && proxyPass != ""
	
	if authRequired {
		// Send auth required
		conn.Write([]byte{0x05, 0x02}) // Username/password auth
		
		// Read auth request
		n, err = conn.Read(buf)
		if err != nil || n < 3 {
			return
		}
		
		// Parse username and password
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
		
		// Check credentials
		if user != proxyUser || pass != proxyPass {
			conn.Write([]byte{0x01, 0x01}) // Auth failed
			return
		}
		
		// Auth success
		conn.Write([]byte{0x01, 0x00})
		
		// Read connect request
		n, err = conn.Read(buf)
		if err != nil || n < 4 {
			return
		}
	} else {
		// No auth required
		conn.Write([]byte{0x05, 0x00})
		
		// Read connect request
		n, err = conn.Read(buf)
		if err != nil || n < 4 {
			return
		}
	}

	// Parse connect request
	if buf[1] != 0x01 { // Only support CONNECT
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
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // Not supported
		return
	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Connect to destination
	destConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 10*time.Second)
	if err != nil {
		conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // Connection refused
		return
	}
	defer destConn.Close()

	// Success response
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// Bidirectional copy
	go transfer(destConn, conn)
	transfer(conn, destConn)
}

func startSOCKS5() {
	listener, err := net.Listen("tcp", ":"+socksPort)
	if err != nil {
		log.Fatalf("Failed to start SOCKS5 proxy: %v", err)
	}
	defer listener.Close()

	authStatus := "no authentication"
	if proxyUser != "" {
		authStatus = "authenticated"
	}
	log.Printf("SOCKS5 proxy listening on :%s (%s)", socksPort, authStatus)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("SOCKS5 accept error: %v", err)
			continue
		}
		go handleSOCKS5(conn)
	}
}

func main() {
	// Start SOCKS5 proxy in goroutine
	go startSOCKS5()

	// Start HTTP proxy
	handler := &HTTPProxyHandler{}
	server := &http.Server{
		Addr:         ":" + httpPort,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	authStatus := "no authentication"
	if proxyUser != "" {
		authStatus = "authenticated"
	}
	log.Printf("HTTP proxy listening on :%s (%s)", httpPort, authStatus)

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Failed to start HTTP proxy: %v", err)
	}
}

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// TestResult represents the result of a single leak test attempt
type TestResult struct {
	Timestamp time.Time
	Protocol  string
	Endpoint  string
	Success   bool
	IP        string
	Leaked    bool
	Error     string
}

// Protocol test functions

// testHTTP attempts an HTTP connection to detect leaks
func testHTTP(ctx context.Context, realIP string) TestResult {
	result := TestResult{
		Timestamp: time.Now(),
		Protocol:  "http",
		Endpoint:  "http://ifconfig.me",
	}

	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "http://ifconfig.me", nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	resp, err := client.Do(req)
	if err != nil {
		// Connection failed (expected if killswitch working)
		result.Error = err.Error()
		return result
	}
	defer resp.Body.Close()

	// If we got a response, read the IP
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Success = true
	result.IP = strings.TrimSpace(string(body))

	// Check if it matches real IP (leak!)
	if realIP != "" && result.IP == realIP {
		result.Leaked = true
	}

	return result
}

// testHTTPS attempts an HTTPS connection to detect leaks
func testHTTPS(ctx context.Context, realIP string) TestResult {
	result := TestResult{
		Timestamp: time.Now(),
		Protocol:  "https",
		Endpoint:  "https://ifconfig.me",
	}

	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://ifconfig.me", nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	resp, err := client.Do(req)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Success = true
	result.IP = strings.TrimSpace(string(body))

	if realIP != "" && result.IP == realIP {
		result.Leaked = true
	}

	return result
}

// testDNS attempts DNS resolution to detect leaks
func testDNS(ctx context.Context, realIP string) TestResult {
	result := TestResult{
		Timestamp: time.Now(),
		Protocol:  "dns",
		Endpoint:  "8.8.8.8:53",
	}

	// Try to resolve using Google DNS directly (should be blocked)
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: 3 * time.Second,
			}
			return d.DialContext(ctx, "udp", "8.8.8.8:53")
		},
	}

	_, err := resolver.LookupHost(ctx, "google.com")
	if err != nil {
		// DNS query failed (expected if killswitch working)
		result.Error = err.Error()
		return result
	}

	// If we got a response, DNS leaked
	result.Success = true
	result.Leaked = true // Any successful DNS query to external resolver is a leak

	return result
}

// testICMP attempts to ping external servers
func testICMP(ctx context.Context, realIP string) TestResult {
	result := TestResult{
		Timestamp: time.Now(),
		Protocol:  "icmp",
		Endpoint:  "8.8.8.8",
	}

	// Use ping command with timeout
	cmd := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "2", "8.8.8.8")
	err := cmd.Run()

	if err != nil {
		// Ping failed (expected if killswitch working)
		result.Error = err.Error()
		return result
	}

	// If ping succeeded, ICMP leaked
	result.Success = true
	result.Leaked = true // Any successful ICMP to external host is a leak

	return result
}

// testAlternativeDNS tests against Cloudflare DNS
func testAlternativeDNS(ctx context.Context, realIP string) TestResult {
	result := TestResult{
		Timestamp: time.Now(),
		Protocol:  "dns",
		Endpoint:  "1.1.1.1:53",
	}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: 3 * time.Second,
			}
			return d.DialContext(ctx, "udp", "1.1.1.1:53")
		},
	}

	_, err := resolver.LookupHost(ctx, "cloudflare.com")
	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Success = true
	result.Leaked = true

	return result
}

// testUDPGeneric attempts UDP connections to various ports
func testUDPGeneric(ctx context.Context, realIP string) TestResult {
	// Cycle through different UDP targets
	targets := []struct {
		host string
		port string
		desc string
	}{
		{"8.8.8.8", "123", "NTP"},        // NTP time server
		{"1.1.1.1", "443", "QUIC"},       // QUIC/HTTP3
		{"8.8.8.8", "80", "UDP-generic"}, // Generic UDP
	}

	// Pick target based on current time to rotate
	target := targets[int(time.Now().UnixNano())%len(targets)]

	result := TestResult{
		Timestamp: time.Now(),
		Protocol:  "udp",
		Endpoint:  fmt.Sprintf("%s:%s (%s)", target.host, target.port, target.desc),
	}

	var d net.Dialer
	d.Timeout = 3 * time.Second

	// Attempt UDP connection
	conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(target.host, target.port))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer conn.Close()

	// Try to send a packet
	_, err = conn.Write([]byte("test"))
	if err != nil {
		result.Error = err.Error()
		return result
	}

	// Connection succeeded - potential leak
	// Note: UDP doesn't guarantee delivery, so "success" just means packet was sent
	result.Success = true
	result.Leaked = true // Any successful UDP send to external host is a leak

	return result
}

// testRawTCP attempts a raw TCP connection
func testRawTCP(ctx context.Context, realIP string) TestResult {
	result := TestResult{
		Timestamp: time.Now(),
		Protocol:  "tcp",
		Endpoint:  "1.1.1.1:80",
	}

	var d net.Dialer
	d.Timeout = 3 * time.Second

	conn, err := d.DialContext(ctx, "tcp", "1.1.1.1:80")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer conn.Close()

	// Connection succeeded - potential leak
	result.Success = true
	result.Leaked = true // Any successful connection to external host is a leak

	return result
}

// testBypassRestrictions verifies bypass routes can't be exploited
func testBypassRestrictions(ctx context.Context, realIP string) TestResult {
	// Test bypass IPs on WRONG ports (should be blocked)
	// Bypass should ONLY work for TCP port 13 (DAYTIME), not other ports
	bypassIPs := []string{
		"129.6.15.28",  // NIST time server
		"129.6.15.29",  // NIST time server
		"132.163.96.1", // NIST time server
	}

	wrongPorts := []string{"80", "443", "22", "53"}

	// Pick a random bypass IP and wrong port
	testIP := bypassIPs[int(time.Now().UnixNano())%len(bypassIPs)]
	testPort := wrongPorts[int(time.Now().UnixNano())%len(wrongPorts)]

	result := TestResult{
		Timestamp: time.Now(),
		Protocol:  "bypass",
		Endpoint:  fmt.Sprintf("%s:%s (should be blocked)", testIP, testPort),
	}

	var d net.Dialer
	d.Timeout = 3 * time.Second

	// Try to connect to bypass IP on wrong port
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(testIP, testPort))
	if err != nil {
		// Connection failed (GOOD - bypass is restricted)
		result.Error = err.Error()
		return result
	}
	defer conn.Close()

	// Connection succeeded (BAD - bypass route is not properly restricted!)
	result.Success = true
	result.Leaked = true // Bypass route is exploitable
	result.IP = testIP

	return result
}

// executeTest runs the appropriate test function based on protocol
func executeTest(ctx context.Context, protocol, realIP string) TestResult {
	switch protocol {
	case "http":
		return testHTTP(ctx, realIP)
	case "https":
		return testHTTPS(ctx, realIP)
	case "dns":
		// Alternate between different DNS servers
		if time.Now().UnixNano()%2 == 0 {
			return testDNS(ctx, realIP)
		}
		return testAlternativeDNS(ctx, realIP)
	case "udp":
		return testUDPGeneric(ctx, realIP)
	case "bypass":
		return testBypassRestrictions(ctx, realIP)
	case "icmp":
		return testICMP(ctx, realIP)
	case "tcp":
		return testRawTCP(ctx, realIP)
	default:
		return TestResult{
			Timestamp: time.Now(),
			Protocol:  protocol,
			Error:     fmt.Sprintf("unknown protocol: %s", protocol),
		}
	}
}

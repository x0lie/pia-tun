package vpn

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
)

// VerifyConnection confirms traffic routes through the VPN.
func VerifyConnection(ctx context.Context, dnsMode string, dnsServers []string, preVPNIP string) error {
	logger := log.New("verify")
	handshakeTimeout := 6 * time.Second
	ipTimeout := 4 * time.Second
	start := time.Now()

	// Trigger handshake by sending packets in background
	triggerCtx, stopTrigger := context.WithCancel(ctx)
	go triggerHandshake(triggerCtx)
	defer stopTrigger()

	// Wait for WireGuard handshake
	logger.Debug("Waiting for handshake...")
	err := waitForHandshake(ctx, handshakeTimeout)
	if err != nil {
		return fmt.Errorf("waiting for handshake: %w", err)
	}
	log.Success("Handshake complete (%.1fs)", time.Since(start).Seconds())
	stopTrigger()

	// Log PIA DNS if enabled
	if dnsMode == "pia" {
		log.Success("DNS: PIA (%s)", strings.Join(dnsServers, ", "))
	}

	// Get public IP (parallel requests to multiple services)
	logger.Debug("Retrieving Public IP")
	publicIP, err := getPublicIP(ctx, dnsMode, dnsServers, ipTimeout)
	if err != nil {
		log.Warning("Could not verify IP:\n    %s", err)
		return nil // Must return, IP comparison invalid
	}

	// Show Critical Error if IP hasn't changed
	if publicIP == preVPNIP {
		log.Error("CRITICAL: Public IP matches pre-VPN IP - possible leak!")
		return fmt.Errorf("IP leak detected: traffic not routing through VPN!")
	}
	log.Success("External IP: %s%s%s%s", log.ColorGreen, log.ColorBold, publicIP, log.ColorReset)

	return nil
}

// triggerHandshake sends a UDP packet to initiate the WireGuard handshake.
func triggerHandshake(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Initial ping
	exec.Command("ping", "-c", "1", "-W", "1", "1.1.1.1").Run()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			exec.Command("ping", "-c", "1", "-W", "1", "1.1.1.1").Run()
		}
	}
}

// waitForHandshake polls until WireGuard reports a successful handshake.
func waitForHandshake(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := exec.Command("wg", "show", "pia0", "latest-handshakes").Output()
		if err == nil {
			parts := strings.Fields(string(output))
			if len(parts) >= 2 {
				ts, _ := strconv.ParseInt(parts[1], 10, 64)
				if ts > 0 {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("handshake timeout after %v", timeout)
}

// getPublicIP fetches public IP from multiple services in parallel.
func getPublicIP(ctx context.Context, dnsMode string, dnsServers []string, timeout time.Duration) (string, error) {
	services := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
		"https://checkip.amazonaws.com",
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		ip  string
		err error
	}

	dialer := &net.Dialer{Timeout: timeout}
	if dnsMode == "pia" && len(dnsServers) > 0 {
		dialer.Resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: timeout}
				return d.DialContext(ctx, "udp", net.JoinHostPort(dnsServers[0], "53"))
			},
		}
	}
	transport := &http.Transport{DialContext: dialer.DialContext}
	client := &http.Client{Timeout: timeout, Transport: transport}
	results := make(chan result, len(services))
	var wg sync.WaitGroup

	for _, svc := range services {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()

			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				results <- result{err: err}
				return
			}

			resp, err := client.Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				results <- result{err: fmt.Errorf("%s: status %d", url, resp.StatusCode)}
				return
			}

			body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
			if err != nil {
				results <- result{err: err}
				return
			}

			ip := strings.TrimSpace(string(body))
			if net.ParseIP(ip) != nil {
				results <- result{ip: ip}
			} else {
				results <- result{err: fmt.Errorf("invalid IP: %s", ip)}
			}
		}(svc)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Return first valid IP
	lastErr := fmt.Errorf("no valid IP from any service")
	for r := range results {
		if r.ip != "" {
			return r.ip, nil
		}
		if r.err != nil {
			lastErr = r.err
		}
	}
	return "", lastErr
}

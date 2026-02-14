package vpn

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
)

// VerifyConnection confirms traffic routes through the VPN.
func VerifyConnection(ctx context.Context) (string, error) {
	logger := &log.Logger{
		Enabled: os.Getenv("_LOG_LEVEL") == "2",
		Prefix:  "verify",
	}
	start := time.Now()

	// Trigger handshake by sending packets in background
	triggerCtx, stopTrigger := context.WithCancel(ctx)
	go triggerHandshake(triggerCtx)
	defer stopTrigger()

	// Wait for WireGuard handshake
	logger.Debug("Waiting for handshake...")
	handshakeErr := waitForHandshake(ctx, 12*time.Second)
	if handshakeErr != nil {
		log.Warning("Handshake wait timed out, continuing anyway")
	} else {
		log.Success(fmt.Sprintf("Handshake complete (%.1fs)", time.Since(start).Seconds()))
	}

	// Log DNS setting
	dnsIP := getPrimaryDNS()
	dns := formatDNS(dnsIP)
	log.Success(fmt.Sprintf("DNS: %s", dns))

	// Get public IP (parallel requests to multiple services)
	logger.Debug("Retrieving Public IP")
	publicIP, err := getPublicIP(ctx, 5*time.Second)
	if err != nil {
		logger.Debug("First IP check failed, retrying: %v", err)
		time.Sleep(2 * time.Second)
		publicIP, err = getPublicIP(ctx, 5*time.Second)
		if err != nil {
			return "", err
		}
	}

	// Show Critical Error if IP hasn't changed
	if preVPNIP := readPreVPNIP(); preVPNIP != "" && publicIP == preVPNIP {
		log.Error("CRITICAL: Public IP matches pre-VPN IP - possible leak!")
		return "", fmt.Errorf("IP leak detected: traffic not routing through VPN!")
	}

	return publicIP, nil
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
	return fmt.Errorf("handshake timeout")
}

// getPublicIP fetches public IP from multiple services in parallel.
func getPublicIP(ctx context.Context, timeout time.Duration) (string, error) {
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

	results := make(chan result, len(services))
	var wg sync.WaitGroup

	client := &http.Client{Timeout: timeout}

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
	for r := range results {
		if r.ip != "" {
			return r.ip, nil
		}
	}

	return "", fmt.Errorf("Could not determine IP - DNS Misconfigured?")
}

func readPreVPNIP() string {
	data, err := os.ReadFile("/tmp/real_ip")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func getPrimaryDNS() string {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "nameserver ") {
			return strings.TrimPrefix(line, "nameserver ")
		}
	}
	return ""
}

func formatDNS(ip string) string {
	switch ip {
	case "10.0.0.243", "10.0.0.242":
		return fmt.Sprintf("PIA (%s)", ip)
	case "209.222.18.222", "209.222.18.218":
		return fmt.Sprintf("PIA (%s)", ip)
	default:
		return ip
	}
}

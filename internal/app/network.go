package app

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
)

// IP detection services (all return plain text IP)
var ipServices = []string{
	"api.ipify.org",
	"icanhazip.com",
	"ifconfig.me",
}

const ipFetchTimeout = 5 * time.Second

// captureRealIP fetches the external IP address before VPN connection.
// This is used later by verify_connection.sh to confirm the VPN is working.
// The IP is written to /tmp/real_ip for backward compatibility.
func (a *App) captureRealIP(ctx context.Context) {
	log.Step("Capturing pre-VPN IP address...")

	// Resolve all services and prepare exemptions
	type target struct {
		hostname string
		ip       string
		exempt   *firewall.Exemption
	}
	var targets []target

	for _, hostname := range ipServices {
		ips, err := a.resolver.Resolve(ctx, hostname)
		if err != nil || len(ips) == 0 {
			a.log.Debug("Failed to resolve %s: %v", hostname, err)
			continue
		}

		ip := ips[0]
		exempt, err := a.fw.AddTemporaryExemption(ip, "443", "tcp", "ipcheck")
		if err != nil {
			a.log.Debug("Failed to add exemption for %s: %v", hostname, err)
			continue
		}

		targets = append(targets, target{hostname: hostname, ip: ip, exempt: exempt})
	}

	if len(targets) == 0 {
		log.Error("Cannot resolve any IP detection services")
		return
	}

	// Clean up all exemptions when done
	defer func() {
		for _, t := range targets {
			a.fw.RemoveTemporaryExemption(t.exempt)
		}
	}()

	// Fetch IP in parallel, first success wins
	ctx, cancel := context.WithTimeout(ctx, ipFetchTimeout)
	defer cancel()

	type result struct {
		ip  string
		src string
	}
	resultCh := make(chan result, len(targets))

	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(hostname, ip string) {
			defer wg.Done()
			if extIP, err := fetchExternalIP(ctx, hostname, ip); err == nil {
				resultCh <- result{ip: extIP, src: hostname}
			}
		}(t.hostname, t.ip)
	}

	// Close channel when all goroutines complete
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Take first result
	res, ok := <-resultCh
	if !ok {
		log.Error("Cannot fetch external IP from any service")
		return
	}
	cancel() // Cancel remaining requests

	a.log.Debug("Got IP %s from %s", res.ip, res.src)

	// Write to /tmp/real_ip for verify_connection.sh compatibility
	if err := os.WriteFile("/tmp/real_ip", []byte(res.ip), 0644); err != nil {
		a.log.Debug("Failed to write /tmp/real_ip: %v", err)
	}

	log.Success("Real IP captured: " + res.ip)
}

// fetchExternalIP makes an HTTPS request to get the external IP.
func fetchExternalIP(ctx context.Context, hostname, resolvedIP string) (string, error) {
	client := &http.Client{
		Timeout: ipFetchTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				ServerName: hostname,
			},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(addr)
				return (&net.Dialer{Timeout: ipFetchTimeout}).DialContext(ctx, network, net.JoinHostPort(resolvedIP, port))
			},
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://"+hostname, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "curl/8.0") // Some services filter unusual UAs

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(body)), nil
}

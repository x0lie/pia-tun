package app

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
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
}

const ipFetchTimeout = 5 * time.Second

// captureRealIP fetches the external IP address before VPN connection.
// This is used later by vpn.verifyConnection to confirm the VPN is working.
func (a *App) captureRealIP(ctx context.Context) string {
	log.Step("Capturing pre-VPN IP address...")

	type target struct {
		hostname string
		ip       string
	}

	// Resolve ipServices
	resolved, _ := a.resolver.ResolveAll(ctx, ipServices)
	if len(resolved) == 0 {
		log.Error("Cannot resolve ip retrievers")
		return ""
	}

	// Batch Exemptions
	var targets []target
	specs := make([]firewall.Exemption, 0, len(ipServices))
	for hostname, result := range resolved {
		if result.NXDomain {
			continue
		}
		targets = append(targets, target{hostname: hostname, ip: result.IPs[0]})
		specs = append(specs, firewall.Exemption{IP: result.IPs[0], Port: "443", Proto: "tcp", Comment: hostname})
	}
	if err := a.fw.AddExemptions(specs...); err != nil {
		a.log.Debug("captureRealIP: %s", err)
	}

	// Clean up all exemptions when done
	defer a.fw.RemoveExemptions()

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
		return ""
	}
	cancel() // Cancel remaining requests

	a.log.Debug("Got IP %s from %s", res.ip, res.src)

	log.Success("Real IP captured: %s", res.ip)

	return res.ip
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

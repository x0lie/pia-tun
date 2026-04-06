package pia

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/x0lie/pia-tun/internal/apperrors"
)

// PIA's private cert only used by AddKey and portforward package
// (getSignature and bindPort), all others use System CAs. The getSignature
// and bindPort functions are the only PIA interactions not listed here.

const (
	AuthHostname       = "www.privateinternetaccess.com"
	ServerlistHostname = "serverlist.piaservers.net"
	authPath           = "/gtoken/generateToken"
	serverListPath     = "/vpninfo/servers/v6"
	addKeyPort         = "1337"
	caCertPath         = "/etc/pia-tun/ca.rsa.4096.crt"
)

// GenerateToken authenticates with PIA and returns a login token.
// ip is the www.privateinternetaccess.com hostname or IP.
// Returns ErrFatal for invalid credentials.
func GenerateToken(ctx context.Context, timeout time.Duration, ip, user, pass string) (string, error) {
	// Use hostname in URL for correct Host header, but connect to IP
	reqURL := fmt.Sprintf("https://%s%s", AuthHostname, authPath)

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("create auth request: %w", err)
	}
	req.SetBasicAuth(user, pass)

	// Create a client that connects to the IP but uses hostname for SNI
	authClient := newHostMappedClient(timeout, AuthHostname, ip)

	resp, err := authClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("%w: invalid credentials (HTTP %d)", apperrors.ErrFatal, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result struct {
		Token   string `json:"token"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	// PIA can return 200 with an error message for some auth failures.
	if result.Message == "authentication failed" {
		return "", fmt.Errorf("%w: invalid credentials (%s)", apperrors.ErrFatal, result.Message)
	}
	if result.Token == "" {
		return "", fmt.Errorf("empty token in response")
	}

	return result.Token, nil
}

// FetchServerList fetches the PIA server list.
// ip is the serverlist.piaservers.net hostname or IP
func FetchServerList(ctx context.Context, timeout time.Duration, ip string) ([]Server, error) {
	// Use hostname in URL for correct Host header, but connect to IP
	reqURL := fmt.Sprintf("https://%s%s", ServerlistHostname, serverListPath)

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create serverlist request: %w", err)
	}

	// Create a client that connects to the IP but uses hostname for SNI
	serverlistClient := newHostMappedClient(timeout, ServerlistHostname, ip)

	resp, err := serverlistClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// PIA appends a signature after the JSON object; trim it.
	if end := findJSONEnd(body); end > 0 {
		body = body[:end]
	}

	var result struct {
		Regions []region `json:"regions"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	flatRegions := flattenRegions(result.Regions)

	return flatRegions, nil
}

// flattenRegions extracts WireGuard servers from regions into a flat Server list.
func flattenRegions(regions []region) []Server {
	var servers []Server
	for _, r := range regions {
		for _, srv := range r.Servers["wg"] {
			servers = append(servers, Server{
				CN:         srv.CN,
				IP:         srv.IP,
				Region:     r.ID,
				RegionName: r.Name,
				PF:         r.PortForward,
			})
		}
	}
	return servers
}

// AddKey registers a WireGuard public key with PIA and returns tunnel parameters.
func AddKey(ctx context.Context, serverIP, cn, token, pubkey string) (*AddKeyResponse, error) {
	client, err := newAddKeyClient(serverIP, cn)
	if err != nil {
		return nil, fmt.Errorf("create addkey client: %w", err)
	}

	params := url.Values{}
	params.Set("pt", token)
	params.Set("pubkey", pubkey)
	reqURL := fmt.Sprintf("https://%s:%s/addKey?%s", cn, addKeyPort, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create addkey request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("token rejected (HTTP %d)", resp.StatusCode)
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var result AddKeyResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if result.Status != "OK" {
		return nil, fmt.Errorf("token rejected (status: %s)", result.Status)
	}

	return &result, nil
}

// GetCertPool returns *x509.CertPool for PIA's private cert
func GetCertPool() (*x509.CertPool, error) {
	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read PIA CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("parse PIA CA cert")
	}

	return pool, nil
}

// newAddKeyClient returns *http.Client with PIA's private CA
func newAddKeyClient(serverIP, cn string) (*http.Client, error) {
	pool, err := GetCertPool()
	if err != nil {
		return nil, err
	}

	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				ServerName: cn,
				RootCAs:    pool,
			},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(addr)
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(
					ctx, network, net.JoinHostPort(serverIP, port),
				)
			},
		},
	}, nil
}

func newHostMappedClient(timeout time.Duration, hostname, targetIP string) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				ServerName: hostname,
			},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(addr)
				return (&net.Dialer{Timeout: timeout}).DialContext(
					ctx, network, net.JoinHostPort(targetIP, port),
				)
			},
		},
	}
}

// findJSONEnd finds the closing brace of the top-level JSON object.
// PIA's server list response appends a signature after the JSON.
func findJSONEnd(data []byte) int {
	depth := 0
	inString := false
	escaped := false

	for i, b := range data {
		if escaped {
			escaped = false
			continue
		}
		if b == '\\' && inString {
			escaped = true
			continue
		}
		if b == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch b {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

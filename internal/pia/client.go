package pia

import (
	"bytes"
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
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/apperrors"
)

// PIA's private cert only used by AddKey and portforward package
// (getSignature and bindPort), all others use System CAs. The getSignature
// and bindPort functions are the only PIA interactions not listed here.

const (
	AuthHostname       = "www.privateinternetaccess.com"
	ServerlistHostname = "serverlist.piaservers.net"
	authPath           = "/api/client/v2/token"
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

	form := url.Values{}
	form.Set("username", user)
	form.Set("password", pass)

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

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

// ResolveDIP resolves a DIP token to its dedicated server details.
// authIP is a resolved IP for www.privateinternetaccess.com.
func ResolveDIP(ctx context.Context, timeout time.Duration, authIP, authToken, dipToken string) (*DIPInfo, error) {
	type requestBody struct {
		Tokens []string `json:"tokens"`
	}
	body, err := json.Marshal(requestBody{Tokens: []string{dipToken}})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	reqURL := fmt.Sprintf("https://%s/api/client/v2/dedicated_ip", AuthHostname)
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create dip request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token "+authToken)

	client := newHostMappedClient(timeout, AuthHostname, authIP)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: DIP auth failed (HTTP %d): %s", apperrors.ErrFatal, resp.StatusCode, string(respBody))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result []struct {
		Status string `json:"status"`
		IP     string `json:"ip"`
		CN     string `json:"cn"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	if result[0].Status != "active" {
		return nil, fmt.Errorf("%w: DIP token not active (status: %s)", apperrors.ErrFatal, result[0].Status)
	}

	return &DIPInfo{
		CN:     result[0].CN,
		IP:     result[0].IP,
		Region: result[0].ID,
	}, nil
}

// AddKeyDIP registers a WireGuard public key for a dedicated IP server.
// Authentication uses the DIP token via HTTP Basic Auth instead of the normal auth token.
func AddKeyDIP(ctx context.Context, serverIP, cn, dipToken, pubkey string) (*AddKeyResponse, error) {
	client, err := newAddKeyClient(serverIP, cn)
	if err != nil {
		return nil, fmt.Errorf("create addkey client: %w", err)
	}

	params := url.Values{}
	params.Set("pubkey", pubkey)
	reqURL := fmt.Sprintf("https://%s:%s/addKey?%s", cn, addKeyPort, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create addkey request: %w", err)
	}
	req.SetBasicAuth("dedicated_ip_"+dipToken, serverIP)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("DIP token rejected (HTTP %d)", resp.StatusCode)
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
		return nil, fmt.Errorf("DIP token rejected (status: %s)", result.Status)
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

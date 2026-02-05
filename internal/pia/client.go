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
)

const (
	authPath       = "/gtoken/generateToken"
	serverListPath = "/vpninfo/servers/v6"
	addKeyPort     = "1337"
	caCertPath     = "/app/ca.rsa.4096.crt"
)

// GenerateToken authenticates with PIA and returns a login token.
// host is the IP or hostname of the auth server.
// Returns *AuthError for invalid credentials, *ConnectivityError for network failures.
func GenerateToken(ctx context.Context, client *http.Client, host, user, pass string) (string, error) {
	reqURL := fmt.Sprintf("https://%s%s", host, authPath)

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("create auth request: %w", err)
	}
	req.SetBasicAuth(user, pass)

	resp, err := client.Do(req)
	if err != nil {
		return "", &ConnectivityError{Op: "auth", Msg: "request failed", Err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", &ConnectivityError{Op: "auth", Msg: "read response", Err: err}
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", &AuthError{Msg: "invalid credentials"}
	}
	if resp.StatusCode != http.StatusOK {
		return "", &ConnectivityError{
			Op:  "auth",
			Msg: fmt.Sprintf("HTTP %d", resp.StatusCode),
		}
	}

	var result struct {
		Token   string `json:"token"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", &ConnectivityError{Op: "auth", Msg: "parse response", Err: err}
	}

	// PIA can return 200 with an error message for some auth failures.
	if result.Message == "authentication failed" {
		return "", &AuthError{Msg: "invalid credentials"}
	}
	if result.Token == "" {
		return "", &ConnectivityError{Op: "auth", Msg: "empty token in response"}
	}

	return result.Token, nil
}

// FetchServerList fetches the PIA server list.
// host is the IP or hostname of the serverlist server.
// Returns *ConnectivityError for network failures.
func FetchServerList(ctx context.Context, client *http.Client, host string) ([]Region, error) {
	reqURL := fmt.Sprintf("https://%s%s", host, serverListPath)

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create serverlist request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, &ConnectivityError{Op: "serverlist", Msg: "request failed", Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &ConnectivityError{
			Op:  "serverlist",
			Msg: fmt.Sprintf("HTTP %d", resp.StatusCode),
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectivityError{Op: "serverlist", Msg: "read response", Err: err}
	}

	// PIA appends a signature after the JSON object; trim it.
	if end := findJSONEnd(body); end > 0 {
		body = body[:end]
	}

	var result struct {
		Regions []Region `json:"regions"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, &ConnectivityError{Op: "serverlist", Msg: "parse response", Err: err}
	}

	return result.Regions, nil
}

// AddKey registers a WireGuard public key with PIA and returns tunnel parameters.
// serverIP is the actual server IP to connect to, cn is the server's certificate
// name used for both TLS verification and the URL hostname. The request is verified
// against PIA's CA certificate.
// Returns *AuthError if the token is rejected (status != "OK").
// Returns *ConnectivityError for network failures.
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
		return nil, &ConnectivityError{Op: "addkey", Msg: "request failed", Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &ConnectivityError{
			Op:  "addkey",
			Msg: fmt.Sprintf("HTTP %d", resp.StatusCode),
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectivityError{Op: "addkey", Msg: "read response", Err: err}
	}

	var result AddKeyResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, &ConnectivityError{Op: "addkey", Msg: "parse response", Err: err}
	}

	if result.Status != "OK" {
		return nil, &AuthError{Msg: fmt.Sprintf("addKey rejected (status: %s)", result.Status)}
	}

	return &result, nil
}

// newAddKeyClient creates an http.Client for the addKey API. It verifies TLS
// against PIA's CA certificate with ServerName set to the server's CN, and
// dials serverIP regardless of the URL hostname (equivalent to curl --resolve).
func newAddKeyClient(serverIP, cn string) (*http.Client, error) {
	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read PIA CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("parse PIA CA cert")
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
			TLSHandshakeTimeout: 5 * time.Second,
		},
	}, nil
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

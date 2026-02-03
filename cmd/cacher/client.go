package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	piaAuthHost       = "www.privateinternetaccess.com"
	piaAuthURL        = "https://www.privateinternetaccess.com/gtoken/generateToken"
	piaServerlistHost = "serverlist.piaservers.net"
	piaServerlistURL  = "https://serverlist.piaservers.net/vpninfo/servers/v6"
)

type PIAClient struct {
	config     *Config
	httpClient *http.Client
}

type TokenResponse struct {
	Token string `json:"token"`
}

type ServerListResponse struct {
	Regions []Region `json:"regions"`
}

type Region struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	PortForward bool              `json:"port_forward"`
	Servers     map[string][]Server `json:"servers"`
}

type Server struct {
	IP string `json:"ip"`
	CN string `json:"cn"`
}

// CachedServer is our normalized server format for the cache
type CachedServer struct {
	CN     string `json:"cn"`
	IP     string `json:"ip"`
	Region string `json:"region"`
	PF     bool   `json:"pf"`
}

func NewPIAClient(config *Config) *PIAClient {
	// Create custom transport bound to pia0 interface
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Get the pia0 interface
			iface, err := net.InterfaceByName("pia0")
			if err != nil {
				return nil, fmt.Errorf("failed to get pia0 interface: %w", err)
			}

			// Get addresses for the interface
			addrs, err := iface.Addrs()
			if err != nil {
				return nil, fmt.Errorf("failed to get interface addresses: %w", err)
			}

			if len(addrs) == 0 {
				return nil, fmt.Errorf("no addresses on pia0 interface")
			}

			// Use the first address
			ipNet, ok := addrs[0].(*net.IPNet)
			if !ok {
				return nil, fmt.Errorf("invalid address type")
			}

			// Create TCP address with the interface IP
			localAddr := &net.TCPAddr{
				IP: ipNet.IP,
			}

			// Dial with local address binding
			d := &net.Dialer{
				LocalAddr: localAddr,
				Timeout:   15 * time.Second,
				KeepAlive: 30 * time.Second,
			}

			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // PIA uses self-signed certs
		},
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	return &PIAClient{
		config:     config,
		httpClient: client,
	}
}

// GetToken fetches a new login token from PIA
// Returns the token and the resolved IPs for the auth server
func (c *PIAClient) GetToken(ctx context.Context) (string, []string, error) {
	debugLog(c.config, "Resolving %s", piaAuthHost)

	// Resolve hostname to get IPs for caching
	ips, err := net.DefaultResolver.LookupHost(ctx, piaAuthHost)
	if err != nil {
		return "", nil, fmt.Errorf("failed to resolve %s: %w", piaAuthHost, err)
	}
	debugLog(c.config, "Resolved %s to %v", piaAuthHost, ips)

	// Create request with basic auth
	req, err := http.NewRequestWithContext(ctx, "GET", piaAuthURL, nil)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.SetBasicAuth(strings.TrimSpace(c.config.PIAUser), strings.TrimSpace(c.config.PIAPass))

	debugLog(c.config, "Requesting token from %s", piaAuthURL)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", nil, fmt.Errorf("authentication failed: invalid credentials")
	}
	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read response: %w", err)
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if tokenResp.Token == "" {
		return "", nil, fmt.Errorf("empty token in response")
	}

	debugLog(c.config, "Token received (length: %d)", len(tokenResp.Token))
	return tokenResp.Token, ips, nil
}

// GetServerList fetches the server list from PIA
// Returns parsed servers and the resolved IPs for the serverlist host
func (c *PIAClient) GetServerList(ctx context.Context) ([]CachedServer, []string, error) {
	debugLog(c.config, "Resolving %s", piaServerlistHost)

	// Resolve hostname to get IPs for caching
	ips, err := net.DefaultResolver.LookupHost(ctx, piaServerlistHost)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve %s: %w", piaServerlistHost, err)
	}
	debugLog(c.config, "Resolved %s to %v", piaServerlistHost, ips)

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", piaServerlistURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	debugLog(c.config, "Fetching server list from %s", piaServerlistURL)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response: %w", err)
	}

	debugLog(c.config, "Server list response size: %d bytes", len(body))

	// The response has some extra data after the JSON, so we need to find the end
	// It's typically: {"groups":...,"regions":...}\nSOME_SIGNATURE
	jsonEnd := findJSONEnd(body)
	if jsonEnd > 0 {
		body = body[:jsonEnd]
	}

	var serverList ServerListResponse
	if err := json.Unmarshal(body, &serverList); err != nil {
		return nil, nil, fmt.Errorf("failed to parse server list: %w", err)
	}

	// Extract WireGuard-capable servers
	var servers []CachedServer
	for _, region := range serverList.Regions {
		wgServers, ok := region.Servers["wg"]
		if !ok {
			continue
		}

		for _, srv := range wgServers {
			servers = append(servers, CachedServer{
				CN:     srv.CN,
				IP:     srv.IP,
				Region: region.ID,
				PF:     region.PortForward,
			})
		}
	}

	debugLog(c.config, "Parsed %d WireGuard servers from %d regions", len(servers), len(serverList.Regions))
	return servers, ips, nil
}

// findJSONEnd finds the end of a JSON object in the byte slice
// PIA's response includes a signature after the JSON
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

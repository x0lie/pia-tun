package cacher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/pia"
)

const (
	piaAuthHost       = "www.privateinternetaccess.com"
	piaAuthURL        = "https://www.privateinternetaccess.com/gtoken/generateToken"
	piaServerlistHost = "serverlist.piaservers.net"
	piaServerlistURL  = "https://serverlist.piaservers.net/vpninfo/servers/v6"
)

// PIAClient handles PIA API requests for token and server list.
type PIAClient struct {
	config     *Config
	log        *log.Logger
	httpClient *http.Client
}

// TokenResponse is the PIA token API response.
type TokenResponse struct {
	Token string `json:"token"`
}

// ServerListResponse is the PIA server list API response.
type ServerListResponse struct {
	Regions []Region `json:"regions"`
}

// Region represents a PIA server region.
type Region struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	PortForward bool                `json:"port_forward"`
	Servers     map[string][]Server `json:"servers"`
}

// Server represents a single PIA server.
type Server struct {
	IP string `json:"ip"`
	CN string `json:"cn"`
}

// CachedServer is the normalized server format for the cache.
type CachedServer struct {
	CN     string `json:"cn"`
	IP     string `json:"ip"`
	Region string `json:"region"`
	PF     bool   `json:"pf"`
}

// NewPIAClient creates a new PIA API client bound to the pia0 interface.
func NewPIAClient(config *Config, logger *log.Logger) *PIAClient {
	return &PIAClient{
		config:     config,
		log:        logger,
		httpClient: pia.NewBoundClient(15*time.Second, 30*time.Second),
	}
}

// GetToken fetches a new login token from PIA.
// Returns the token and the resolved IPs for the auth server.
func (c *PIAClient) GetToken(ctx context.Context) (string, []string, error) {
	c.log.Debug("Resolving %s", piaAuthHost)

	ips, err := net.DefaultResolver.LookupHost(ctx, piaAuthHost)
	if err != nil {
		return "", nil, fmt.Errorf("failed to resolve %s: %w", piaAuthHost, err)
	}
	c.log.Debug("Resolved %s to %v", piaAuthHost, ips)

	req, err := http.NewRequestWithContext(ctx, "GET", piaAuthURL, nil)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.SetBasicAuth(strings.TrimSpace(c.config.PIAUser), strings.TrimSpace(c.config.PIAPass))

	c.log.Debug("Requesting token from %s", piaAuthURL)

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

	c.log.Debug("Token received (length: %d)", len(tokenResp.Token))
	return tokenResp.Token, ips, nil
}

// GetServerList fetches the server list from PIA.
// Returns parsed servers and the resolved IPs for the serverlist host.
func (c *PIAClient) GetServerList(ctx context.Context) ([]CachedServer, []string, error) {
	c.log.Debug("Resolving %s", piaServerlistHost)

	ips, err := net.DefaultResolver.LookupHost(ctx, piaServerlistHost)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve %s: %w", piaServerlistHost, err)
	}
	c.log.Debug("Resolved %s to %v", piaServerlistHost, ips)

	req, err := http.NewRequestWithContext(ctx, "GET", piaServerlistURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	c.log.Debug("Fetching server list from %s", piaServerlistURL)

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

	c.log.Debug("Server list response size: %d bytes", len(body))

	jsonEnd := findJSONEnd(body)
	if jsonEnd > 0 {
		body = body[:jsonEnd]
	}

	var serverList ServerListResponse
	if err := json.Unmarshal(body, &serverList); err != nil {
		return nil, nil, fmt.Errorf("failed to parse server list: %w", err)
	}

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

	c.log.Debug("Parsed %d WireGuard servers from %d regions", len(servers), len(serverList.Regions))
	return servers, ips, nil
}

// findJSONEnd finds the end of a JSON object in the byte slice.
// PIA's response includes a signature after the JSON.
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

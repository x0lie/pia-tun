package vpn

import (
	"time"

	"github.com/x0lie/pia-tun/internal/pia"
)

// ConnectionInfo holds the result of a successful VPN connection.
// Populated by Setup() and consumed by monitor (metrics), port forwarding,
// and the orchestrator.
type ConnectionInfo struct {
	Token        string
	ServerIP     string
	ServerCN     string
	ClientIP     string
	PFGateway    string
	Location     string
	LocationName string
	Latency      time.Duration
	WGMode       string
}

// CacheState holds cached PIA API data that persists across reconnections.
// Pre-populated by the cacher goroutine so VPN setup can skip DNS resolution
// and authenticate using cached IPs and tokens.
type CacheState struct {
	Token         string
	TokenTime     time.Time
	AuthIPs       []string
	ServerListIPs []string
	Servers       []pia.CachedServer
}

// TokenFresh reports whether the cached token is non-empty and younger than maxAge.
func (c *CacheState) TokenFresh(maxAge time.Duration) bool {
	return c.Token != "" && time.Since(c.TokenTime) < maxAge
}

// SetToken updates the cached token and records the current time.
func (c *CacheState) SetToken(token string) {
	c.Token = token
	c.TokenTime = time.Now()
}

// MergeIPs adds new IPs to front of existing, deduplicates, and caps at max.
func (c *CacheState) MergeIPs(field *[]string, newIPs []string, max int) {
	seen := make(map[string]bool)
	var merged []string
	for _, ip := range newIPs {
		if !seen[ip] {
			seen[ip] = true
			merged = append(merged, ip)
		}
	}
	for _, ip := range *field {
		if !seen[ip] {
			seen[ip] = true
			merged = append(merged, ip)
		}
	}
	if len(merged) > max {
		merged = merged[:max]
	}
	*field = merged
}

// MergeServers merges new servers into cache by CN (new servers take priority).
func (c *CacheState) MergeServers(newServers []pia.CachedServer) {
	byCN := make(map[string]int)
	for i := range c.Servers {
		byCN[c.Servers[i].CN] = i
	}
	for _, srv := range newServers {
		if idx, ok := byCN[srv.CN]; ok {
			c.Servers[idx] = srv
		} else {
			byCN[srv.CN] = len(c.Servers)
			c.Servers = append(c.Servers, srv)
		}
	}
}

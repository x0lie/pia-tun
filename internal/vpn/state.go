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

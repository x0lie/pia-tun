package cacher

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/pia"
)

const (
	piaAuthHost       = "www.privateinternetaccess.com"
	piaServerlistHost = "serverlist.piaservers.net"
	maxCachedIPs      = 5
	maxTokenAge       = 23 * time.Hour
)

type config struct {
	piaUser         string
	piaPass         string
	refreshInterval time.Duration
}

// Cache holds cached PIA API data that persists across reconnections.
// Pre-populated by the cacher goroutine so VPN setup can skip DNS resolution
// and authenticate using cached IPs and tokens.
type Cache struct {
	mu sync.RWMutex

	Token         string
	TokenTime     time.Time
	AuthIPs       []string
	ServerListIPs []string
	Servers       []pia.CachedServer
}

func refreshAll(ctx context.Context, logger *log.Logger, cfg *config, client *http.Client, c *Cache) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	var lastErr error

	// 1. Refresh login token
	logger.Trace("Refreshing login token...")
	authIPs, err := resolveIPv4(ctx, piaAuthHost)
	if err != nil {
		logger.Debug("Failed to resolve %s: %v", piaAuthHost, err)
		lastErr = err
	} else {
		logger.Trace("Resolved %s to %v", piaAuthHost, authIPs)
		c.MergeAuthIPs(authIPs)

		token, err := pia.GenerateToken(ctx, client, piaAuthHost, cfg.piaUser, cfg.piaPass)
		if err != nil {
			logger.Debug("Token refresh failed: %v", err)
			lastErr = err
		} else {
			c.SetToken(token)
			logger.Trace("Token refreshed (length: %d)", len(token))
		}
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 2. Refresh server list
	logger.Trace("Refreshing server list...")
	slIPs, err := resolveIPv4(ctx, piaServerlistHost)
	if err != nil {
		logger.Debug("Failed to resolve %s: %v", piaServerlistHost, err)
		lastErr = err
	} else {
		logger.Trace("Resolved %s to %v", piaServerlistHost, slIPs)
		c.MergeServerListIPs(slIPs)

		regions, err := pia.FetchServerList(ctx, client, piaServerlistHost)
		if err != nil {
			logger.Debug("Server list refresh failed: %v", err)
			lastErr = err
		} else {
			servers := pia.FlattenRegions(regions)
			c.MergeServers(servers)
			logger.Trace("Cache updated with %d servers", len(c.Servers))
		}
	}

	return lastErr
}

func Run(ctx context.Context, cache *Cache, piaUser string, piaPass string) error {
	if cache == nil {
		cache = &Cache{}
	}

	cfg := &config{
		piaUser:         piaUser,
		piaPass:         piaPass,
		refreshInterval: 6 * time.Hour,
	}

	logger := log.New("cacher")

	logger.Debug("Starting with refresh interval: %v", cfg.refreshInterval)

	client := pia.NewBoundClient(15*time.Second, 30*time.Second)

	// Initial refresh on startup
	logger.Debug("Performing initial cache refresh")
	if err := refreshAll(ctx, logger, cfg, client, cache); err != nil {
		logger.Debug(fmt.Sprintf("Initial cache refresh failed: %v", err))
	} else {
		logger.Debug("Cache initialized")
	}

	// Periodic refresh
	ticker := time.NewTicker(cfg.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Debug("Received shutdown signal")
			return ctx.Err()

		case <-ticker.C:
			logger.Debug("Starting scheduled refresh")
			var err error
			for attempt := range 3 {
				if err = refreshAll(ctx, logger, cfg, client, cache); err == nil {
					break
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if attempt < 2 {
					logger.Debug("Cache refresh failed, retrying: %v", err)
					time.Sleep(60 * time.Second)
				}
			}
			if err != nil {
				log.Warning(fmt.Sprintf("Cache refresh failed after 3 attempts: %v", err))
			} else {
				logger.Trace("Cache refreshed")
			}
		}
	}
}

// resolveIPv4 resolves a hostname to IPv4 addresses using the system resolver.
func resolveIPv4(ctx context.Context, hostname string) ([]string, error) {
	addrs, err := net.DefaultResolver.LookupIP(ctx, "ip4", hostname)
	if err != nil {
		return nil, err
	}
	ips := make([]string, len(addrs))
	for i, addr := range addrs {
		ips[i] = addr.String()
	}
	return ips, nil
}

func (c *Cache) MergeAuthIPs(newIPs []string) {
	c.AuthIPs = mergeIPs(c.AuthIPs, newIPs, maxCachedIPs)
}

func (c *Cache) MergeServerListIPs(newIPs []string) {
	c.ServerListIPs = mergeIPs(c.ServerListIPs, newIPs, maxCachedIPs)
}

// mergeIPs adds new IPs to front of existing, deduplicates, and caps at max.
func mergeIPs(existing, newIPs []string, max int) []string {
	seen := make(map[string]bool, len(existing)+len(newIPs))
	var merged []string
	for _, ip := range newIPs {
		if !seen[ip] {
			seen[ip] = true
			merged = append(merged, ip)
		}
	}
	for _, ip := range existing {
		if !seen[ip] {
			seen[ip] = true
			merged = append(merged, ip)
		}
	}
	if len(merged) > max {
		merged = merged[:max]
	}
	return merged
}

// mergeServers merges new servers into existing by CN, updating IP/PF/Region for
// known servers and appending new ones.
func (c *Cache) MergeServers(newServers []pia.CachedServer) {
	byCN := make(map[string]int, len(c.Servers))
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

func (c *Cache) GetToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Token
}

func (c *Cache) TokenFresh() (bool, time.Duration) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	age := time.Since(c.TokenTime)
	return c.Token != "" && age < maxTokenAge, age
}

func (c *Cache) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Token = token
	c.TokenTime = time.Now()
}

func (c *Cache) ClearToken() {
	c.Token = ""
	c.TokenTime = time.Time{}
}

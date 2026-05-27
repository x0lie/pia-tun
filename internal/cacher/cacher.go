package cacher

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/pia"
)

const (
	maxCachedIPs = 5
	maxTokenAge  = 23 * time.Hour
	tokenFile    = "/run/pia-tun/token"
	timeout      = 15 * time.Second
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
	Servers       []pia.Server
}

// New creates and returns a new Cache instance, loads persistent auth token into instance
func New() *Cache {
	token, timestamp, err := readTokenFile()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Warning("Failed to load persisted token: %v", err)
		}
		return &Cache{}
	}
	if time.Since(timestamp) > maxTokenAge {
		os.Remove(tokenFile)
		return &Cache{}
	}
	return &Cache{
		Token:     token,
		TokenTime: timestamp,
	}
}

func refreshAll(ctx context.Context, logger *log.Logger, cfg *config, c *Cache) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	var lastErr error

	// 1. Refresh login token
	logger.Trace("Refreshing login token...")
	authIPs, err := resolveIPv4(ctx, pia.AuthHostname)
	if err != nil {
		logger.Debug("Failed to resolve %s: %v", pia.AuthHostname, err)
		lastErr = err
	} else {
		logger.Trace("Resolved %s to %v", pia.AuthHostname, authIPs)
		c.MergeAuthIPs(authIPs)

		token, err := pia.GenerateToken(ctx, timeout, pia.AuthHostname, cfg.piaUser, cfg.piaPass)
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
	slIPs, err := resolveIPv4(ctx, pia.ServerlistHostname)
	if err != nil {
		logger.Debug("Failed to resolve %s: %v", pia.ServerlistHostname, err)
		lastErr = err
	} else {
		logger.Trace("Resolved %s to %v", pia.ServerlistHostname, slIPs)
		c.MergeServerListIPs(slIPs)

		servers, err := pia.FetchServerList(ctx, timeout, pia.ServerlistHostname)
		if err != nil {
			logger.Debug("Server list refresh failed: %v", err)
			lastErr = err
		} else {
			c.MergeServers(servers)
			logger.Trace("Cache updated with %d servers", len(c.Servers))
		}
	}

	return lastErr
}

// Run starts the cacher process
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

	ticker := time.NewTicker(cfg.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Debug("Received shutdown signal")
			return ctx.Err()

		case <-ticker.C:
			logger.Trace("Starting scheduled refresh")
			var err error
			for attempt := range 3 {
				if err = refreshAll(ctx, logger, cfg, cache); err == nil {
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
				log.Warning("Cache refresh failed after 3 attempts: %v", err)
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

// MergeAuthIPs merges input IPs into existing Cache.AuthIPs
func (c *Cache) MergeAuthIPs(newIPs []string) {
	c.AuthIPs = mergeIPs(c.AuthIPs, newIPs, maxCachedIPs)
}

// MergeServerListIPs merges input IPs into existing Cache.ServerListIPs
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
func (c *Cache) MergeServers(newServers []pia.Server) {
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

// GetToken returns the current auth token
func (c *Cache) GetToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Token
}

// TokenFresh returns true if token is < 23 hours old
func (c *Cache) TokenFresh() (bool, time.Duration) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	age := time.Since(c.TokenTime)
	return c.Token != "" && age < maxTokenAge, age
}

// SetToken writes the input token and current time to /run/pia-tun/token, and sets the values in Cache.Token and Cache.TokenTime
func (c *Cache) SetToken(token string) {
	content := token + "\n" + strconv.FormatInt(time.Now().Unix(), 10) + "\n"
	if err := os.WriteFile(tokenFile, []byte(content), 0600); err != nil {
		log.Warning("Failed to persist token: %v", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.Token = token
	c.TokenTime = time.Now()
}

// ClearToken removes /run/pia-tun/token, and clears Cache.Token and Cache.TokenTime
func (c *Cache) ClearToken() {
	os.Remove(tokenFile)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.Token = ""
	c.TokenTime = time.Time{}
}

// ClearServerListIPs removes all cached ServerListIPs
func (c *Cache) ClearServerListIPs() {
	c.ServerListIPs = nil
}

func readTokenFile() (string, time.Time, error) {
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", time.Time{}, err
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) != 2 {
		return "", time.Time{}, fmt.Errorf("malformed token file")
	}
	unixSec, err := strconv.ParseInt(lines[1], 10, 64)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("invalid timestamp: %w", err)
	}
	return lines[0], time.Unix(unixSec, 0), nil
}

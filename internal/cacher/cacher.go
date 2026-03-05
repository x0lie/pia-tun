package cacher

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/pia"
	"github.com/x0lie/pia-tun/internal/vpn"
)

const (
	piaAuthHost       = "www.privateinternetaccess.com"
	piaServerlistHost = "serverlist.piaservers.net"
	maxCachedIPs      = 5
)

// cacherConfig holds cacher configuration.
type cacherConfig struct {
	piaUser         string
	piaPass         string
	refreshInterval time.Duration
}

func refreshAll(ctx context.Context, logger *log.Logger, cfg *cacherConfig, client *http.Client, cache *vpn.CacheState) error {
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
		cache.AuthIPs = mergeIPs(cache.AuthIPs, authIPs, maxCachedIPs)

		token, err := pia.GenerateToken(ctx, client, piaAuthHost, cfg.piaUser, cfg.piaPass)
		if err != nil {
			logger.Debug("Token refresh failed: %v", err)
			lastErr = err
		} else {
			cache.SetToken(token)
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
		cache.ServerListIPs = mergeIPs(cache.ServerListIPs, slIPs, maxCachedIPs)

		regions, err := pia.FetchServerList(ctx, client, piaServerlistHost)
		if err != nil {
			logger.Debug("Server list refresh failed: %v", err)
			lastErr = err
		} else {
			servers := pia.FlattenRegions(regions)
			cache.Servers = mergeServers(cache.Servers, servers)
			logger.Trace("Server cache updated with %d servers", len(cache.Servers))
		}
	}

	return lastErr
}

// Run starts the cacher service. cache may be nil for standalone mode,
// in which case a local CacheState is used.
func Run(ctx context.Context, cache *vpn.CacheState, piaUser string, piaPass string) error {
	if cache == nil {
		cache = &vpn.CacheState{}
	}

	cfg := &cacherConfig{
		piaUser:         piaUser,
		piaPass:         piaPass,
		refreshInterval: 6 * time.Hour,
	}

	logger := log.New("cacher")

	logger.Debug("Cacher starting with refresh interval: %v", cfg.refreshInterval)

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
			logger.Debug("Starting scheduled cache refresh")
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
func mergeServers(existing, newServers []pia.CachedServer) []pia.CachedServer {
	byCN := make(map[string]int, len(existing))
	for i := range existing {
		byCN[existing[i].CN] = i
	}
	for _, srv := range newServers {
		if idx, ok := byCN[srv.CN]; ok {
			existing[idx] = srv
		} else {
			byCN[srv.CN] = len(existing)
			existing = append(existing, srv)
		}
	}
	return existing
}

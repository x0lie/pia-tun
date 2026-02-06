package cacher

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/config"
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
	debugMode       bool
}

func loadConfig() (*cacherConfig, error) {
	piaUser := strings.TrimSpace(config.GetSecret("PIA_USER", "/run/secrets/pia_user"))
	piaPass := strings.TrimSpace(config.GetSecret("PIA_PASS", "/run/secrets/pia_pass"))

	if piaUser == "" || piaPass == "" {
		return nil, fmt.Errorf("PIA credentials not found (set PIA_USER/PIA_PASS or use Docker secrets)")
	}

	refreshHours := config.GetEnvInt("CACHE_REFRESH_HOURS", 6)
	if refreshHours <= 0 {
		refreshHours = 6
	}

	return &cacherConfig{
		piaUser:         piaUser,
		piaPass:         piaPass,
		refreshInterval: time.Duration(refreshHours) * time.Hour,
		debugMode:       config.IsDebugMode(),
	}, nil
}

func refreshAll(ctx context.Context, logger *log.Logger, cfg *cacherConfig, client *http.Client, cache *vpn.CacheState) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	var lastErr error

	// 1. Refresh login token
	logger.Debug("Refreshing login token...")
	authIPs, err := resolveIPv4(ctx, piaAuthHost)
	if err != nil {
		logger.Debug("Failed to resolve %s: %v", piaAuthHost, err)
		lastErr = err
	} else {
		logger.Debug("Resolved %s to %v", piaAuthHost, authIPs)
		cache.AuthIPs = mergeIPs(cache.AuthIPs, authIPs, maxCachedIPs)

		token, err := pia.GenerateToken(ctx, client, piaAuthHost, cfg.piaUser, cfg.piaPass)
		if err != nil {
			logger.Debug("Token refresh failed: %v", err)
			lastErr = err
		} else {
			cache.SetToken(token)
			logger.Debug("Token refreshed (length: %d)", len(token))
		}
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 2. Refresh server list
	logger.Debug("Refreshing server list...")
	slIPs, err := resolveIPv4(ctx, piaServerlistHost)
	if err != nil {
		logger.Debug("Failed to resolve %s: %v", piaServerlistHost, err)
		lastErr = err
	} else {
		logger.Debug("Resolved %s to %v", piaServerlistHost, slIPs)
		cache.ServerListIPs = mergeIPs(cache.ServerListIPs, slIPs, maxCachedIPs)

		regions, err := pia.FetchServerList(ctx, client, piaServerlistHost)
		if err != nil {
			logger.Debug("Server list refresh failed: %v", err)
			lastErr = err
		} else {
			servers := pia.FlattenRegions(regions)
			cache.Servers = mergeServers(cache.Servers, servers)
			logger.Debug("Server cache updated with %d servers", len(cache.Servers))
		}
	}

	return lastErr
}

// Run starts the cacher service. cache may be nil for standalone mode,
// in which case a local CacheState is used.
func Run(ctx context.Context, cache *vpn.CacheState) error {
	if cache == nil {
		cache = &vpn.CacheState{}
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Error(fmt.Sprintf("Cacher failed to start: %v", err))
		return err
	}

	logger := &log.Logger{
		Enabled: cfg.debugMode,
		Prefix:  "cacher",
	}

	logger.Debug("Cacher starting with refresh interval: %v", cfg.refreshInterval)

	client := pia.NewBoundClient(15*time.Second, 30*time.Second)

	// Initial refresh on startup
	logger.Debug("Performing initial cache refresh")
	if err := refreshAll(ctx, logger, cfg, client, cache); err != nil {
		log.Warning(fmt.Sprintf("Initial cache refresh failed: %v", err))
	} else {
		logger.Debug("Cache initialized")
	}

	// Periodic refresh
	ticker := time.NewTicker(cfg.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Debug("Cacher received shutdown signal")
			return ctx.Err()

		case <-ticker.C:
			logger.Debug("Starting scheduled cache refresh")
			if err := refreshAll(ctx, logger, cfg, client, cache); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				log.Warning(fmt.Sprintf("Cache refresh failed: %v", err))
			} else {
				logger.Debug("Cache refreshed")
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

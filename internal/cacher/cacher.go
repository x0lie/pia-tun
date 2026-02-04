package cacher

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/x0lie/pia-tun/internal/config"
	"github.com/x0lie/pia-tun/internal/log"
)

// Config holds cacher configuration.
type Config struct {
	PIAUser         string
	PIAPass         string
	RefreshInterval time.Duration
	DebugMode       bool
}

func loadConfig() (*Config, error) {
	var piaUser, piaPass string

	piaUser = config.GetSecret("PIA_USER", "/run/secrets/pia_user")
	piaPass = config.GetSecret("PIA_PASS", "/run/secrets/pia_pass")

	if piaUser == "" || piaPass == "" {
		return nil, fmt.Errorf("PIA credentials not found (set PIA_USER/PIA_PASS or use Docker secrets)")
	}

	refreshHours := config.GetEnvInt("CACHE_REFRESH_HOURS", 6)
	if refreshHours <= 0 {
		refreshHours = 6
	}

	return &Config{
		PIAUser:         piaUser,
		PIAPass:         piaPass,
		RefreshInterval: time.Duration(refreshHours) * time.Hour,
		DebugMode:       config.IsDebugMode(),
	}, nil
}

// isIPv4 checks if an IP string is IPv4.
func isIPv4(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return parsed.To4() != nil
}

// filterIPv4Only returns only IPv4 addresses from the slice.
func filterIPv4Only(ips []string) []string {
	var ipv4s []string
	for _, ip := range ips {
		if isIPv4(ip) {
			ipv4s = append(ipv4s, ip)
		}
	}
	return ipv4s
}

func refreshAll(ctx context.Context, logger *log.Logger, client *PIAClient) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	var lastErr error

	// 1. Refresh login token
	logger.Debug("Refreshing login token...")
	token, piaIPs, err := client.GetToken(ctx)
	if err != nil {
		logger.Debug("Token refresh failed: %v", err)
		lastErr = err
	} else {
		if err := WriteToken(token); err != nil {
			logger.Debug("Failed to write token: %v", err)
			lastErr = err
		} else {
			logger.Debug("Token saved (length: %d)", len(token))
		}

		piaIPv4s := filterIPv4Only(piaIPs)
		logger.Debug("Filtered PIA IPs: %d total, %d IPv4", len(piaIPs), len(piaIPv4s))

		for _, ip := range piaIPv4s {
			if err := AddIPToCache(PIAIPsFile, ip, MaxCachedIPs); err != nil {
				logger.Debug("Failed to cache PIA IP %s: %v", ip, err)
			}
		}
		logger.Debug("Cached %d PIA IPv4 address(es)", len(piaIPv4s))
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 2. Refresh server list
	logger.Debug("Refreshing server list...")
	servers, serverlistIPs, err := client.GetServerList(ctx)
	if err != nil {
		logger.Debug("Server list refresh failed: %v", err)
		lastErr = err
	} else {
		serverlistIPv4s := filterIPv4Only(serverlistIPs)
		logger.Debug("Filtered serverlist IPs: %d total, %d IPv4", len(serverlistIPs), len(serverlistIPv4s))

		for _, ip := range serverlistIPv4s {
			if err := AddIPToCache(ServerlistIPsFile, ip, MaxCachedIPs); err != nil {
				logger.Debug("Failed to cache serverlist IP %s: %v", ip, err)
			}
		}
		logger.Debug("Cached %d serverlist IPv4 address(es)", len(serverlistIPv4s))

		if err := MergeAndSaveServers(servers); err != nil {
			logger.Debug("Failed to save server cache: %v", err)
			lastErr = err
		} else {
			logger.Debug("Server cache updated with %d servers", len(servers))
		}
	}

	return lastErr
}

// Run starts the cacher service. This is the main entry point called by the dispatcher.
func Run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		log.Error(fmt.Sprintf("Cacher failed to start: %v", err))
		return err
	}

	logger := &log.Logger{
		Enabled: cfg.DebugMode,
		Prefix:  "cacher",
	}

	logger.Debug("Cacher starting with refresh interval: %v", cfg.RefreshInterval)

	client := NewPIAClient(cfg, logger)

	// Initial refresh on startup
	logger.Debug("Performing initial cache refresh")
	if err := refreshAll(ctx, logger, client); err != nil {
		log.Warning(fmt.Sprintf("Initial cache refresh failed: %v", err))
	} else {
		logger.Debug("Cache initialized")
	}

	// Periodic refresh
	ticker := time.NewTicker(cfg.RefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Debug("Cacher received shutdown signal")
			return ctx.Err()

		case <-ticker.C:
			logger.Debug("Starting scheduled cache refresh")
			if err := refreshAll(ctx, logger, client); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				log.Warning(fmt.Sprintf("Cache refresh failed: %v", err))
			} else {
				timestamp := time.Now().Format("2006-01-02 15:04:05")
				logger.Debug("[%s] Cache refreshed", timestamp)
			}
		}
	}
}

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type Config struct {
	PIAUser         string
	PIAPass         string
	RefreshInterval time.Duration
	DebugMode       bool
}

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorBlue   = "\033[0;34m"
	colorYellow = "\033[0;33m"
)

func loadConfig() (*Config, error) {
	var piaUser, piaPass string

	// Check for Docker secrets first
	if data, err := os.ReadFile("/run/secrets/pia_user"); err == nil {
		piaUser = string(data)
	} else if val := os.Getenv("PIA_USER"); val != "" {
		piaUser = val
	}

	if data, err := os.ReadFile("/run/secrets/pia_pass"); err == nil {
		piaPass = string(data)
	} else if val := os.Getenv("PIA_PASS"); val != "" {
		piaPass = val
	}

	if piaUser == "" || piaPass == "" {
		return nil, fmt.Errorf("PIA credentials not found (set PIA_USER/PIA_PASS or use Docker secrets)")
	}

	// Parse refresh interval (default 6 hours)
	refreshHours := 6
	if val := os.Getenv("CACHE_REFRESH_HOURS"); val != "" {
		if i, err := strconv.Atoi(val); err == nil && i > 0 {
			refreshHours = i
		}
	}

	// Debug mode
	debugMode := os.Getenv("_LOG_LEVEL") == "debug" || os.Getenv("_LOG_LEVEL") == "2"

	return &Config{
		PIAUser:         piaUser,
		PIAPass:         piaPass,
		RefreshInterval: time.Duration(refreshHours) * time.Hour,
		DebugMode:       debugMode,
	}, nil
}

func debugLog(config *Config, format string, args ...interface{}) {
	if config.DebugMode {
		timestamp := time.Now().Format("15:04:05")
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintf(os.Stderr, "    %s[DEBUG]%s %s - cacher: %s\n", colorBlue, colorReset, timestamp, msg)
	}
}

func showInfo(msg string) {
	fmt.Printf("  %s○%s %s\n", colorBlue, colorReset, msg)
}

func showSuccess(msg string) {
	fmt.Printf("  %s✓%s %s\n", colorGreen, colorReset, msg)
}

func showError(msg string) {
	fmt.Printf("  %s✗%s %s\n", colorRed, colorReset, msg)
}

func showWarning(msg string) {
	fmt.Printf("  %s⚠%s %s\n", colorYellow, colorReset, msg)
}

// isIPv4 checks if an IP string is IPv4 (not IPv6)
// IPv6 addresses contain colons, IPv4 addresses don't
func isIPv4(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return parsed.To4() != nil
}

// filterIPv4Only returns only IPv4 addresses from the slice
func filterIPv4Only(ips []string) []string {
	var ipv4s []string
	for _, ip := range ips {
		if isIPv4(ip) {
			ipv4s = append(ipv4s, ip)
		}
	}
	return ipv4s
}

func main() {
	config, err := loadConfig()
	if err != nil {
		showError(fmt.Sprintf("Cacher failed to start: %v", err))
		os.Exit(1)
	}

	debugLog(config, "Cacher starting with refresh interval: %v", config.RefreshInterval)

	// Create PIA client
	client := NewPIAClient(config)

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)

	go func() {
		sig := <-sigChan
		debugLog(config, "Received signal: %v", sig)
		cancel()
	}()

	// Run the cacher
	if err := run(ctx, config, client); err != nil {
		if err != context.Canceled {
			showError(fmt.Sprintf("Cacher error: %v", err))
			os.Exit(1)
		}
	}

	debugLog(config, "Cacher shutdown complete")
}

func run(ctx context.Context, config *Config, client *PIAClient) error {
	// Initial refresh on startup
	debugLog(config, "Performing initial cache refresh")
	if err := refreshAll(ctx, config, client); err != nil {
		showWarning(fmt.Sprintf("Initial cache refresh failed: %v", err))
		// Don't exit - continue and retry on next tick
	} else {
		debugLog(config, "Cache initialized")
	}

	// Create ticker for periodic refresh
	ticker := time.NewTicker(config.RefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			debugLog(config, "Cacher received shutdown signal")
			return ctx.Err()

		case <-ticker.C:
			debugLog(config, "Starting scheduled cache refresh")
			if err := refreshAll(ctx, config, client); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				showWarning(fmt.Sprintf("Cache refresh failed: %v", err))
				// Continue - will retry on next tick
			} else {
				timestamp := time.Now().Format("2006-01-02 15:04:05")
				debugLog(config, fmt.Sprintf("[%s] Cache refreshed", timestamp))
			}
		}
	}
}

func refreshAll(ctx context.Context, config *Config, client *PIAClient) error {
	// Check context before starting
	if ctx.Err() != nil {
		return ctx.Err()
	}

	var lastErr error

	// 1. Refresh login token
	debugLog(config, "Refreshing login token...")
	token, piaIPs, err := client.GetToken(ctx)
	if err != nil {
		debugLog(config, "Token refresh failed: %v", err)
		lastErr = err
	} else {
		// Save token
		if err := WriteToken(token); err != nil {
			debugLog(config, "Failed to write token: %v", err)
			lastErr = err
		} else {
			debugLog(config, "Token saved (length: %d)", len(token))
		}

		// Filter to IPv4 only (iptables exemptions don't support IPv6)
		piaIPv4s := filterIPv4Only(piaIPs)
		debugLog(config, "Filtered PIA IPs: %d total, %d IPv4", len(piaIPs), len(piaIPv4s))

		// Save PIA IPs (rolling cache of 5)
		for _, ip := range piaIPv4s {
			if err := AddIPToCache(PIAIPsFile, ip, MaxCachedIPs); err != nil {
				debugLog(config, "Failed to cache PIA IP %s: %v", ip, err)
			}
		}
		debugLog(config, "Cached %d PIA IPv4 address(es)", len(piaIPv4s))
	}

	// Check context between operations
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 2. Refresh server list
	debugLog(config, "Refreshing server list...")
	servers, serverlistIPs, err := client.GetServerList(ctx)
	if err != nil {
		debugLog(config, "Server list refresh failed: %v", err)
		lastErr = err
	} else {
		// Filter to IPv4 only (iptables exemptions don't support IPv6)
		serverlistIPv4s := filterIPv4Only(serverlistIPs)
		debugLog(config, "Filtered serverlist IPs: %d total, %d IPv4", len(serverlistIPs), len(serverlistIPv4s))

		// Save serverlist IPs (rolling cache of 5)
		for _, ip := range serverlistIPv4s {
			if err := AddIPToCache(ServerlistIPsFile, ip, MaxCachedIPs); err != nil {
				debugLog(config, "Failed to cache serverlist IP %s: %v", ip, err)
			}
		}
		debugLog(config, "Cached %d serverlist IPv4 address(es)", len(serverlistIPv4s))

		// Merge and save server cache
		if err := MergeAndSaveServers(servers); err != nil {
			debugLog(config, "Failed to save server cache: %v", err)
			lastErr = err
		} else {
			debugLog(config, "Server cache updated with %d servers", len(servers))
		}
	}

	return lastErr
}

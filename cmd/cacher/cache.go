package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// Cache file paths
	TokenFile         = "/tmp/pia_login_token"
	TokenTSFile       = "/tmp/pia_login_token_ts"
	PIAIPsFile        = "/tmp/pia_login_ips"
	ServerlistIPsFile = "/tmp/pia_serverlist_ips"
	ServerCacheFile   = "/tmp/pia_serverlist"

	// Maximum IPs to keep in rolling cache
	MaxCachedIPs = 5
)

// WriteToken writes the token and its timestamp
func WriteToken(token string) error {
	// Write token
	if err := os.WriteFile(TokenFile, []byte(strings.TrimSpace(token)), 0600); err != nil {
		return fmt.Errorf("failed to write token: %w", err)
	}

	// Write timestamp
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(TokenTSFile, []byte(ts), 0644); err != nil {
		return fmt.Errorf("failed to write token timestamp: %w", err)
	}

	return nil
}

// ReadIPs reads IPs from a cache file (one per line)
func ReadIPs(filepath string) ([]string, error) {
	file, err := os.Open(filepath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	defer file.Close()

	var ips []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		ip := strings.TrimSpace(scanner.Text())
		if ip != "" {
			ips = append(ips, ip)
		}
	}

	return ips, scanner.Err()
}

// WriteIPs writes IPs to a cache file (one per line)
func WriteIPs(filepath string, ips []string) error {
	content := strings.Join(ips, "\n")
	if len(ips) > 0 {
		content += "\n"
	}
	return os.WriteFile(filepath, []byte(content), 0644)
}

// AddIPToCache adds an IP to the rolling cache
// - If IP exists, moves it to the front (most recent)
// - If new IP, adds to front and trims to maxIPs
func AddIPToCache(filepath string, newIP string, maxIPs int) error {
	newIP = strings.TrimSpace(newIP)
	if newIP == "" {
		return nil
	}

	existing, err := ReadIPs(filepath)
	if err != nil {
		return err
	}

	// Check if IP already exists
	for i, ip := range existing {
		if ip == newIP {
			// Move to front (most recent)
			existing = append([]string{ip}, append(existing[:i], existing[i+1:]...)...)
			return WriteIPs(filepath, existing)
		}
	}

	// Add new IP at front
	existing = append([]string{newIP}, existing...)

	// Trim to max
	if len(existing) > maxIPs {
		existing = existing[:maxIPs]
	}

	return WriteIPs(filepath, existing)
}

// ReadServerCache reads the cached server list
func ReadServerCache() ([]CachedServer, error) {
	data, err := os.ReadFile(ServerCacheFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []CachedServer{}, nil
		}
		return nil, err
	}

	var servers []CachedServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, err
	}

	return servers, nil
}

// WriteServerCache writes the server cache
func WriteServerCache(servers []CachedServer) error {
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ServerCacheFile, data, 0644)
}

// MergeAndSaveServers merges new servers into the existing cache
// - Updates IPs for existing CNs
// - Adds new CNs
// - Never deletes CNs
func MergeAndSaveServers(newServers []CachedServer) error {
	existing, err := ReadServerCache()
	if err != nil {
		return fmt.Errorf("failed to read existing cache: %w", err)
	}

	// Create map of existing servers by CN
	byCN := make(map[string]*CachedServer)
	for i := range existing {
		byCN[existing[i].CN] = &existing[i]
	}

	// Merge new servers
	added := 0
	updated := 0
	for _, srv := range newServers {
		if ex, ok := byCN[srv.CN]; ok {
			// Update existing server if IP changed
			if ex.IP != srv.IP {
				ex.IP = srv.IP
				updated++
			}
			// Always update PF and Region in case they changed
			ex.PF = srv.PF
			ex.Region = srv.Region
		} else {
			// Add new server
			existing = append(existing, srv)
			byCN[srv.CN] = &existing[len(existing)-1]
			added++
		}
	}

	if added > 0 || updated > 0 {
		// Only write if there were changes
		if err := WriteServerCache(existing); err != nil {
			return fmt.Errorf("failed to write cache: %w", err)
		}
	}

	return nil
}

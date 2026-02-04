package cacher

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

// WriteToken writes the token and its timestamp.
func WriteToken(token string) error {
	if err := os.WriteFile(TokenFile, []byte(strings.TrimSpace(token)), 0600); err != nil {
		return fmt.Errorf("failed to write token: %w", err)
	}

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(TokenTSFile, []byte(ts), 0644); err != nil {
		return fmt.Errorf("failed to write token timestamp: %w", err)
	}

	return nil
}

// ReadIPs reads IPs from a cache file (one per line).
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

// WriteIPs writes IPs to a cache file (one per line).
func WriteIPs(filepath string, ips []string) error {
	content := strings.Join(ips, "\n")
	if len(ips) > 0 {
		content += "\n"
	}
	return os.WriteFile(filepath, []byte(content), 0644)
}

// AddIPToCache adds an IP to the rolling cache.
func AddIPToCache(filepath string, newIP string, maxIPs int) error {
	newIP = strings.TrimSpace(newIP)
	if newIP == "" {
		return nil
	}

	existing, err := ReadIPs(filepath)
	if err != nil {
		return err
	}

	for i, ip := range existing {
		if ip == newIP {
			existing = append([]string{ip}, append(existing[:i], existing[i+1:]...)...)
			return WriteIPs(filepath, existing)
		}
	}

	existing = append([]string{newIP}, existing...)

	if len(existing) > maxIPs {
		existing = existing[:maxIPs]
	}

	return WriteIPs(filepath, existing)
}

// ReadServerCache reads the cached server list.
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

// WriteServerCache writes the server cache.
func WriteServerCache(servers []CachedServer) error {
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ServerCacheFile, data, 0644)
}

// MergeAndSaveServers merges new servers into the existing cache.
func MergeAndSaveServers(newServers []CachedServer) error {
	existing, err := ReadServerCache()
	if err != nil {
		return fmt.Errorf("failed to read existing cache: %w", err)
	}

	byCN := make(map[string]*CachedServer)
	for i := range existing {
		byCN[existing[i].CN] = &existing[i]
	}

	added := 0
	updated := 0
	for _, srv := range newServers {
		if ex, ok := byCN[srv.CN]; ok {
			if ex.IP != srv.IP {
				ex.IP = srv.IP
				updated++
			}
			ex.PF = srv.PF
			ex.Region = srv.Region
		} else {
			existing = append(existing, srv)
			byCN[srv.CN] = &existing[len(existing)-1]
			added++
		}
	}

	if added > 0 || updated > 0 {
		if err := WriteServerCache(existing); err != nil {
			return fmt.Errorf("failed to write cache: %w", err)
		}
	}

	return nil
}

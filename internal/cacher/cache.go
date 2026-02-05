package cacher

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/pia"
)

// Backward-compat file paths consumed by vpn.sh until it is fully ported.
const (
	tokenFile         = "/tmp/pia_login_token"
	tokenTSFile       = "/tmp/pia_login_token_ts"
	piaIPsFile        = "/tmp/pia_login_ips"
	serverlistIPsFile = "/tmp/pia_serverlist_ips"
	serverCacheFile   = "/tmp/pia_serverlist"
)

// writeTokenFile writes the token and its timestamp to /tmp/ files.
func writeTokenFile(token string) error {
	if err := os.WriteFile(tokenFile, []byte(strings.TrimSpace(token)), 0600); err != nil {
		return fmt.Errorf("write token: %w", err)
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(tokenTSFile, []byte(ts), 0644); err != nil {
		return fmt.Errorf("write token timestamp: %w", err)
	}
	return nil
}

// writeServerFile writes the server cache to /tmp/.
func writeServerFile(servers []pia.CachedServer) error {
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(serverCacheFile, data, 0644)
}

// addIPToFile adds an IP to a rolling file-based cache.
func addIPToFile(filepath, newIP string, maxIPs int) error {
	newIP = strings.TrimSpace(newIP)
	if newIP == "" {
		return nil
	}

	existing, err := readIPsFile(filepath)
	if err != nil {
		return err
	}

	for i, ip := range existing {
		if ip == newIP {
			existing = append([]string{ip}, append(existing[:i], existing[i+1:]...)...)
			return writeIPsFile(filepath, existing)
		}
	}

	existing = append([]string{newIP}, existing...)
	if len(existing) > maxIPs {
		existing = existing[:maxIPs]
	}
	return writeIPsFile(filepath, existing)
}

// readIPsFile reads IPs from a file (one per line).
func readIPsFile(filepath string) ([]string, error) {
	file, err := os.Open(filepath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
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

// writeIPsFile writes IPs to a file (one per line).
func writeIPsFile(filepath string, ips []string) error {
	content := strings.Join(ips, "\n")
	if len(ips) > 0 {
		content += "\n"
	}
	return os.WriteFile(filepath, []byte(content), 0644)
}

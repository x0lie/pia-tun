package vpn

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/x0lie/pia-tun/internal/apperrors"
	"github.com/x0lie/pia-tun/internal/cacher"
	"github.com/x0lie/pia-tun/internal/dns"
	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/pia"
)

const latencyTestTimeout = 2 * time.Second

// selectServer fetches the server list, merges with cache, and selects by latency.
// Flow: fetch fresh (cached IP or DNS) → merge with cache → filter → latency test
func selectServer(ctx context.Context, cfg Config, fw *firewall.Firewall, cache *cacher.Cache, resolver *dns.Resolver, logger *log.Logger) (pia.Server, time.Duration, error) {
	log.Step(fmt.Sprintf("Selecting server across %s...", cfg.Location))

	// If no cached ips, resolve
	if cache.ServerListIPs == nil {
		ips, err := resolver.Resolve(ctx, "serverlist.piaservers.net")
		if err != nil {
			return pia.Server{}, 0, err
		}
		cache.MergeServerListIPs(ips)
	}

	// Gather and cache servers, clear IPs if failure
	servers, err := tryServerListIPs(ctx, cache.ServerListIPs, fw, logger)
	if err != nil {
		log.Warning("Cleared cached serverlist IPs")
		cache.ClearServerListIPs()
		return pia.Server{}, 0, err
	}
	cache.MergeServers(servers)

	// Check if Region exists
	allInRegion := filterServers(cache.Servers, cfg.Location, false)
	if len(allInRegion) == 0 {
		return pia.Server{}, 0, fmt.Errorf("%w: PIA_LOCATION not found: %s\n\n    Available regions:\n    %s",
			apperrors.ErrFatal, cfg.Location, regionList(cache.Servers, false))
	}

	// If required, Check if port forwarding supported
	candidates := allInRegion
	if cfg.PFRequired {
		candidates = filterServers(cache.Servers, cfg.Location, true)
		if len(candidates) == 0 {
			return pia.Server{}, 0, fmt.Errorf("%w: PIA_LOCATION does not support port forwarding: %s\n\n    Available regions with port forwarding:\n      %s",
				apperrors.ErrFatal, cfg.Location, regionList(cache.Servers, true))
		}
	}
	log.Success(fmt.Sprintf("Found %d candidates", len(candidates)))

	return raceServers(ctx, candidates, fw, latencyTestTimeout, logger)
}

// tryServerListIPs fetches the server list using given ips
func tryServerListIPs(ctx context.Context, ips []string, fw *firewall.Firewall, logger *log.Logger) ([]pia.Server, error) {
	client := pia.NewDirectClient(apiTimeout)

	for _, ip := range ips {
		comment := "pia-serverlist"
		logger.Debug("Trying serverlist IP: %s", ip)
		err := fw.AddExemption(ip, "443", "tcp", comment)
		if err != nil {
			logger.Debug("Failed to add exemption: %v", err)
			continue
		}
		servers, err := pia.FetchServerList(ctx, client, ip)
		fw.RemoveExemptions(comment)
		if err == nil {
			logger.Debug("Fetched server list via IP %s", ip)
			return servers, nil
		}
		logger.Debug("Serverlist fetch from %s failed: %v", ip, err)
	}
	return nil, fmt.Errorf("failed to obtain serverlist from all endpoints")
}

// filterServers returns servers matching any of the given locations +pf if enabled
// locations is a comma or space-separated list of region IDs (e.g., "us_ca,uk_london").
// Returns nil if no servers match.
func filterServers(servers []pia.Server, locations string, pfRequired bool) []pia.Server {
	if len(servers) == 0 {
		return nil
	}

	// Parse locations into a set
	locationSet := make(map[string]bool)
	for _, loc := range strings.FieldsFunc(locations, func(r rune) bool {
		return r == ',' || r == ' '
	}) {
		loc = strings.TrimSpace(loc)
		if loc != "" {
			locationSet[loc] = true
		}
	}

	if len(locationSet) == 0 {
		return nil // No locations specified
	}

	var filtered []pia.Server
	for _, srv := range servers {
		if !locationSet[srv.Region] {
			continue
		}
		if pfRequired && !srv.PF {
			continue
		}
		filtered = append(filtered, srv)
	}

	return filtered
}

// raceServers tests latency to candidates in parallel and returns the lowest-latency server.
// Each candidate gets a temporary firewall exemption for its IP on port 443.
func raceServers(ctx context.Context, candidates []pia.Server, fw *firewall.Firewall, dialTimeout time.Duration, logger *log.Logger) (pia.Server, time.Duration, error) {
	if len(candidates) == 0 {
		return pia.Server{}, 0, fmt.Errorf("no server candidates")
	}

	// Add all firewall exemptions upfront
	specs := make([]firewall.Exemption, len(candidates))
	for i, srv := range candidates {
		specs[i] = firewall.Exemption{IP: srv.IP, Port: "443", Proto: "tcp", Comment: srv.CN}
	}
	comments := fw.AddExemptions(specs...)

	// Clean up all exemptions when done
	defer fw.RemoveExemptions(comments...)

	// Test all candidates in parallel
	type result struct {
		idx     int
		latency time.Duration
	}
	resultCh := make(chan result, len(candidates))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for i, srv := range candidates {
		wg.Add(1)
		go func(idx int, ip string) {
			defer wg.Done()
			lat := measureLatency(ctx, ip, dialTimeout)
			resultCh <- result{idx: idx, latency: lat}
		}(i, srv.IP)
	}

	// Close channel when all goroutines complete
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect result(s)
	for r := range resultCh {
		srv := candidates[r.idx]
		if r.latency > 0 {
			logger.Debug("Server %s (%s): %dms", srv.CN, srv.IP, r.latency.Milliseconds())
			cancel()
			return srv, r.latency, nil
		} else {
			logger.Debug("Server %s (%s): timeout", srv.CN, srv.IP)
		}
	}

	log.Warning(fmt.Sprintf("All servers timed out, using fallback: %s", candidates[0].CN))
	return candidates[0], 0, nil
}

func measureLatency(ctx context.Context, ip string, timeout time.Duration) time.Duration {
	start := time.Now()

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, "443"))
	if err != nil {
		return 0
	}
	conn.Close()

	return time.Since(start)
}

func regionList(servers []pia.Server, pfOnly bool) string {
	if servers == nil {
		return "serverlist empty"
	}
	seen := make(map[string]bool)
	var regions []string
	for _, srv := range servers {
		if pfOnly && !srv.PF {
			continue
		}
		if !seen[srv.Region] {
			seen[srv.Region] = true
			regions = append(regions, srv.Region)
		}
	}
	sort.Strings(regions)

	const perLine = 5
	var lines []string
	for i := 0; i < len(regions); i += perLine {
		end := min(i+perLine, len(regions))
		lines = append(lines, strings.Join(regions[i:end], ", "))
	}
	return strings.Join(lines, "\n      ")
}

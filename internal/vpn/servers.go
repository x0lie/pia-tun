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

// selectServer fetches the server list, merges with cache, and selects by latency.
// Flow: fetch fresh (cached IP or DNS) → merge with cache → filter → latency test
func selectServer(ctx context.Context, cfg Config, fw *firewall.Firewall, cache *cacher.Cache, resolver *dns.Resolver, logger *log.Logger) (pia.Server, time.Duration, error) {
	if cfg.Location == "all" {
		log.Step("Selecting best server globally...")
	} else {
		log.Step("Selecting best server from %s...", cfg.Location)
	}

	// If no cached ips, resolve
	if cache.ServerListIPs == nil {
		ips, err := resolver.Resolve(ctx, pia.ServerlistHostname)
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

	var candidates []pia.Server
	if cfg.Location != "all" {
		// Check if Region exists
		allInRegion := filterServers(cache.Servers, cfg.Location, false)
		if len(allInRegion) == 0 {
			return pia.Server{}, 0, fmt.Errorf("%w: PIA_LOCATIONS not found: %s\n\n    Available regions:\n    %s",
				apperrors.ErrFatal, cfg.Location, regionList(cache.Servers, false))
		}

		// If required, Check if port forwarding supported
		candidates = allInRegion
		if cfg.PFRequired {
			candidates = filterServers(cache.Servers, cfg.Location, true)
			if len(candidates) == 0 {
				return pia.Server{}, 0, fmt.Errorf("%w: PIA_LOCATIONS does not support port forwarding: %s\n\n    Available regions with port forwarding:\n      %s",
					apperrors.ErrFatal, cfg.Location, regionList(cache.Servers, true))
			}
		}
	} else {
		candidates = onePerRegion(filterServers(cache.Servers, cfg.Location, cfg.PFRequired))
	}
	log.Success("Found %d candidates", len(candidates))

	return raceServers(ctx, candidates, fw, logger)
}

// tryServerListIPs fetches the server list using given ips
func tryServerListIPs(ctx context.Context, ips []string, fw *firewall.Firewall, logger *log.Logger) ([]pia.Server, error) {
	client := pia.NewDirectClient(apiTimeout)

	for _, ip := range ips {
		logger.Debug("Trying serverlist IP: %s", ip)
		if err := fw.AddExemption(ip, "443", "tcp", "pia-serverlist"); err != nil {
			logger.Debug("Failed to add exemption: %v", err)
			continue
		}
		servers, err := pia.FetchServerList(ctx, client, ip)
		fw.RemoveExemptions()
		if err != nil {
			logger.Debug("Serverlist fetch from %s failed: %v", ip, err)
			continue
		}
		logger.Debug("Fetched server list via IP %s", ip)
		return servers, nil
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

	allRegions := locationSet["all"]

	var filtered []pia.Server
	for _, srv := range servers {
		if !allRegions && !locationSet[srv.Region] {
			continue
		}
		if pfRequired && !srv.PF {
			continue
		}
		filtered = append(filtered, srv)
	}

	return filtered
}

func onePerRegion(candidates []pia.Server) []pia.Server {
	seen := make(map[string]bool)
	var out []pia.Server
	for _, srv := range candidates {
		if !seen[srv.Region] {
			seen[srv.Region] = true
			out = append(out, srv)
		}
	}
	return out
}

// raceServers tests latency to candidates in parallel and returns the lowest-latency server.
// Each candidate gets a temporary firewall exemption for its IP on port 443.
func raceServers(ctx context.Context, candidates []pia.Server, fw *firewall.Firewall, logger *log.Logger) (pia.Server, time.Duration, error) {
	if len(candidates) == 0 {
		return pia.Server{}, 0, fmt.Errorf("no server candidates")
	}
	logger.Debug("Testing latency to %d candidates", len(candidates))

	// Add all firewall exemptions upfront
	specs := make([]firewall.Exemption, len(candidates))
	for i, srv := range candidates {
		specs[i] = firewall.Exemption{IP: srv.IP, Port: "443", Proto: "tcp", Comment: srv.CN}
	}
	if err := fw.AddExemptions(specs...); err != nil {
		return pia.Server{}, 0, fmt.Errorf("raceServers: %w", err)
	}
	defer fw.RemoveExemptions()

	// Test all candidates in parallel
	type result struct {
		idx     int
		latency time.Duration
	}
	resultCh := make(chan result, len(candidates))

	const collectMax = 25

	minTimer := time.NewTimer(120 * time.Millisecond)
	maxTimer := time.NewTimer(600 * time.Millisecond)
	defer minTimer.Stop()
	defer maxTimer.Stop()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for i, srv := range candidates {
		wg.Add(1)
		go func(idx int, ip string) {
			defer wg.Done()
			lat := measureLatency(ctx, ip)
			resultCh <- result{idx: idx, latency: lat}
		}(i, srv.IP)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var collected []result

loop:
	for {
		select {
		case r, ok := <-resultCh:
			if !ok {
				break loop // all goroutines finished
			}
			if r.latency > 0 {
				srv := candidates[r.idx]
				logger.Debug("%-23s %-23s (%dms)", srv.CN, srv.Region, r.latency.Milliseconds())
				collected = append(collected, r)
				if len(collected) >= collectMax {
					cancel()
					break loop
				}
			}
		case <-minTimer.C:
			if len(collected) > 0 {
				cancel()
				break loop
			}
		case <-maxTimer.C:
			cancel()
			break loop
		case <-ctx.Done():
			break loop
		}
	}

	if len(collected) == 0 {
		log.Warning("All servers timed out, using fallback: %s", candidates[0].CN)
		return candidates[0], 0, nil
	}

	sort.Slice(collected, func(i, j int) bool {
		return collected[i].latency < collected[j].latency
	})

	best := collected[0]
	return candidates[best.idx], best.latency, nil
}

func measureLatency(ctx context.Context, ip string) time.Duration {
	start := time.Now()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip, "443"))
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

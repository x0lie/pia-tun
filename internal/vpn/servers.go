package vpn

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/pia"
)

// FilterServers returns servers matching any of the given locations +pf if enabled
// locations is a comma or space-separated list of region IDs (e.g., "us_ca,uk_london").
// Returns nil if no servers match.
func FilterServers(servers []pia.CachedServer, locations string, pfRequired bool) []pia.CachedServer {
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

	var filtered []pia.CachedServer
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

// serverLatency holds a server with its measured latency for sorting.
type serverLatency struct {
	server  pia.CachedServer
	latency time.Duration
}

// SelectServer tests latency to candidates in parallel and returns the lowest-latency server.
// Each candidate gets a temporary firewall exemption for its IP on port 443.
// dialTimeout controls how long to wait for each TCP connection attempt.
// If all servers timeout, returns the first candidate with latency 0.
// Returns error only if candidates is empty.
func SelectServer(ctx context.Context, candidates []pia.CachedServer, fw *firewall.Firewall, dialTimeout time.Duration, logger *log.Logger) (pia.CachedServer, time.Duration, error) {
	if len(candidates) == 0 {
		return pia.CachedServer{}, 0, fmt.Errorf("no server candidates")
	}

	logger.Debug("Testing latency to %d server candidates (parallel)", len(candidates))

	// Add all firewall exemptions upfront
	exemptions := make([]*firewall.Exemption, len(candidates))
	for i, srv := range candidates {
		exemption, err := fw.AddTemporaryExemption(srv.IP, "443", "tcp", fmt.Sprintf("latency_%d", i))
		if err != nil {
			logger.Debug("Failed to add exemption for %s: %v", srv.CN, err)
		}
		exemptions[i] = exemption
	}

	// Clean up all exemptions when done
	defer func() {
		for _, e := range exemptions {
			if e != nil {
				fw.RemoveTemporaryExemption(e)
			}
		}
	}()

	// Test all candidates in parallel
	type result struct {
		idx     int
		latency time.Duration
	}
	resultCh := make(chan result, len(candidates))

	var wg sync.WaitGroup
	for i, srv := range candidates {
		if exemptions[i] == nil {
			// No exemption = skip this candidate
			resultCh <- result{idx: i, latency: 0}
			continue
		}
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

	// Collect results
	var successful []serverLatency
	for r := range resultCh {
		srv := candidates[r.idx]
		if r.latency > 0 {
			logger.Debug("Server %s (%s): %dms", srv.CN, srv.IP, r.latency.Milliseconds())
			successful = append(successful, serverLatency{server: srv, latency: r.latency})
		} else {
			logger.Debug("Server %s (%s): timeout", srv.CN, srv.IP)
		}
	}

	if len(successful) == 0 {
		// All timed out, use first candidate as fallback
		logger.Debug("All servers timed out, using fallback: %s", candidates[0].CN)
		return candidates[0], 0, nil
	}

	// Sort by latency and pick lowest
	sort.Slice(successful, func(i, j int) bool {
		return successful[i].latency < successful[j].latency
	})

	best := successful[0]
	logger.Debug("Selected server: %s (%s) - %dms", best.server.CN, best.server.IP, best.latency.Milliseconds())

	// Log top 5 for debug
	for i := 0; i < len(successful) && i < 5; i++ {
		r := successful[i]
		logger.Debug("  %dms - %s (%s)", r.latency.Milliseconds(), r.server.CN, r.server.Region)
	}

	return best.server, best.latency, nil
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

package vpn

import (
	"context"
	"fmt"
	"net"
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
func FilterServers(servers []pia.Server, locations string, pfRequired bool) []pia.Server {
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

// SelectServer tests latency to candidates in parallel and returns the lowest-latency server.
// Each candidate gets a temporary firewall exemption for its IP on port 443.
// dialTimeout controls how long to wait for each TCP connection attempt.
// If all servers timeout, returns the first candidate with latency 0.
// Returns error only if candidates is empty.
func SelectServer(ctx context.Context, candidates []pia.Server, fw *firewall.Firewall, dialTimeout time.Duration, logger *log.Logger) (pia.Server, time.Duration, error) {
	if len(candidates) == 0 {
		return pia.Server{}, 0, fmt.Errorf("no server candidates")
	}

	log.Success(fmt.Sprintf("Testing %d candidates...", len(candidates)))

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
			cancel()
			logger.Debug("Server %s (%s): %dms", srv.CN, srv.IP, r.latency.Milliseconds())
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

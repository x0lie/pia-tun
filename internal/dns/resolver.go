package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
)

// defaultServers are the alternative addresses for
// dns9.quad9.net and dns11.quad9.net
var defaultServers = []string{
	"149.112.112.9",
	"149.112.112.11",
}

// hostResult holds the DNS lookup result for a single hostname.
// NXDomain=true means the hostname definitively does not exist.
// An absent map entry means a transient failure.
type hostResult struct {
	IPs      []string
	NXDomain bool
}

// Resolver performs Do53 resolution with temporary firewall exemptions.
type Resolver struct {
	fw         *firewall.Firewall
	dnsServers []string
	log        *log.Logger
}

// NewExemptResolver creates a Resolver that uses the given Firewall for temporary
// exemptions during DNS queries.
func NewExemptResolver(fw *firewall.Firewall, dns []string) *Resolver {
	var dnsServers []string
	if len(dns) > 0 {
		dnsServers = dns
	} else {
		dnsServers = defaultServers
	}
	return &Resolver{fw: fw, dnsServers: dnsServers, log: log.New("resolver")}
}

// Resolve resolves a hostname to IPv4 addresses using Do53.
func (r *Resolver) Resolve(ctx context.Context, hostname string) ([]string, error) {
	for _, srv := range r.dnsServers {
		m, err := r.query(ctx, []string{hostname}, srv)
		if err == nil {
			if result, ok := m[hostname]; ok && !result.NXDomain {
				return result.IPs, nil
			}
		}
	}
	return nil, fmt.Errorf("failed to resolve %s", hostname)
}

// ResolveAll resolves multiple hostnames in parallel under one exemption.
// Returns a map of hostname to hostResult (IPs or NXDomain=true), and an
// error if resolution failed entirely (e.g. no network).
// Hostnames with transient failures are absent from the map.
func (r *Resolver) ResolveAll(ctx context.Context, hostnames []string) (map[string]hostResult, error) {
	for _, srv := range r.dnsServers {
		m, err := r.query(ctx, hostnames, srv)
		if err != nil {
			continue
		}
		if len(m) > 0 {
			return m, nil
		}
	}
	return nil, fmt.Errorf("failed to resolve %s", strings.Join(hostnames, ", "))
}

// query performs a single DNS lookup against one server.
// A firewall exemption for port 53 is held for the duration of the query.
// Resolves multiple hostnames in parallel under one exemption.
func (r *Resolver) query(ctx context.Context, hostnames []string, srv string) (map[string]hostResult, error) {
	if err := r.fw.AddExemptions(
		firewall.Exemption{IP: srv, Port: "53", Proto: "udp", Comment: "dns_resolve"},
		firewall.Exemption{IP: srv, Port: "53", Proto: "tcp", Comment: "dns_resolve"},
	); err != nil {
		return nil, fmt.Errorf("add exemption for %s: %w", srv, err)
	}
	defer r.fw.RemoveExemptions()

	type res struct {
		hostname string
		ips      []string
		err      error
		nxdomain bool
	}
	ch := make(chan res, len(hostnames))

	for _, h := range hostnames {
		go func(h string) {
			rslv := &net.Resolver{ // fresh resolver per goroutine
				PreferGo: true,
				Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
					d := &net.Dialer{Timeout: 2 * time.Second}
					return d.DialContext(ctx, network, net.JoinHostPort(srv, "53"))
				},
			}
			addrs, err := rslv.LookupIP(ctx, "ip4", h)
			if err != nil {
				r.log.Debug("LookupIP %s via %s failed: %v", h, srv, err)
				var dnsErr *net.DNSError
				if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
					ch <- res{hostname: h, nxdomain: true}
				} else {
					ch <- res{hostname: h, err: err}
				}
				return
			}
			ips := make([]string, len(addrs))
			for i, addr := range addrs {
				ips[i] = addr.String()
			}
			r.log.Debug("Resolved %s to %v via %s", h, ips, srv)
			ch <- res{hostname: h, ips: ips}
		}(h)
	}

	results := make(map[string]hostResult)
	for range hostnames {
		item := <-ch
		switch {
		case item.nxdomain:
			results[item.hostname] = hostResult{NXDomain: true}
		case item.err == nil:
			results[item.hostname] = hostResult{IPs: item.ips}
		}
	}
	return results, nil
}

// ValidateDNS warns the user when DNS and BOOTSTRAP_DNS overlap one another
func (r *Resolver) ValidateDNS(dns []string, dnsMode string) error {
	if dnsMode == "system" || dnsMode == "do53" {
		var overlap bool
		for _, ip := range dns {
			if r.isBootstrapIP(ip) {
				log.Warning("BOOTSTRAP_DNS and DNS overlap detected: %s", ip)
				overlap = true
			}
		}
		if overlap {
			return fmt.Errorf("cannot continue with BOOTSTRAP_DNS and DNS overlap")
		}
	}
	return nil
}

// isBootstrapIP returns true when input ip matches bootstrapServers
func (r *Resolver) isBootstrapIP(ip string) bool {
	for _, srv := range r.dnsServers {
		if ip == srv {
			return true
		}
	}
	return false
}

package dns

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
)

type dnsServer struct {
	ip      string
	tlsHost string
}

var bootstrapServers = []dnsServer{
	{"9.9.9.9", "dns.quad9.net"},
	{"9.9.9.11", "dns11.quad9.net"},
}

// hostResult holds the DNS lookup result for a single hostname.
// NXDomain=true means the hostname definitively does not exist.
// An absent map entry means a transient failure.
type hostResult struct {
	IPs      []string
	NXDomain bool
}

// Resolver performs DNS-over-TLS resolution using Quad9 with temporary firewall
// exemptions. This is used for pre-VPN name resolution where the system resolver
// is unavailable (killswitch blocks port 53).
type Resolver struct {
	fw  *firewall.Firewall
	log *log.Logger
}

// NewExemptResolver creates a Resolver that uses the given Firewall for temporary
// exemptions during DNS queries.
func NewExemptResolver(fw *firewall.Firewall) *Resolver {
	return &Resolver{fw: fw, log: log.New("resolver")}
}

// Resolve resolves a hostname to IPv4 addresses using Quad9 DNS-over-TLS.
// Tries 9.9.9.9 first, falls back to 9.9.9.11.
func (r *Resolver) Resolve(ctx context.Context, hostname string) ([]string, error) {
	for _, srv := range bootstrapServers {
		m, err := r.queryDoT(ctx, []string{hostname}, srv)
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
	for _, srv := range bootstrapServers {
		m, err := r.queryDoT(ctx, hostnames, srv)
		if err != nil {
			continue
		}
		if len(m) > 0 {
			return m, nil
		}
	}
	return nil, fmt.Errorf("failed to resolve %s", strings.Join(hostnames, ", "))
}

// queryDoT performs a single DNS-over-TLS lookup against one Quad9 server.
// A firewall exemption for port 853 is held for the duration of the query.
// Resolves multiple hostnames in parallel under one exemption.
func (r *Resolver) queryDoT(ctx context.Context, hostnames []string, srv dnsServer) (map[string]hostResult, error) {
	if err := r.fw.AddExemption(srv.ip, "853", "tcp", "dns_resolve"); err != nil {
		return nil, fmt.Errorf("add exemption for %s: %w", srv.ip, err)
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
				Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
					d := &tls.Dialer{
						NetDialer: &net.Dialer{Timeout: 2 * time.Second},
						Config:    &tls.Config{ServerName: srv.tlsHost},
					}
					return d.DialContext(ctx, "tcp", net.JoinHostPort(srv.ip, "853"))
				},
			}
			addrs, err := rslv.LookupIP(ctx, "ip4", h)
			if err != nil {
				r.log.Debug("LookupIP %s via %s failed: %v", h, srv.ip, err)
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
			r.log.Debug("Resolved %s to %v via %s", h, ips, srv.ip)
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

func isBootstrapIP(ip string) bool {
	for _, srv := range bootstrapServers {
		if ip == srv.ip {
			return true
		}
	}
	return false
}

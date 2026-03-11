package dns

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/pia"
)

type dnsServer struct {
	ip      string
	tlsHost string
}

var dnsServers = []dnsServer{
	{"9.9.9.9", "dns.quad9.net"},
	{"9.9.9.11", "dns11.quad9.net"},
}

// Resolver performs DNS-over-TLS resolution using Quad9 with temporary firewall
// exemptions. This is used for pre-VPN name resolution where the system resolver
// is unavailable (killswitch blocks port 53).
type Resolver struct {
	fw  *firewall.Firewall
	log *log.Logger
}

// NewResolver creates a Resolver that uses the given Firewall for temporary
// exemptions during DNS queries.
func NewResolver(fw *firewall.Firewall) *Resolver {
	return &Resolver{fw: fw, log: log.New("resolver")}
}

// Resolve resolves a hostname to IPv4 addresses using Quad9 DNS-over-TLS.
// Tries 9.9.9.9 first, falls back to 9.9.9.11.
// Returns pia.ConnectivityError if all servers fail.
func (r *Resolver) Resolve(ctx context.Context, hostname string) ([]string, error) {
	for _, srv := range dnsServers {
		m, err := r.queryDoT(ctx, []string{hostname}, srv)
		if err == nil {
			if ips := m[hostname]; len(ips) > 0 {
				return ips, nil
			}
		}
	}
	return nil, &pia.ConnectivityError{Op: "dns", Msg: "failed to resolve " + hostname}
}

// ResolveAll resolves multiple hostnames in parallel under one exemption — used by captureRealIP
func (r *Resolver) ResolveAll(ctx context.Context, hostnames []string) (map[string][]string, error) {
	for _, srv := range dnsServers {
		m, err := r.queryDoT(ctx, hostnames, srv)
		if err == nil && len(m) > 0 {
			return m, nil
		}
	}
	return nil, &pia.ConnectivityError{Op: "dns", Msg: "failed to resolve hostnames"}
}

// queryDoT performs a single DNS-over-TLS lookup against one Quad9 server.
// A firewall exemption for port 853 is held for the duration of the query.
// queryDoT resolves multiple hostnames in parallel under one exemption
func (r *Resolver) queryDoT(ctx context.Context, hostnames []string, srv dnsServer) (map[string][]string, error) {
	if err := r.fw.AddExemption(srv.ip, "853", "tcp", "dns_resolve"); err != nil {
		return nil, fmt.Errorf("add exemption for %s: %w", srv.ip, err)
	}
	defer r.fw.RemoveExemptions("dns_resolve")

	type res struct {
		hostname string
		ips      []string
		err      error
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
				ch <- res{hostname: h, err: err}
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

	results := make(map[string][]string)
	for range hostnames {
		res := <-ch
		if res.err == nil {
			results[res.hostname] = res.ips
		}
	}
	return results, nil
}

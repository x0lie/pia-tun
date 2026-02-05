package pia

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
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
func NewResolver(fw *firewall.Firewall, logger *log.Logger) *Resolver {
	return &Resolver{fw: fw, log: logger}
}

// Resolve resolves a hostname to IPv4 addresses using Quad9 DNS-over-TLS.
// Tries 9.9.9.9 first, falls back to 9.9.9.11.
// Returns ConnectivityError if all servers fail.
func (r *Resolver) Resolve(ctx context.Context, hostname string) ([]string, error) {
	for i, srv := range dnsServers {
		ips, err := r.queryDoT(ctx, hostname, srv)
		if err != nil {
			r.log.Debug("DNS via %s failed: %v", srv.ip, err)
			if i < len(dnsServers)-1 {
				r.log.Debug("Trying fallback %s", dnsServers[i+1].ip)
			}
			continue
		}
		if len(ips) > 0 {
			r.log.Debug("Resolved %s to %v via %s", hostname, ips, srv.ip)
			return ips, nil
		}
	}
	return nil, &ConnectivityError{
		Op:  "dns",
		Msg: fmt.Sprintf("failed to resolve %s (all Quad9 servers failed)", hostname),
	}
}

// queryDoT performs a single DNS-over-TLS lookup against one Quad9 server.
// A firewall exemption for port 853 is held for the duration of the query.
func (r *Resolver) queryDoT(ctx context.Context, hostname string, srv dnsServer) ([]string, error) {
	exemption, err := r.fw.AddTemporaryExemption(srv.ip, "853", "tcp", "dns_resolve")
	if err != nil {
		return nil, fmt.Errorf("add exemption for %s: %w", srv.ip, err)
	}
	defer r.fw.RemoveTemporaryExemption(exemption)

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := &tls.Dialer{
				NetDialer: &net.Dialer{Timeout: 2 * time.Second},
				Config:    &tls.Config{ServerName: srv.tlsHost},
			}
			return d.DialContext(ctx, "tcp", net.JoinHostPort(srv.ip, "853"))
		},
	}

	addrs, err := resolver.LookupIP(ctx, "ip4", hostname)
	if err != nil {
		return nil, err
	}

	ips := make([]string, len(addrs))
	for i, addr := range addrs {
		ips[i] = addr.String()
	}
	return ips, nil
}

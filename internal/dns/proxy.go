package dns

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/x0lie/pia-tun/internal/apperrors"
	"github.com/x0lie/pia-tun/internal/log"
)

const (
	proxyIP      = "127.0.0.1"
	proxyAddr    = proxyIP + ":53"
	queryTimeout = 3 * time.Second
	maxDNSMsg    = 16384
)

type upstream struct {
	addr   string // "ip:port"
	tlsCfg *tls.Config
	mu     sync.Mutex               // serializes queries; conn is reused across calls
	conn   atomic.Pointer[tls.Conn] // nil when not connected; closed directly by CloseUpstreams
}

type Proxy struct {
	rawServers  []string
	upstreams   []*upstream
	resolver    *Resolver
	log         *log.Logger
	dialMu      sync.RWMutex
	dialsCtx    context.Context
	cancelDials context.CancelFunc
}

func New(rawServers []string, r *Resolver) *Proxy {
	return &Proxy{rawServers: rawServers, resolver: r, log: log.New("dotproxy")}
}

func (p *Proxy) Setup(ctx context.Context) error {
	p.upstreams = nil

	hostnames, err := p.parseUpstreams()
	if err != nil {
		return err
	}

	if err = p.resolveHostnames(ctx, hostnames); err != nil {
		return err
	}

	if err = p.start(ctx); err != nil {
		return fmt.Errorf("%w: %v", apperrors.ErrFatal, err) // not retryable (local bind)
	}
	return nil
}

// start binds listeners and writes resolv.conf. Returns when ready or on first error.
// Goroutines continue until ctx is cancelled.
func (p *Proxy) start(ctx context.Context) error {
	var idx atomic.Uint64
	handle := func(ctx context.Context, query []byte) []byte { return p.dispatch(ctx, p.upstreams, &idx, query) }

	readyCh := make(chan struct{}, 2)
	errCh := make(chan error, 2)
	go func() { errCh <- serveUDP(ctx, readyCh, handle) }()
	go func() { errCh <- serveTCP(ctx, readyCh, handle) }()

	for ready := 0; ready < 2; {
		select {
		case err := <-errCh:
			p.CloseUpstreams()
			return fmt.Errorf("dot proxy: %w", err)
		case <-readyCh:
			ready++
		}
	}

	p.dialsCtx, p.cancelDials = context.WithCancel(context.Background())

	if err := Write([]string{proxyIP}); err != nil {
		p.CloseUpstreams()
		return fmt.Errorf("write resolv.conf: %w", err)
	}
	p.log.Debug("Listening on %s (UDP+TCP)", proxyAddr)

	go func() {
		defer p.CloseUpstreams()
		select {
		case err := <-errCh:
			if ctx.Err() == nil {
				log.Error("DoT proxy error: %v", err)
			}
		case <-ctx.Done():
		}
	}()
	return nil
}

// Display returns a formatted string of active DoT upstreams for logging,
// grouped as "hostname (ip1, ip2, ...)".
func (p *Proxy) Display() string {
	var parts []string
	groups := make(map[string][]string) // serverName -> IPs
	var order []string
	for _, u := range p.upstreams {
		host, _, _ := net.SplitHostPort(u.addr)
		sn := u.tlsCfg.ServerName
		if _, seen := groups[sn]; !seen {
			order = append(order, sn)
		}
		groups[sn] = append(groups[sn], host)
	}
	for _, sn := range order {
		parts = append(parts, fmt.Sprintf("%s (%s)", sn, strings.Join(groups[sn], ", ")))
	}
	return strings.Join(parts, ", ")
}

func (p *Proxy) CloseUpstreams() {
	// Cancel in-flight dials so forwardDoT exits DialContext immediately.
	// Reset so the proxy remains functional after reconnect.
	p.dialMu.Lock()
	if p.cancelDials != nil {
		p.cancelDials()
		p.dialsCtx, p.cancelDials = context.WithCancel(context.Background())
	}
	p.dialMu.Unlock()

	// Close connections directly without acquiring u.mu — this immediately unblocks
	// any forwardDoT goroutine blocked in readMsg/writeMsg.
	for _, u := range p.upstreams {
		if conn := u.conn.Swap(nil); conn != nil {
			conn.Close()
		}
	}
}

// serveUDP listens for DNS queries over UDP and dispatches them.
func serveUDP(ctx context.Context, readyCh chan<- struct{}, handle func(context.Context, []byte) []byte) error {
	pc, err := net.ListenPacket("udp", proxyAddr)
	if err != nil {
		return err
	}
	defer pc.Close()
	readyCh <- struct{}{}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			pc.Close()
		case <-done:
		}
	}()

	buf := make([]byte, 4096)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		query := make([]byte, n)
		copy(query, buf[:n])
		go func(q []byte, a net.Addr) {
			resp := handle(ctx, q)
			if resp != nil {
				pc.WriteTo(resp, a)
			}
		}(query, addr)
	}
}

// serveTCP listens for DNS queries over TCP and dispatches them.
func serveTCP(ctx context.Context, readyCh chan<- struct{}, handle func(context.Context, []byte) []byte) error {
	ln, err := net.Listen("tcp", proxyAddr)
	if err != nil {
		return err
	}
	defer ln.Close()
	readyCh <- struct{}{}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			ln.Close()
		case <-done:
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go handleTCPConn(ctx, conn, handle)
	}
}

func handleTCPConn(ctx context.Context, conn net.Conn, handle func(context.Context, []byte) []byte) {
	defer conn.Close()
	for {
		conn.SetDeadline(time.Now().Add(queryTimeout))
		query, err := readMsg(conn)
		if err != nil {
			return
		}
		resp := handle(ctx, query)
		if resp == nil {
			return
		}
		if err := writeMsg(conn, resp); err != nil {
			return
		}
	}
}

// dispatch forwards a raw DNS query to an upstream server, trying each in round-robin order.
func (p *Proxy) dispatch(_ context.Context, upstreams []*upstream, idx *atomic.Uint64, query []byte) []byte {
	p.dialMu.RLock()
	dialCtx := p.dialsCtx
	p.dialMu.RUnlock()

	n := len(upstreams)
	start := int(idx.Add(1)-1) % n

	for i := 0; i < n; i++ {
		u := upstreams[(start+i)%n]
		resp, err := u.forwardDoT(dialCtx, query)
		if err != nil {
			if dialCtx.Err() != nil {
				break
			}
			p.log.Trace("Upstream %s failed: %v", u.addr, err)
			continue
		}
		return resp
	}

	return servfail(query)
}

// forwardDoT sends a DNS query over a persistent TLS connection.
// On a stale connection (write or read error), reconnects once and retries.
// Queries to the same upstream are serialized via mu.
func (u *upstream) forwardDoT(dialCtx context.Context, query []byte) ([]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	for attempt := 0; attempt < 2; attempt++ {
		conn := u.conn.Load()
		if conn == nil {
			c, err := (&tls.Dialer{
				NetDialer: &net.Dialer{Timeout: queryTimeout},
				Config:    u.tlsCfg,
			}).DialContext(dialCtx, "tcp", u.addr)
			if err != nil {
				return nil, err
			}
			conn = c.(*tls.Conn)
			u.conn.Store(conn)
		}
		conn.SetDeadline(time.Now().Add(queryTimeout))
		if err := writeMsg(conn, query); err != nil {
			u.conn.Store(nil)
			conn.Close()
			continue
		}
		resp, err := readMsg(conn)
		if err != nil {
			u.conn.Store(nil)
			conn.Close()
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("upstream %s: failed after reconnect", u.addr)
}

// writeMsg writes a DNS message with a 2-byte big-endian length prefix (TCP framing).
func writeMsg(conn net.Conn, msg []byte) error {
	buf := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(buf[:2], uint16(len(msg)))
	copy(buf[2:], msg)
	_, err := conn.Write(buf)
	return err
}

// readMsg reads a length-prefixed DNS message from a TCP connection.
func readMsg(conn net.Conn) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(hdr[:])
	if n > maxDNSMsg {
		return nil, fmt.Errorf("oversized DNS message: %d bytes", n)
	}
	msg := make([]byte, n)
	_, err := io.ReadFull(conn, msg)
	return msg, err
}

// servfail constructs a minimal SERVFAIL response for the given query.
func servfail(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}
	resp := make([]byte, 12)
	copy(resp[:2], query[:2]) // copy ID
	resp[2] = query[2] | 0x80 // QR=1, preserve opcode+RD
	resp[3] = 0x82            // RA=1, RCODE=2 (SERVFAIL)
	return resp
}

// parseUpstreams validates rawServers and returns hostname entries for resolution.
// Bare IPs are rejected — DoT requires a hostname for TLS certificate verification.
func (p *Proxy) parseUpstreams() ([]string, error) {
	var hostnames []string
	for _, host := range p.rawServers {
		host = strings.TrimPrefix(host, "tls://")
		if net.ParseIP(host) != nil {
			log.Warning("DNS=tls://%s is a bare IP — hostname required for TLS verification (e.g. tls://one.one.one.one)", host)
			continue
		}
		hostnames = append(hostnames, host)
	}
	if len(hostnames) == 0 {
		return nil, fmt.Errorf("%w: no valid upstreams configured", apperrors.ErrFatal)
	}
	return hostnames, nil
}

// resolveHostnames resolves hostname-based upstreams via Quad9 and adds them to p.upstreams.
// NXDOMAIN results are warned and skipped; transient failures are retryable.
func (p *Proxy) resolveHostnames(ctx context.Context, hostnames []string) error {
	results, err := p.resolver.ResolveAll(ctx, hostnames)
	if err != nil {
		return fmt.Errorf("failed to resolve DoT hostnames: %w", err)
	}

	for h, result := range results {
		if result.NXDomain {
			log.Warning("Hostname %s not found, skipping", h)
			continue
		}
		for _, ip := range result.IPs {
			if isBootstrapIP(ip) {
				p.log.Debug("Cannot use %s from %s (bootstrap DNS), skipping", ip, h)
				continue
			}
			p.upstreams = append(p.upstreams, &upstream{
				addr: net.JoinHostPort(ip, "853"),
				tlsCfg: &tls.Config{
					ServerName:         h,
					ClientSessionCache: tls.NewLRUClientSessionCache(4),
				},
			})
		}
	}

	if len(p.upstreams) == 0 {
		return fmt.Errorf("%w: no upstream servers available", apperrors.ErrFatal)
	}
	return nil
}

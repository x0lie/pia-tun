package pia

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"
)

// NewBoundTransport creates an http.Transport bound to the pia0 interface.
func NewBoundTransport(dialTimeout time.Duration) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			iface, err := net.InterfaceByName("pia0")
			if err != nil {
				return nil, fmt.Errorf("failed to get pia0 interface: %w", err)
			}

			addrs, err := iface.Addrs()
			if err != nil {
				return nil, fmt.Errorf("failed to get interface addresses: %w", err)
			}

			if len(addrs) == 0 {
				return nil, fmt.Errorf("no addresses on pia0 interface")
			}

			ipNet, ok := addrs[0].(*net.IPNet)
			if !ok {
				return nil, fmt.Errorf("invalid address type")
			}

			localAddr := &net.TCPAddr{
				IP: ipNet.IP,
			}

			d := &net.Dialer{
				LocalAddr: localAddr,
				Timeout:   dialTimeout,
				KeepAlive: 30 * time.Second,
			}

			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // PIA uses self-signed certs
		},
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
}

// NewBoundClient creates an http.Client with a pia0-bound transport.
func NewBoundClient(dialTimeout, clientTimeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   clientTimeout,
		Transport: NewBoundTransport(dialTimeout),
	}
}

// NewDirectClient creates an http.Client for pre-VPN API calls.
// Not bound to any interface, skips TLS certificate verification
// (PIA's auth and serverlist endpoints use certs that don't chain to
// a system-trusted CA when accessed by IP).
func NewDirectClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
			DialContext:         (&net.Dialer{Timeout: timeout}).DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
		},
	}
}

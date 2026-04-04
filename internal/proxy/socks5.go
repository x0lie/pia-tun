package proxy

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
)

func (c *Config) StartSOCKS5(ctx context.Context) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", c.Socks5Port))
	if err != nil {
		log.Error("Failed to start SOCKS5 proxy: %v", err)
		return
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warning("SOCKS5 accept error: %v", err)
			continue
		}
		go c.handleSOCKS5(conn)
	}
}

func (c *Config) handleSOCKS5(conn net.Conn) {
	defer conn.Close()

	// Step 1: Read client greeting [ver, nMethods]
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}
	// If not speaking SOCKS5, drop connection
	if header[0] != 0x05 {
		return
	}
	// Drain the methods list — length is in header[1]
	if _, err := io.ReadFull(conn, make([]byte, header[1])); err != nil {
		return
	}

	// Step 2: Tell the client which auth method we require
	authRequired := c.User != "" && c.Pass != ""
	if authRequired {
		conn.Write([]byte{0x05, 0x02}) // username/password
	} else {
		conn.Write([]byte{0x05, 0x00}) // no auth
	}

	// Step 3: Auth sub-negotiation (RFC 1929)
	if authRequired {
		// Read [ver, uLen]
		authHeader := make([]byte, 2)
		if _, err := io.ReadFull(conn, authHeader); err != nil {
			return
		}
		user := make([]byte, authHeader[1])
		if _, err := io.ReadFull(conn, user); err != nil {
			return
		}
		pLenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, pLenBuf); err != nil {
			return
		}
		pass := make([]byte, pLenBuf[0])
		if _, err := io.ReadFull(conn, pass); err != nil {
			return
		}

		userMatch := subtle.ConstantTimeCompare(user, []byte(c.User))
		passMatch := subtle.ConstantTimeCompare(pass, []byte(c.Pass))
		if userMatch != 1 || passMatch != 1 {
			conn.Write([]byte{0x01, 0x01}) // auth failed
			return
		}
		conn.Write([]byte{0x01, 0x00}) // auth success
	}

	// Step 4: Read CONNECT request [ver, cmd, rsv, atyp]
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return
	}
	if req[1] != 0x01 { // only CONNECT supported, not BIND or UDP
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Read destination address — format depends on atyp
	var host string
	switch req[3] {
	case 0x01: // IPv4 — always 4 bytes
		addr := make([]byte, 4)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return
		}
		host = net.IP(addr).String()
	case 0x03: // Domain — 1 byte length prefix, then that many bytes
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return
		}
		domain := make([]byte, lenBuf[0])
		if _, err := io.ReadFull(conn, domain); err != nil {
			return
		}
		host = string(domain)
	case 0x04: // IPv6 — not supported by PIA
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Port is always 2 bytes, big-endian
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return
	}
	port := int(portBuf[0])<<8 | int(portBuf[1])

	// Step 5: Dial the destination and tunnel
	destConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 10*time.Second)
	if err != nil {
		conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer destConn.Close()

	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	go transfer(destConn, conn)
	transfer(conn, destConn)
}

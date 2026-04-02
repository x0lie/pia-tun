package dns

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
)

// SetNetResolver sets net.DefaultResolver to read resolv.conf on every lookup
func SetNetResolver() {
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			servers, err := Read()
			if err != nil || len(servers) == 0 {
				return nil, fmt.Errorf("no nameservers in resolv.conf")
			}
			return (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(servers[0], "53"))
		},
	}
}

// Read reads and returns /etc/resolv.conf servers
func Read() ([]string, error) {
	data, err := os.ReadFile("/etc/resolv.conf")
	var servers []string
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "nameserver ") {
			server := strings.TrimPrefix(line, "nameserver ")
			servers = append(servers, server)
		}
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("no nameservers exist in resolv.conf")
	}
	return servers, nil
}

// WriteDNS writes /etc/resolv.conf based on the DNS configuration.
func Write(servers []string) error {
	var buf strings.Builder
	buf.WriteString("# Set by pia-tun\n")
	for _, s := range servers {
		buf.WriteString("nameserver ")
		buf.WriteString(s)
		buf.WriteString("\n")
	}

	if len(servers) > 1 {
		buf.WriteString("options rotate\n")
	}

	if err := os.WriteFile("/etc/resolv.conf", []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write /etc/resolv.conf: %w", err)
	}

	return nil
}

// Clear empties /etc/resolv.conf of data
func Clear() error {
	if err := os.WriteFile("/etc/resolv.conf", []byte{}, 0644); err != nil {
		return fmt.Errorf("failed to clear /etc/resolv.conf: %w", err)
	}
	return nil
}

// Backup moves /etc/resolv.conf to /etc/resolv.bak
func Backup() error {
	if _, err := os.Stat("/etc/resolv.bak"); err == nil {
		return nil // backup already exists, do not overwrite
	}
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return fmt.Errorf("failed to read resolv.conf for backup: %w", err)
	}
	if err := os.WriteFile("/etc/resolv.bak", data, 0644); err != nil {
		return fmt.Errorf("failed to write resolv.bak: %w", err)
	}
	return nil
}

// Restore moves /etc/resolv.bak to /etc/resolv.conf
func Restore() error {
	data, err := os.ReadFile("/etc/resolv.bak")
	if err != nil {
		return fmt.Errorf("failed to read resolv.bak: %w", err)
	}
	if err := os.WriteFile("/etc/resolv.conf", data, 0644); err != nil {
		return fmt.Errorf("failed to restore resolv.conf: %w", err)
	}
	os.Remove("/etc/resolv.bak")
	return nil
}

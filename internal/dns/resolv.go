package dns

import (
	"fmt"
	"os"
	"strings"
)

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

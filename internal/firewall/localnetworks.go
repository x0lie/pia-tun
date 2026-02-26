package firewall

import (
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/x0lie/pia-tun/internal/log"
)

// resolveLocalNetworks parses the LOCAL_NETWORKS input string into separate
// IPv4 and IPv6 CIDR slices. Supports special values "all", and "auto"
func resolveLocalNetworks(input string) (ipv4, ipv6 []string) {
	if input == "" {
		input = "auto"
	}

	parts := strings.Split(input, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		switch part {
		case "auto":
			v4, v6 := detectKernelSubnets()
			ipv4 = append(ipv4, v4...)
			ipv6 = append(ipv6, v6...)
		case "all":
			ipv4 = append(ipv4, "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")
			ipv6 = append(ipv6, "fc00::/7")
		case "none":
		default:
			_, _, err := net.ParseCIDR(part)
			if err != nil {
				log.Warning(fmt.Sprintf("Skipping %s (invalid CIDR)", part))
				continue
			}
			if strings.Contains(part, ":") {
				ipv6 = append(ipv6, part)
			} else {
				ipv4 = append(ipv4, part)
			}
		}
	}
	return ipv4, ipv6
}

// detectKernelSubnets returns connected subnets from kernel routing table.
func detectKernelSubnets() (ipv4, ipv6 []string) {
	// IPv4 kernel routes
	if out, err := exec.Command("ip", "-4", "route", "show", "proto", "kernel").Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.Contains(fields[0], "/") {
				ipv4 = append(ipv4, fields[0])
			}
		}
	}

	// IPv6 kernel routes (exclude link-local)
	if out, err := exec.Command("ip", "-6", "route", "show", "proto", "kernel").Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.Contains(fields[0], "/") && !strings.HasPrefix(fields[0], "fe80") {
				ipv6 = append(ipv6, fields[0])
			}
		}
	}

	return ipv4, ipv6
}

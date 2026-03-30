package firewall

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"github.com/x0lie/pia-tun/internal/log"
)

const priorityLocalNet = 100

// setupLocalNetworks orchestrates the rules and routes creation
// for bi-directional access on LOCAL_NETWORKS
func (fw *Firewall) setupLocalNetworks(localNetworks string) (string, error) {
	lansV4, lansV6 := resolveLocalNetworks(localNetworks)
	lans := formatNetworks(lansV4, lansV6)
	fw.log.Debug("Resolved local networks: %s", lans)

	if err := fw.setupLocalRoutes(lansV4, lansV6); err != nil {
		return "", fmt.Errorf("failed to setup routes: %w", err)
	}
	fw.log.Debug("Added routes for %s", lans)

	if err := fw.setupLocalRules(lansV4, lansV6); err != nil {
		return "", fmt.Errorf("failed to setup input chain: %w", err)
	}
	fw.log.Debug("Added rules for %s", lans)

	return lans, nil
}

// setupLocalRoutes adds routing rules so traffic to LOCAL_NETWORKS
// uses the main routing table instead of the VPN tunnel.
func (fw *Firewall) setupLocalRoutes(lansV4, lansV6 []string) error {
	fw.cleanupLocalRoutes()

	priority := strconv.Itoa(priorityLocalNet)
	for _, network := range lansV4 {
		args := []string{"rule", "add", "to", network, "table", "main", "priority", priority}
		if err := exec.Command("ip", args...).Run(); err != nil {
			return fmt.Errorf("add route %s: %w", network, err)
		}
	}
	for _, network := range lansV6 {
		args := []string{"-6", "rule", "add", "to", network, "table", "main", "priority", priority}
		if err := exec.Command("ip", args...).Run(); err != nil {
			return fmt.Errorf("add route %s: %w", network, err)
		}
	}
	return nil
}

// cleanupLocalRoutes removes all routing rules at priority 100 (IPv4 and IPv6).
func (fw *Firewall) cleanupLocalRoutes() {
	priority := strconv.Itoa(priorityLocalNet)
	for _, family := range [][]string{{}, {"-6"}} {
		args := append(family, "rule", "del", "priority", priority)
		for {
			if err := exec.Command("ip", args...).Run(); err != nil {
				break
			}
			fw.log.Debug("Removed routing rule at priority %d", priorityLocalNet)
		}
	}
}

// setupLocalRules inserts local network ACCEPT rules before DROP in all 4 chains.
// INPUT chains get -s (source) rules, OUTPUT chains get -d (destination) rules.
func (fw *Firewall) setupLocalRules(lansV4, lansV6 []string) error {
	// IPv4 local networks → VPN_IN and VPN_OUT
	if len(lansV4) > 0 {
		for _, network := range lansV4 {
			if err := fw.insertBeforeDrop(fw.ipt4, ChainIn, "-s", network, "-j", "ACCEPT"); err != nil {
				return fmt.Errorf("%s local network %s: %w", ChainIn, network, err)
			}
			if err := fw.insertBeforeDrop(fw.ipt4, ChainOut, "-d", network, "-j", "ACCEPT"); err != nil {
				return fmt.Errorf("%s local network %s: %w", ChainOut, network, err)
			}
		}
	}

	// IPv6 local networks → VPN_IN6 and VPN_OUT6
	if len(lansV6) > 0 {
		for _, network := range lansV6 {
			if err := fw.insertBeforeDrop(fw.ipt6, chainIn6, "-s", network, "-j", "ACCEPT"); err != nil {
				return fmt.Errorf("%s local network %s: %w", chainIn6, network, err)
			}
			if err := fw.insertBeforeDrop(fw.ipt6, chainOut6, "-d", network, "-j", "ACCEPT"); err != nil {
				return fmt.Errorf("%s local network %s: %w", chainOut6, network, err)
			}
		}
	}

	return nil
}

// resolveLocalNetworks parses the LOCAL_NETWORKS input string into separate
// IPv4 and IPv6 CIDR slices. Supports special value "all". Caller is responsible
// for not calling this when LOCAL_NETWORKS=none
func resolveLocalNetworks(input string) (ipv4, ipv6 []string) {
	if !strings.Contains(input, "all") {
		v4, v6 := detectKernelSubnets()
		ipv4 = append(ipv4, v4...)
		ipv6 = append(ipv6, v6...)
	}

	parts := strings.Split(input, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		switch part {
		case "all":
			ipv4 = append(ipv4, "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")
			ipv6 = append(ipv6, "fc00::/7")
		default:
			_, _, err := net.ParseCIDR(part)
			if err != nil {
				log.Warning("Skipping local network: %s (invalid CIDR)", part)
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

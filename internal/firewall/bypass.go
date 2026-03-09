package firewall

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/x0lie/pia-tun/internal/log"
)

// WANCheckIPs are the NIST/NCAR time servers used for WAN connectivity checks.
// Bypass routing ensures these are reachable even when the VPN tunnel is down.
var WANCheckIPs = []string{
	"129.6.15.28",
	"129.6.15.29",
	"132.163.96.1",
	"132.163.97.1",
	"128.138.140.44",
}

const (
	bypassTable    = "100"
	bypassPriority = "50"
	bypassComment  = "bypass_routes"
)

// setupBypassRoutes creates routing table 100 with the default gateway and adds
// ip rules so WAN check IPs use that table (bypassing the VPN tunnel).
func (fw *Firewall) setupBypassRoutes() (string, error) {
	gateway, err := getDefaultGateway()
	if err != nil {
		return "", err
	}

	iface, err := getDefaultInterface()
	if err != nil {
		log.Warning(fmt.Sprintf("Defaulting to eth0: %s", err))
		gateway = "eth0"
	}

	fw.log.Debug("Setting up bypass routing table")
	fw.log.Debug("Adding default route to table 100: %s via %s", gateway, iface)

	// Add default route to bypass table (ignore error if already exists)
	exec.Command("ip", "route", "add", "default", "via", gateway, "dev", iface, "table", bypassTable).Run()

	fw.log.Debug("Adding bypass rules for WAN check IPs (priority 50)")
	for _, ip := range WANCheckIPs {
		exec.Command("ip", "rule", "add", "to", ip, "table", bypassTable, "priority", bypassPriority).Run()
	}

	fw.log.Debug("Bypass routing table configured")

	return iface, nil
}

// cleanupBypassRoutes removes ip rules and the bypass table default route.
func (fw *Firewall) cleanupBypassRoutes() {
	fw.log.Debug("Cleaning up bypass routing rules")

	for _, ip := range WANCheckIPs {
		exec.Command("ip", "rule", "del", "to", ip, "table", bypassTable, "priority", bypassPriority).Run()
	}

	fw.log.Debug("Removing bypass table default route")
	exec.Command("ip", "route", "del", "default", "table", bypassTable).Run()
}

// insertBypassFirewallRules inserts ACCEPT rules for each WAN check IP into the
// given chain, just before the terminal DROP rule. Rules are restricted to TCP
// port 13 (DAYTIME) on the specified interface.
func (fw *Firewall) insertBypassFirewallRules(iface string) error {
	fw.log.Debug("Inserting bypass route rules before DROP (TCP/13 via default gateway)")

	for _, ip := range WANCheckIPs {
		spec := []string{
			"-o", iface, "-p", "tcp", "--dport", "13",
			"-d", ip, "-j", "ACCEPT",
			"-m", "comment", "--comment", bypassComment,
		}
		if err := fw.insertBeforeDrop(fw.ipt4, chainOut, spec...); err != nil {
			return fmt.Errorf("insert bypass rule for %s: %w", ip, err)
		}
	}

	return nil
}

// getDefaultGateway returns the default gateway IP from the routing table.
func getDefaultGateway() (string, error) {
	out, err := exec.Command("ip", "route").Output()
	if err != nil {
		return "", fmt.Errorf("ip route: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "default") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				return fields[2], nil
			}
		}
	}
	return "", fmt.Errorf("cannot determine default gateway")
}

// getDefaultInterface returns the default network interface from the routing table.
func getDefaultInterface() (string, error) {
	out, err := exec.Command("ip", "route").Output()
	if err != nil {
		return "", fmt.Errorf("ip route: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "default") {
			fields := strings.Fields(line)
			if len(fields) >= 5 {
				return fields[4], nil
			}
		}
	}
	return "", fmt.Errorf("cannot determine default interface")
}

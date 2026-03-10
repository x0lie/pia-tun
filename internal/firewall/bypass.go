package firewall

import (
	"fmt"
	"os/exec"
	"strconv"
)

const (
	bypassPriority = 50
	bypassComment  = "bypass_routes"
)

// CheckIPs are the NIST/NCAR time servers used for WAN connectivity checks.
// Bypass routing ensures these are reachable even when the VPN tunnel is down.
var WANCheckIPs = []string{
	"129.6.15.28",
	"129.6.15.29",
	"132.163.96.1",
	"132.163.97.1",
	"128.138.140.44",
}

// setupBypass creates the bypass for wan check capability
func (fw *Firewall) setupBypass() error {
	if err := fw.setupBypassRoutes(); err != nil {
		return fmt.Errorf("failed to setup bypass routes: %w", err)
	}
	if err := fw.insertBypassFirewallRules(); err != nil {
		return fmt.Errorf("failed to insert bypass rules: %w", err)
	}
	return nil
}

// setupBypassRoutes creates routes with priority 50 to main table
func (fw *Firewall) setupBypassRoutes() error {
	for _, network := range WANCheckIPs {
		args := []string{"rule", "add", "to", network, "table", "main", "priority", strconv.Itoa(bypassPriority)}
		if err := exec.Command("ip", args...).Run(); err != nil {
			return err
		}
		fw.log.Debug("Added wan check bypass route (%s)", network)
	}
	return nil
}

// cleanupBypassRoutes removes ip rules.
func (fw *Firewall) cleanupBypassRoutes() {
	priority := strconv.Itoa(bypassPriority)
	for {
		if err := exec.Command("ip", "rule", "del", "priority", priority).Run(); err != nil {
			break
		}
		fw.log.Debug("Removed routing rule at priority %v", priority)
	}
}

// insertBypassFirewallRules inserts ACCEPT rules for each WanCheckIP into the
// VPN_OUT chain before DROP. Rules are restricted to TCP port 13 (DAYTIME).
func (fw *Firewall) insertBypassFirewallRules() error {
	fw.log.Debug("Inserting bypass route rules before DROP (TCP/13 via default gateway)")

	for _, ip := range WANCheckIPs {
		spec := []string{
			"-p", "tcp", "--dport", "13",
			"-d", ip, "-j", "ACCEPT",
			"-m", "comment", "--comment", bypassComment,
		}
		if err := fw.insertBeforeDrop(fw.ipt4, chainOut, spec...); err != nil {
			return fmt.Errorf("insert bypass rule for %s: %w", ip, err)
		}
	}

	return nil
}

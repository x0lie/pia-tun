package firewall

import (
	"fmt"
	"os/exec"
	"strconv"
)

const priorityLocalNet = 100

// RFC1918 and IPv6 ULA networks that should bypass the VPN tunnel.
var privateNetworks = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

var privateNetworksV6 = []string{
	"fc00::/7",
}

// setupPrivateRoutes adds routing rules so traffic to RFC1918 and ULA
// networks uses the main routing table instead of the VPN tunnel.
func (fw *Firewall) setupPrivateRoutes() error {
	for _, network := range privateNetworks {
		args := []string{"rule", "add", "to", network, "table", "main", "priority", strconv.Itoa(priorityLocalNet)}
		if err := exec.Command("ip", args...).Run(); err != nil {
			return err
		}
		fw.log.Debug("Added private IPv4 route (%s)", network)
	}
	for _, network := range privateNetworksV6 {
		args := []string{"-6", "rule", "add", "to", network, "table", "main", "priority", strconv.Itoa(priorityLocalNet)}
		if err := exec.Command("ip", args...).Run(); err != nil {
			return err
		}
		fw.log.Debug("Added private IPv6 route (%s)", network)
	}
	return nil
}

// cleanupPrivateRoutes removes all routing rules at priority 100 (IPv4 and IPv6).
func (fw *Firewall) cleanupPrivateRoutes() {
	priority := strconv.Itoa(priorityLocalNet)
	for _, family := range [][]string{{}, {"-6"}} {
		for {
			args := append(family, "rule", "del", "priority", priority)
			if err := exec.Command("ip", args...).Run(); err != nil {
				break
			}
			fw.log.Debug("Removed routing rule at priority %d", priorityLocalNet)
		}
	}
}

const priorityPIAGuard = 76
const priorityPIA = 75

// addPIADNSRoutes adds routing rules for PIA's internal dns so it uses the VPN
// table instead of being caught by the RFC1918 bypass at priority 100.
func (fw *Firewall) AddPIADNSRoutes(dns []string) error {
	// Defensive cleanup
	fw.RemovePIARoutes()

	for _, ip := range dns {
		// Prevent fallthrough to priority 100 (LOCAL_NETWORKS) when pia0 is down
		guardArgs := []string{"rule", "add", "to", ip, "unreachable", "priority", strconv.Itoa(priorityPIAGuard)}
		if err := exec.Command("ip", guardArgs...).Run(); err != nil {
			return fmt.Errorf("failed to add pia dns blackhole route: %w", err)
		}
		fw.log.Debug("Added PIA DNS blackhole route (%s)", ip)

		// Add routes for PIA's internal DNS through tunnel
		args := []string{"rule", "add", "to", ip, "lookup", "51820", "priority", strconv.Itoa(priorityPIA)}
		if err := exec.Command("ip", args...).Run(); err != nil {
			return fmt.Errorf("failed to add pia dns route: %w", err)
		}
		fw.log.Debug("Added PIA DNS route (%s)", ip)
	}
	return nil
}

// AddPFRoute adds a routing rule for the port forward gateway so it uses the VPN
// table instead of being caught by the RFC1918 bypass at priority 100.
func (fw *Firewall) AddPFRoute(pfGateway string) error {
	args := []string{"rule", "add", "to", pfGateway, "lookup", "51820", "priority", strconv.Itoa(priorityPIA)}
	if err := exec.Command("ip", args...).Run(); err != nil {
		return err
	}
	fw.log.Debug("Added PF gateway route (%s)", pfGateway)
	return nil
}

// RemovePIARoutes removes all routes at 75 and 76 priority. Safe to call with
// portforwarding enabled when portforwarding goroutine down
func (fw *Firewall) RemovePIARoutes() {
	args := []string{"rule", "delete", "prio", strconv.Itoa(priorityPIAGuard)}
	for {
		if err := exec.Command("ip", args...).Run(); err != nil {
			break
		}
	}
	fw.log.Debug("Removed blackhole routing rules at priority %d", priorityPIAGuard)

	args = []string{"rule", "delete", "prio", strconv.Itoa(priorityPIA)}
	for {
		if err := exec.Command("ip", args...).Run(); err != nil {
			break
		}
	}
	fw.log.Debug("Removed routing rules at priority %d", priorityPIA)
}

func (fw *Firewall) RemovePFRoute(pfGateway string) {
	if pfGateway == "" {
		return
	}
	args := []string{"rule", "del", "to", pfGateway, "lookup", "51820", "priority", strconv.Itoa(priorityPIA)}
	exec.Command("ip", args...).Run()
	fw.log.Debug("Removed PF gateway route (%s)", pfGateway)
}

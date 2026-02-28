package firewall

import (
	"os/exec"
	"strconv"
	"strings"
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
		fw.log.Debug("exec: ip %s", strings.Join(args, " "))
		if err := exec.Command("ip", args...).Run(); err != nil {
			return err
		}
	}
	for _, network := range privateNetworksV6 {
		args := []string{"-6", "rule", "add", "to", network, "table", "main", "priority", strconv.Itoa(priorityLocalNet)}
		fw.log.Debug("exec: ip %s", strings.Join(args, " "))
		if err := exec.Command("ip", args...).Run(); err != nil {
			return err
		}
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
func (fw *Firewall) addPIADNSRoutes(dns string) error {
	if dns == "pia" {
		for _, ip := range []string{"10.0.0.242", "10.0.0.243"} {
			// Prevent fallthrough to priority 100 (LOCAL_NETWORKS) when pia0 is down
			guardArgs := []string{"rule", "add", "to", ip, "unreachable", "priority", strconv.Itoa(priorityPIAGuard)}
			fw.log.Debug("exec: ip %s", strings.Join(guardArgs, " "))
			if err := exec.Command("ip", guardArgs...).Run(); err != nil {
				return err
			}

			// Add routes for PIA's internal DNS through tunnel
			args := []string{"rule", "add", "to", ip, "lookup", "51820", "priority", strconv.Itoa(priorityPIA)}
			fw.log.Debug("exec: ip %s", strings.Join(args, " "))
			if err := exec.Command("ip", args...).Run(); err != nil {
				return err
			}
		}
	}
	return nil
}

// AddPFRoute adds a routing rule for the port forward gateway so it uses the VPN
// table instead of being caught by the RFC1918 bypass at priority 100.
func (fw *Firewall) AddPFRoute(pfEnabled bool, pfGateway string) error {
	if pfEnabled {
		args := []string{"rule", "add", "to", pfGateway, "lookup", "51820", "priority", strconv.Itoa(priorityPIA)}
		fw.log.Debug("exec: ip %s", strings.Join(args, " "))
		if err := exec.Command("ip", args...).Run(); err != nil {
			return err
		}
	}
	return nil
}

func (fw *Firewall) removePIADNSRoutes() {
	for _, ip := range []string{"10.0.0.242", "10.0.0.243"} {
		args := []string{"rule", "del", "to", ip, "lookup", "51820", "priority", strconv.Itoa(priorityPIA)}
		exec.Command("ip", args...).Run()
		fw.log.Debug("Removed PIA DNS route for %s", ip)

		guardArgs := []string{"rule", "del", "to", ip, "unreachable", "priority", strconv.Itoa(priorityPIAGuard)}
		exec.Command("ip", guardArgs...).Run()
		fw.log.Debug("Removed PIA DNS blackhole for %s", ip)
	}
}

func (fw *Firewall) RemovePFRoute(pfGateway string) {
	if pfGateway == "" {
		return
	}
	args := []string{"rule", "del", "to", pfGateway, "lookup", "51820", "priority", strconv.Itoa(priorityPIA)}
	exec.Command("ip", args...).Run()
	fw.log.Debug("Removed PF gateway route for %s", pfGateway)
}

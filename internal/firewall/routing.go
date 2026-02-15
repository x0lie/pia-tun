package firewall

import (
	"os/exec"
	"strconv"
	"strings"
)

const priorityLocalNet = 100

// SetupLocalNetworkRoutes adds routing rules to bypass VPN for local networks.
// Traffic to these CIDRs uses the main routing table instead of the VPN table.
func (fw *Firewall) SetupLocalNetworkRoutes(networks []string) error {
	for _, network := range networks {
		network = strings.TrimSpace(network)
		if network == "" {
			continue
		}
		args := []string{"rule", "add", "to", network, "table", "main", "priority", strconv.Itoa(priorityLocalNet)}
		if strings.Contains(network, ":") {
			args = append([]string{"-6"}, args...)
		}
		fw.log.Debug("exec: ip %s", strings.Join(args, " "))
		if err := exec.Command("ip", args...).Run(); err != nil {
			return err
		}
	}
	return nil
}

// CleanupLocalNetworkRoutes removes all routing rules at priority 100 (IPv4 and IPv6).
func (fw *Firewall) CleanupLocalNetworkRoutes() {
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

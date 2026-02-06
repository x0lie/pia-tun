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
		fw.log.Debug("exec: ip %s", strings.Join(args, " "))
		if err := exec.Command("ip", args...).Run(); err != nil {
			return err
		}
	}
	return nil
}

// CleanupLocalNetworkRoutes removes all routing rules at priority 100.
func (fw *Firewall) CleanupLocalNetworkRoutes() {
	priority := strconv.Itoa(priorityLocalNet)
	for {
		if err := exec.Command("ip", "rule", "del", "priority", priority).Run(); err != nil {
			break
		}
		fw.log.Debug("Removed routing rule at priority %d", priorityLocalNet)
	}
}

package firewall

import (
	"fmt"
	"strconv"
)

const chainIn = "VPN_IN"

var portForwardComments = []string{"port_forward_tcp", "port_forward_udp"}

// AllowForwardedPort inserts TCP and UDP ACCEPT rules for the given port
// into VPN_IN, just before the terminal DROP rule. Removes any existing
// forwarded port rules first.
func (fw *Firewall) AllowForwardedPort(port int) error {
	fw.RemoveForwardedPort()

	pos, err := fw.ruleCount(chainIn)
	if err != nil {
		return fmt.Errorf("find DROP position in %s: %w", chainIn, err)
	}

	portStr := strconv.Itoa(port)

	for _, proto := range []string{"tcp", "udp"} {
		comment := "port_forward_" + proto
		spec := []string{
			"-i", "pia0", "-p", proto, "--dport", portStr,
			"-j", "ACCEPT",
			"-m", "comment", "--comment", comment,
		}
		if err := fw.ipt4.Insert(tableFilter, chainIn, pos, spec...); err != nil {
			return fmt.Errorf("insert port forward %s rule: %w", proto, err)
		}
	}

	fw.log.Debug("Port forwarding firewall rules added: %d (TCP+UDP)", port)
	return nil
}

// RemoveForwardedPort removes all port forwarding rules from VPN_IN.
func (fw *Firewall) RemoveForwardedPort() {
	fw.removeVPNRulesByComment(fw.backend, chainIn, portForwardComments)
}

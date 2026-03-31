package firewall

import "github.com/x0lie/pia-tun/internal/log"

// mssRuleSpec is the common rule spec for TCP MSS clamping.
var mssRuleSpec = []string{"-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"}

const tableMangle = "mangle"

// setupMSSClamping adds TCP MSS clamping rules to the mangle table for both
// FORWARD (other containers routing through us) and OUTPUT (this container).
func (fw *Firewall) setupMSSClamping(ipv6Enabled bool) {
	fw.cleanupMSSClamping()
	fw.log.Debug("Setting up TCP MSS clamping for VPN tunnel")

	ipv4Ok := false
	if err := fw.ipt4.Append(tableMangle, "FORWARD", mssRuleSpec...); err == nil {
		fw.log.Debug("IPv4 FORWARD MSS clamping enabled")
		ipv4Ok = true
	} else {
		fw.log.Debug("IPv4 FORWARD MSS clamping failed (TCPMSS target may not be available)")
	}

	if err := fw.ipt4.Append(tableMangle, "OUTPUT", mssRuleSpec...); err == nil {
		fw.log.Debug("IPv4 OUTPUT MSS clamping enabled")
		ipv4Ok = true
	} else {
		fw.log.Debug("IPv4 OUTPUT MSS clamping failed (TCPMSS target may not be available)")
	}

	ipv6Ok := false
	if ipv6Enabled {
		if err := fw.ipt6.Append(tableMangle, "FORWARD", mssRuleSpec...); err == nil {
			ipv6Ok = true
		}
		if err := fw.ipt6.Append(tableMangle, "OUTPUT", mssRuleSpec...); err == nil {
			ipv6Ok = true
		}
		if ipv6Ok {
			fw.log.Debug("IPv6 MSS clamping enabled")
		}
	}

	if ipv4Ok || ipv6Ok {
		log.Success("TCP MSS clamping enabled")
	} else {
		fw.log.Debug("TCP MSS clamping unavailable (kernel may lack xt_TCPMSS module)")
	}
}

// cleanupMSSClamping removes TCP MSS clamping rules from the mangle table.
func (fw *Firewall) cleanupMSSClamping() {
	fw.log.Debug("Cleaning up TCP MSS clamping rules")

	fw.ipt4.Delete(tableMangle, "FORWARD", mssRuleSpec...)
	fw.ipt4.Delete(tableMangle, "OUTPUT", mssRuleSpec...)
	fw.ipt6.Delete(tableMangle, "FORWARD", mssRuleSpec...)
	fw.ipt6.Delete(tableMangle, "OUTPUT", mssRuleSpec...)
}

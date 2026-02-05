package firewall

import "fmt"

const (
	chainOut6 = "VPN_OUT6"

	// vpnInsertPos is the position in VPN_OUT / VPN_OUT6 where VPN rules are
	// inserted: after established/related (1) and loopback (2).
	vpnInsertPos = 3
)

// AddVPN inserts VPN interface rules into the killswitch. If fwmark is non-empty
// and not "off", a fwmark-based rule is also inserted. Both rule types are stored
// for exact removal by RemoveVPN.
func (fw *Firewall) AddVPN(fwmark string, ipv6Enabled bool) error {
	ifaceSpec := []string{"-o", "pia0", "-j", "ACCEPT", "-m", "comment", "--comment", "vpn_interface"}
	if err := fw.ipt4.Insert(tableFilter, chainOut, vpnInsertPos, ifaceSpec...); err != nil {
		return fmt.Errorf("insert VPN interface rule: %w", err)
	}
	fw.vpnRules4 = append(fw.vpnRules4, ifaceSpec)

	if fwmark != "" && fwmark != "off" {
		fwmarkSpec := []string{"-m", "mark", "--mark", fwmark, "-j", "ACCEPT", "-m", "comment", "--comment", "vpn_fwmark"}
		if err := fw.ipt4.Insert(tableFilter, chainOut, vpnInsertPos, fwmarkSpec...); err != nil {
			return fmt.Errorf("insert VPN fwmark rule: %w", err)
		}
		fw.vpnRules4 = append(fw.vpnRules4, fwmarkSpec)
		fw.log.Debug("VPN added to killswitch (fwmark: %s)", fwmark)
	} else {
		fw.log.Debug("VPN added to killswitch (interface-based)")
	}

	if ipv6Enabled {
		ifaceSpec6 := []string{"-o", "pia0", "-j", "ACCEPT"}
		if err := fw.ipt6.Insert(tableFilter, chainOut6, vpnInsertPos, ifaceSpec6...); err != nil {
			return fmt.Errorf("insert IPv6 VPN interface rule: %w", err)
		}
		fw.vpnRules6 = append(fw.vpnRules6, ifaceSpec6)

		icmpSpec6 := []string{"-p", "ipv6-icmp", "-j", "ACCEPT"}
		if err := fw.ipt6.Insert(tableFilter, chainOut6, vpnInsertPos, icmpSpec6...); err != nil {
			return fmt.Errorf("insert IPv6 ICMPv6 rule: %w", err)
		}
		fw.vpnRules6 = append(fw.vpnRules6, icmpSpec6)
	}

	return fw.verifyVPN()
}

// RemoveVPN deletes the VPN rules that were added by AddVPN.
// Called before WireGuard teardown to prevent leak windows.
func (fw *Firewall) RemoveVPN() {
	for _, spec := range fw.vpnRules4 {
		if err := fw.ipt4.Delete(tableFilter, chainOut, spec...); err != nil {
			fw.log.Debug("Failed to remove IPv4 VPN rule: %v", err)
		}
	}
	fw.vpnRules4 = nil

	for _, spec := range fw.vpnRules6 {
		if err := fw.ipt6.Delete(tableFilter, chainOut6, spec...); err != nil {
			fw.log.Debug("Failed to remove IPv6 VPN rule: %v", err)
		}
	}
	fw.vpnRules6 = nil
}

// verifyVPN checks that all stored IPv4 VPN rules exist in the chain.
func (fw *Firewall) verifyVPN() error {
	for _, spec := range fw.vpnRules4 {
		exists, err := fw.ipt4.Exists(tableFilter, chainOut, spec...)
		if err != nil {
			return fmt.Errorf("verify VPN: %w", err)
		}
		if !exists {
			return fmt.Errorf("verify VPN: rule not found in %s after insert", chainOut)
		}
	}
	return nil
}

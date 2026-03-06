package firewall

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	chainOut6 = "VPN_OUT6"

	// vpnInsertPos is the position in VPN_OUT / VPN_OUT6 where VPN rules are
	// inserted: after established/related (1) and loopback (2).
	vpnInsertPos = 3
)

// vpnComments are the comment markers used to identify VPN rules.
var vpnComments = []string{"vpn_interface", "vpn_fwmark"}

// AddVPN inserts VPN interface rules into the killswitch. If fwmark is non-empty
// and not "off", a fwmark-based rule is also inserted.
func (fw *Firewall) AddVPN(fwmark string, ipv6Enabled bool) error {
	// Clean up any stale VPN rules first (handles unclean shutdown, reconnect edge cases)
	fw.removeVPN()

	ifaceSpec := []string{"-o", "pia0", "-m", "comment", "--comment", "vpn_interface", "-j", "ACCEPT"}
	if err := fw.ipt4.Insert(tableFilter, chainOut, vpnInsertPos, ifaceSpec...); err != nil {
		return fmt.Errorf("insert VPN interface rule: %w", err)
	}

	if fwmark != "" && fwmark != "off" {
		fwmarkSpec := []string{"-m", "mark", "--mark", fwmark, "-m", "comment", "--comment", "vpn_fwmark", "-j", "ACCEPT"}
		if err := fw.ipt4.Insert(tableFilter, chainOut, vpnInsertPos, fwmarkSpec...); err != nil {
			return fmt.Errorf("insert VPN fwmark rule: %w", err)
		}
		fw.log.Debug("VPN added to killswitch (fwmark: %s)", fwmark)
	} else {
		fw.log.Debug("VPN added to killswitch (interface-based)")
	}

	if ipv6Enabled {
		ifaceSpec6 := []string{"-o", "pia0", "-m", "comment", "--comment", "vpn_interface", "-j", "ACCEPT"}
		if err := fw.ipt6.Insert(tableFilter, chainOut6, vpnInsertPos, ifaceSpec6...); err != nil {
			return fmt.Errorf("insert IPv6 VPN interface rule: %w", err)
		}
	}

	return nil
}

// removeVPN deletes VPN rules by finding them by comment and deleting by line number.
// This approach is more reliable than spec-based deletion with iptables-nft.
func (fw *Firewall) removeVPN() {
	fw.removeVPNRulesByComment(fw.ipt4Cmd, chainOut, vpnComments)

	// For IPv6, look for pia0 interface rules (no comment on these)
	fw.removeIPv6VPNRules()
}

// removeVPNRulesByComment finds rules containing any of the given comments
// and deletes them by line number (highest first to avoid index shifting).
func (fw *Firewall) removeVPNRulesByComment(iptables, chain string, comments []string) {
	lineNumbers := fw.findRuleLineNumbers(iptables, chain, comments)
	if len(lineNumbers) == 0 {
		return
	}

	// Sort descending so we delete from bottom up (avoids line number shifting)
	sort.Sort(sort.Reverse(sort.IntSlice(lineNumbers)))

	for _, lineNum := range lineNumbers {
		// Use direct exec for deletion by line number - more reliable than go-iptables
		cmd := exec.Command(iptables, "-t", "filter", "-D", chain, strconv.Itoa(lineNum))
		if err := cmd.Run(); err != nil {
			fw.log.Debug("Failed to remove rule at line %d: %v", lineNum, err)
		}
	}
}

// findRuleLineNumbers lists the chain and returns line numbers of rules containing any comment.
func (fw *Firewall) findRuleLineNumbers(iptables, chain string, comments []string) []int {
	cmd := exec.Command(iptables, "-t", "filter", "-L", chain, "--line-numbers", "-n")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var lineNumbers []int
	// Regex to match line number at start: "1    ACCEPT..."
	lineNumRegex := regexp.MustCompile(`^(\d+)\s+`)

	for _, line := range strings.Split(string(output), "\n") {
		for _, comment := range comments {
			if strings.Contains(line, comment) {
				matches := lineNumRegex.FindStringSubmatch(line)
				if len(matches) >= 2 {
					if num, err := strconv.Atoi(matches[1]); err == nil {
						lineNumbers = append(lineNumbers, num)
					}
				}
				break
			}
		}
	}

	return lineNumbers
}

// removeIPv6VPNRules removes IPv6 VPN rules by comment.
func (fw *Firewall) removeIPv6VPNRules() {
	fw.removeVPNRulesByComment(fw.ipt6Cmd, chainOut6, []string{"vpn_interface"})
}

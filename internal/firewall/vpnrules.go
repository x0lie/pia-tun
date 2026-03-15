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
	fwmark        = "51820"
	vpnInsertPos  = 3
	portInsertPos = 3
	tableFilter   = "filter"
	chainExempt   = "VPN_EXEMPTIONS"
)

var portForwardComments = []string{"port_forward_tcp", "port_forward_udp"}

type Exemption struct {
	IP, Port, Proto, Comment string
}

func (fw *Firewall) createExemptChain() error {
	// Create VPN_EXEMPTIONS chain
	if err := fw.ipt4.NewChain(tableFilter, chainExempt); err != nil {
		return fmt.Errorf("create %s: %w", chainExempt, err)
	}

	// Wire into parent VPN_OUT
	if err := fw.ipt4.Insert(tableFilter, chainOut, 1, "-j", chainExempt); err != nil {
		fw.log.Debug("Failed to insert %s into %s: %s", chainExempt, chainOut, err)
	}
	return nil
}

func (fw *Firewall) AddExemption(ip, port, proto, comment string) error {
	fw.log.Debug("Adding exemption: %s:%s/%s (%s)", ip, port, proto, comment)

	if err := fw.createExemptChain(); err != nil {
		return err
	}

	// Append Rule to chain
	if err := fw.ipt4.Append(tableFilter, chainExempt,
		"-d", ip, "-p", proto, "--dport", port,
		"-m", "comment", "--comment", comment,
		"-j", "ACCEPT",
	); err != nil {
		return fmt.Errorf("%s: append exemption: %w", chainExempt, err)
	}

	return nil
}

// Batching version of AddExemption
func (fw *Firewall) AddExemptions(specs ...Exemption) error {
	if err := fw.createExemptChain(); err != nil {
		return err
	}

	var comments []string
	for _, s := range specs {
		if err := fw.ipt4.Append(tableFilter, chainExempt,
			"-d", s.IP, "-p", s.Proto, "--dport", s.Port,
			"-m", "comment", "--comment", s.Comment,
			"-j", "ACCEPT",
		); err != nil {
			fw.log.Debug("Failed to add exemption %s: %v", s.Comment, err)
		} else {
			comments = append(comments, s.Comment)
		}
	}
	if len(comments) > 10 {
		fw.log.Debug("Added %d exemptions", len(comments))
	} else {
		fw.log.Debug("Added %d exemptions: %v", len(comments), comments)
	}
	return nil
}

func (fw *Firewall) RemoveExemptions() {
	fw.log.Debug("Removing exemptions")
	fw.ipt4.Delete(tableFilter, chainOut, "-j", chainExempt)
	fw.ipt4.ClearAndDeleteChain(tableFilter, chainExempt)
}

// addVPN inserts VPN interface rules into the killswitch.
func (fw *Firewall) addVPN(ipv6Enabled bool) error {
	ifaceComment := "vpn_interface"
	fwmarkComment := "vpn_fwmark"

	fwmarkSpec := []string{"-m", "mark", "--mark", fwmark, "-m", "comment", "--comment", fwmarkComment, "-j", "ACCEPT"}
	if err := fw.ipt4.Insert(tableFilter, chainOut, vpnInsertPos, fwmarkSpec...); err != nil {
		return fmt.Errorf("insert VPN interface rule: %w", err)
	}
	fw.log.Debug("fwmark ACCEPT added to %s", chainOut)

	ifaceSpec := []string{"-o", "pia0", "-m", "comment", "--comment", ifaceComment, "-j", "ACCEPT"}
	if err := fw.ipt4.Insert(tableFilter, chainOut, vpnInsertPos, ifaceSpec...); err != nil {
		return fmt.Errorf("insert VPN interface rule: %w", err)
	}
	fw.log.Debug("pia0 ACCEPT added to %s", chainOut)

	if ipv6Enabled {
		ifaceSpec6 := []string{"-o", "pia0", "-m", "comment", "--comment", ifaceComment, "-j", "ACCEPT"}
		if err := fw.ipt6.Insert(tableFilter, chainOut6, vpnInsertPos, ifaceSpec6...); err != nil {
			return fmt.Errorf("insert IPv6 VPN interface rule: %w", err)
		}
		fw.log.Debug("pia0 ACCEPT added to %s", chainOut6)
	}

	return nil
}

// AllowForwardedPort inserts TCP and UDP ACCEPT rules for the given port
func (fw *Firewall) AllowForwardedPort(port int) error {
	fw.RemoveForwardedPort()

	portStr := strconv.Itoa(port)

	for _, proto := range []string{"tcp", "udp"} {
		comment := "port_forward_" + proto
		spec := []string{
			"-i", "pia0", "-p", proto, "--dport", portStr,
			"-j", "ACCEPT",
			"-m", "comment", "--comment", comment,
		}
		if err := fw.ipt4.Insert(tableFilter, chainIn, portInsertPos, spec...); err != nil {
			return fmt.Errorf("insert port forward rule: %w", err)
		}
	}

	fw.log.Debug("Port forwarding rules added for %d (TCP+UDP)", port)
	return nil
}

// RemoveForwardedPort removes all port forwarding rules from VPN_IN.
func (fw *Firewall) RemoveForwardedPort() {
	fw.removeVPNRulesByComment(fw.ipt4Cmd, chainIn, portForwardComments)
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

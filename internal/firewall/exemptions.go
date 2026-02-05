package firewall

import (
	"fmt"
	"strings"
)

const (
	tableFilter = "filter"
	chainOut    = "VPN_OUT"
)

// Exemption is a handle to a temporary firewall rule that was inserted into VPN_OUT.
// Pass it to RemoveTemporaryExemption to delete the exact rule that was added.
type Exemption struct {
	rulespec []string
}

// AddTemporaryExemption inserts a firewall rule before the terminal DROP rule in
// VPN_OUT, allowing outbound traffic to ip:port/proto. Returns an Exemption handle
// for removal.
func (fw *Firewall) AddTemporaryExemption(ip, port, proto, comment string) (*Exemption, error) {
	fw.log.Debug("Adding temporary exemption: %s:%s/%s (%s)", ip, port, proto, comment)

	pos, err := fw.ruleCount(chainOut)
	if err != nil {
		return nil, err
	}
	// DROP is always the last rule; insert at its position to push it down.

	spec := []string{
		"-d", ip, "-p", proto, "--dport", port,
		"-j", "ACCEPT",
		"-m", "comment", "--comment", comment,
	}

	if err := fw.ipt4.Insert(tableFilter, chainOut, pos, spec...); err != nil {
		return nil, fmt.Errorf("insert exemption %s: %w", comment, err)
	}

	return &Exemption{rulespec: spec}, nil
}

// RemoveTemporaryExemption deletes the exact rule that was previously added.
func (fw *Firewall) RemoveTemporaryExemption(e *Exemption) error {
	if e == nil {
		return nil
	}
	fw.log.Debug("Removing temporary exemption: %v", e.rulespec)
	return fw.ipt4.Delete(tableFilter, chainOut, e.rulespec...)
}

// ruleCount returns the number of rules (1-based) in chain, which equals the
// position of the last rule. List() returns a -N header line followed by -A lines.
func (fw *Firewall) ruleCount(chain string) (int, error) {
	rules, err := fw.ipt4.List(tableFilter, chain)
	if err != nil {
		return 0, fmt.Errorf("list %s rules: %w", chain, err)
	}

	count := 0
	for _, rule := range rules {
		if strings.HasPrefix(rule, "-A ") {
			count++
		}
	}

	if count == 0 {
		return 0, fmt.Errorf("chain %s has no rules", chain)
	}

	return count, nil
}

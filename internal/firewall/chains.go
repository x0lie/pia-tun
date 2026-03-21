package firewall

import (
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-iptables/iptables"
)

const (
	chainIn   = "VPN_IN"
	chainOut  = "VPN_OUT"
	chainIn6  = "VPN_IN6"
	chainOut6 = "VPN_OUT6"
)

type chainDef struct {
	name   string
	parent string
	ipt    *iptables.IPTables
	loFlag string
}

func (fw *Firewall) chainDefs() []chainDef {
	return []chainDef{
		{chainIn, "INPUT", fw.ipt4, "-i"},
		{chainOut, "OUTPUT", fw.ipt4, "-o"},
		{chainIn6, "INPUT", fw.ipt6, "-i"},
		{chainOut6, "OUTPUT", fw.ipt6, "-o"},
	}
}

func (fw *Firewall) setupBaselineChains() error {
	fw.cleanupChains()

	for _, c := range fw.chainDefs() {
		if err := c.ipt.NewChain(tableFilter, c.name); err != nil {
			return fmt.Errorf("create %s: %w", c.name, err)
		}

		// Baseline: established/related → loopback → DROP
		if err := c.ipt.Append(tableFilter, c.name,
			"-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"); err != nil {
			return fmt.Errorf("%s: append established/related: %w", c.name, err)
		}
		if err := c.ipt.Append(tableFilter, c.name,
			c.loFlag, "lo", "-j", "ACCEPT"); err != nil {
			return fmt.Errorf("%s: append loopback: %w", c.name, err)
		}
		if err := c.ipt.Append(tableFilter, c.name, "-j", "DROP"); err != nil {
			return fmt.Errorf("%s: append DROP: %w", c.name, err)
		}

		// Wire into parent — all traffic now flows through our chain
		if err := c.ipt.Insert(tableFilter, c.parent, 1, "-j", c.name); err != nil {
			return fmt.Errorf("insert %s into %s: %w", c.name, c.parent, err)
		}
		fw.log.Debug("DROP rule established for %s", c.name)
	}

	return nil
}

// insertBeforeDrop inserts a rule just before the terminal DROP rule in a chain.
func (fw *Firewall) insertBeforeDrop(ipt *iptables.IPTables, chain string, spec ...string) error {
	pos, err := ruleCount(ipt, chain)
	if err != nil {
		return err
	}
	return ipt.Insert(tableFilter, chain, pos, spec...)
}

// ruleCount returns the number of rules (1-based) in chain, which equals the
// position of the last rule (DROP). Inserting at this position pushes DROP down.
func ruleCount(ipt *iptables.IPTables, chain string) (int, error) {
	rules, err := ipt.List(tableFilter, chain)
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

// cleanupChains removes VPN_IN/VPN_OUT/VPN_IN6/VPN_OUT6 from INPUT/OUTPUT,
// then flushes and deletes them.
func (fw *Firewall) cleanupChains() {
	fw.log.Debug("Cleaning up iptables configuration")

	for _, c := range fw.chainDefs() {
		c.ipt.Delete(tableFilter, c.parent, "-j", c.name)
		c.ipt.ClearAndDeleteChain(tableFilter, c.name)
	}
}

// verifyKillswitch checks that VPN_OUT has a DROP rule and is wired into OUTPUT,
// and the same for VPN_OUT6. Retries up to 3 times with 300ms between attempts.
func (fw *Firewall) verifyKillswitch() error {
	fw.log.Debug("Verifying baseline killswitch is active")

	for attempt := 1; attempt <= 4; attempt++ {
		if fw.checkChainsPresent() {
			fw.log.Debug("Baseline killswitch verification passed")
			return nil
		}
		if attempt < 4 {
			time.Sleep(250 * time.Millisecond)
		}
	}

	return fmt.Errorf("killswitch verification failed — this is a critical security issue")
}

// checkChainsPresent verifies all 4 chains have DROP rules and are wired into their parents.
func (fw *Firewall) checkChainsPresent() bool {
	return chainHasDrop(fw.ipt4, chainIn) &&
		chainIsInParent(fw.ipt4, "INPUT", chainIn) &&
		chainHasDrop(fw.ipt4, chainOut) &&
		chainIsInParent(fw.ipt4, "OUTPUT", chainOut) &&
		chainHasDrop(fw.ipt6, chainIn6) &&
		chainIsInParent(fw.ipt6, "INPUT", chainIn6) &&
		chainHasDrop(fw.ipt6, chainOut6) &&
		chainIsInParent(fw.ipt6, "OUTPUT", chainOut6)
}

// chainHasDrop checks if the chain contains a DROP rule using go-iptables List().
func chainHasDrop(ipt *iptables.IPTables, chain string) bool {
	rules, err := ipt.List(tableFilter, chain)
	if err != nil {
		return false
	}
	for _, rule := range rules {
		if strings.HasSuffix(rule, "-j DROP") {
			return true
		}
	}
	return false
}

// chainIsInParent checks if the parent chain contains a jump to the child chain.
func chainIsInParent(ipt *iptables.IPTables, parent, child string) bool {
	rules, err := ipt.List(tableFilter, parent)
	if err != nil {
		return false
	}
	target := "-j " + child
	for _, rule := range rules {
		if strings.Contains(rule, target) {
			return true
		}
	}
	return false
}

// formatNetworks joins v4 and v6 network slices into a comma-separated string.
func formatNetworks(v4, v6 []string) string {
	all := make([]string, 0, len(v4)+len(v6))
	all = append(all, v4...)
	all = append(all, v6...)
	return strings.Join(all, ", ")
}

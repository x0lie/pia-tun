package firewall

import "fmt"

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

	spec := []string{
		"-d", ip, "-p", proto, "--dport", port,
		"-j", "ACCEPT",
		"-m", "comment", "--comment", comment,
	}

	if err := fw.insertBeforeDrop(fw.ipt4, chainOut, spec...); err != nil {
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

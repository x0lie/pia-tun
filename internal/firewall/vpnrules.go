package firewall

import (
	"fmt"
	"strconv"
)

const (
	fwmark        = "51820"
	vpnInsertPos  = 3
	portInsertPos = 3
	tableFilter   = "filter"
	chainExempt   = "VPN_EXEMPTIONS"
)

type Exemption struct {
	IP, Port, Proto, Comment string
}

func (fw *Firewall) createExemptChain() error {
	// Create VPN_EXEMPTIONS chain
	if err := fw.ipt4.NewChain(tableFilter, chainExempt); err != nil {
		return fmt.Errorf("create %s: %w", chainExempt, err)
	}

	// Wire into parent VPN_OUT
	if err := fw.ipt4.Insert(tableFilter, ChainOut, 1, "-j", chainExempt); err != nil {
		fw.log.Debug("Failed to insert %s into %s: %s", chainExempt, ChainOut, err)
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
	fw.ipt4.Delete(tableFilter, ChainOut, "-j", chainExempt)
	fw.ipt4.ClearAndDeleteChain(tableFilter, chainExempt)
}

// addVPN inserts VPN interface rules into the killswitch.
func (fw *Firewall) addVPN(ipv6Enabled bool) error {
	fwmarkSpec := []string{"-m", "mark", "--mark", fwmark, "-j", "ACCEPT"}
	if err := fw.ipt4.Insert(tableFilter, ChainOut, vpnInsertPos, fwmarkSpec...); err != nil {
		return fmt.Errorf("insert VPN interface rule: %w", err)
	}
	fw.log.Debug("fwmark ACCEPT added to %s", ChainOut)

	ifaceSpec := []string{"-o", "pia0", "-j", "ACCEPT"}
	if err := fw.ipt4.Insert(tableFilter, ChainOut, vpnInsertPos, ifaceSpec...); err != nil {
		return fmt.Errorf("insert VPN interface rule: %w", err)
	}
	fw.log.Debug("pia0 ACCEPT added to %s", ChainOut)

	if ipv6Enabled {
		ifaceSpec6 := []string{"-o", "pia0", "-j", "ACCEPT"}
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
		spec := []string{
			"-i", "pia0", "-p", proto, "--dport", portStr,
			"-j", "ACCEPT",
		}
		if err := fw.ipt4.Insert(tableFilter, ChainIn, portInsertPos, spec...); err != nil {
			return fmt.Errorf("insert port forward rule: %w", err)
		}
	}
	fw.activePort = port

	fw.log.Debug("Port forwarding rules added for %d (TCP+UDP)", port)
	return nil
}

// RemoveForwardedPort removes all port forwarding rules from VPN_IN.
func (fw *Firewall) RemoveForwardedPort() {
	if fw.activePort == 0 {
		return
	}
	portStr := strconv.Itoa(fw.activePort)
	fw.ipt4.Delete(tableFilter, ChainIn, "-i", "pia0", "-p", "tcp", "--dport", portStr, "-j", "ACCEPT")
	fw.ipt4.Delete(tableFilter, ChainIn, "-i", "pia0", "-p", "udp", "--dport", portStr, "-j", "ACCEPT")
}

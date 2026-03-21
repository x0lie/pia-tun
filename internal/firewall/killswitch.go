package firewall

import (
	"fmt"

	"github.com/x0lie/pia-tun/internal/log"
)

// KillswitchConfig holds parameters for killswitch setup.
type KillswitchConfig struct {
	LANs        string
	IPv6Enabled bool
}

// Setup applies the baseline killswitch: resolves local networks, sets up bypass
// routes, creates iptables chains, verifies they're active, and enables MSS clamping.
func (fw *Firewall) Setup(cfg KillswitchConfig) error {
	log.Step("Applying killswitch...")

	// Establish baseline DROP for ipv4 and ipv6
	if err := fw.setupBaselineChains(); err != nil {
		return err
	}
	log.Success(fmt.Sprintf("Baseline established (%s)", fw.ipt4Cmd))

	// Set up LOCAL_NETWORKS routes and rules if LOCAL_NETWORKS != "none"
	if cfg.LANs != "none" {
		lans, err := fw.setupLocalNetworks(cfg.LANs)
		if err != nil {
			return fmt.Errorf("failed to setup local networks: %w", err)
		}
		log.Success(fmt.Sprintf("Local networks: %s", lans))
	}

	// Add VPN to killswitch - Must be present for handshake initiation packet
	if err := fw.addVPN(cfg.IPv6Enabled); err != nil {
		return fmt.Errorf("failed to add VPN rules: %w", err)
	}
	log.Success(fmt.Sprintf("VPN allowed (fwmark %s)", fwmark))

	// Setup daytime NIST bypass for WAN checking
	if err := fw.setupBypass(); err != nil {
		return fmt.Errorf("failed to set up wan-check bypass: %w", err)
	}

	// Set up IPv6 tunnel traffic if enabled
	if cfg.IPv6Enabled {
		fw.log.Debug("Adding ICMPv6 rules to VPN_IN6 and VPN_OUT6")
		if err := fw.insertBeforeDrop(fw.ipt6, chainIn6, "-p", "ipv6-icmp", "-j", "ACCEPT"); err != nil {
			return fmt.Errorf("VPN_IN6 ICMPv6: %w", err)
		}
		if err := fw.insertBeforeDrop(fw.ipt6, chainOut6, "-p", "ipv6-icmp", "-j", "ACCEPT"); err != nil {
			return fmt.Errorf("VPN_OUT6 ICMPv6: %w", err)
		}
		log.Success("IPv6 through tunnel enabled")
	}

	// Setup MSS Clamping to reduce fragmentation
	fw.setupMSSClamping(cfg.IPv6Enabled)

	// Verify Killswitch setup
	if err := fw.verifyKillswitch(); err != nil {
		return err
	}
	fw.active = true

	return nil
}

// Cleanup removes all killswitch chains, bypass routes, and MSS clamping rules.
func (fw *Firewall) Cleanup() {
	fw.active = false
	fw.RemovePIARoutes()
	fw.cleanupLocalRoutes()
	fw.cleanupMSSClamping()
	fw.cleanupBypassRoutes()
	fw.RemoveExemptions()
	fw.cleanupChains()
}

// IsActive returns whether the killswitch is currently set up.
func (fw *Firewall) IsActive() bool {
	return fw.active
}

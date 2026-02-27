package firewall

import (
	"fmt"
	"os/exec"
	"strings"

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
	fw.mu.Lock()
	defer fw.mu.Unlock()

	// Defensive cleanup of orphaned rules from previous runs
	fw.log.Debug("Cleaning up any orphaned killswitch rules from previous runs")
	fw.cleanupMSSClamping()
	fw.cleanupBypassRoutes()
	fw.cleanupChains()

	// Establish baseline DROP for ipv4 and ipv6
	if err := fw.setupBaselineChains(); err != nil {
		return err
	}
	log.Success(fmt.Sprintf("Baseline established (%s)", fw.ipt4Cmd))
	fw.active = true

	// Resolve LOCAL_NETWORKS keywords into CIDRs and add them to chains if != "none"
	if cfg.LANs != "none" {
		fw.localNetworksV4, fw.localNetworksV6 = resolveLocalNetworks(cfg.LANs)
		fw.log.Debug("Local networks v4=%v v6=%v", fw.localNetworksV4, fw.localNetworksV6)
		if err := fw.setupLocalNetworks(); err != nil {
			return fmt.Errorf("failed to setup input chain: %w", err)
		}
	}

	// Get default gateway and interface
	gateway, err := getDefaultGateway()
	if err != nil {
		return err
	}
	iface, err := getDefaultInterface()
	if err != nil {
		return err
	}
	fw.log.Debug("Default gateway: %s, interface: %s", gateway, iface)

	if err := fw.setupBypassRoutes(gateway, iface); err != nil {
		return fmt.Errorf("failed to setup bypass routes: %w", err)
	}

	// Insert bypass routes before DROP
	if err := fw.insertBypassFirewallRules(fw.ipt4, chainOut, iface); err != nil {
		return fmt.Errorf("insert bypass rules: %w", err)
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

	fw.setupMSSClamping(cfg.IPv6Enabled)

	if err := fw.verifyKillswitch(); err != nil {
		return err
	}
	log.Success("DROP Rule on all VPN chains")

	return nil
}

// Cleanup removes all killswitch chains, bypass routes, and MSS clamping rules.
func (fw *Firewall) Cleanup() {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	fw.log.Debug("Full killswitch cleanup")
	fw.cleanupMSSClamping()
	fw.cleanupBypassRoutes()
	fw.cleanupChains()
	fw.active = false
}

// IsActive returns whether the killswitch is currently set up.
func (fw *Firewall) IsActive() bool {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return fw.active
}

// getDefaultGateway returns the default gateway IP from the routing table.
func getDefaultGateway() (string, error) {
	out, err := exec.Command("ip", "route").Output()
	if err != nil {
		return "", fmt.Errorf("ip route: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "default") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				return fields[2], nil
			}
		}
	}
	return "", fmt.Errorf("cannot determine default gateway")
}

// getDefaultInterface returns the default network interface from the routing table.
func getDefaultInterface() (string, error) {
	out, err := exec.Command("ip", "route").Output()
	if err != nil {
		return "", fmt.Errorf("ip route: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "default") {
			fields := strings.Fields(line)
			if len(fields) >= 5 {
				return fields[4], nil
			}
		}
	}
	return "", fmt.Errorf("cannot determine default interface")
}

package firewall

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/coreos/go-iptables/iptables"
	"github.com/x0lie/pia-tun/internal/log"
)

// Firewall manages iptables rules for VPN killswitch and temporary exemptions.
type Firewall struct {
	Ipt4Cmd string
	Ipt6Cmd string
	ipt4    *iptables.IPTables
	ipt6    *iptables.IPTables
	log     *log.Logger

	// Killswitch state (protected by mu)
	mu              sync.Mutex
	active          bool
	localNetworksV4 []string
	localNetworksV6 []string
}

// New creates a Firewall with auto-detected or manually specified iptables backend.
// Respects the IPT_BACKEND environment variable ("legacy" or "nft").
func New(backend string) (*Firewall, error) {
	logger := log.New("firewall")
	ipt4Cmd, ipt6Cmd := detectBackend(backend, logger)

	ipt4, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4), iptables.Path(ipt4Cmd))
	if err != nil {
		return nil, fmt.Errorf("init iptables IPv4 (%s): %w", ipt4Cmd, err)
	}

	ipt6, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv6), iptables.Path(ipt6Cmd))
	if err != nil {
		return nil, fmt.Errorf("init iptables IPv6 (%s): %w", ipt6Cmd, err)
	}

	return &Firewall{ipt4: ipt4, ipt6: ipt6, log: logger, Ipt4Cmd: ipt4Cmd, Ipt6Cmd: ipt6Cmd}, nil
}

// Backend returns the detected backend name (e.g. "iptables-nft" or "iptables-legacy").
func (fw *Firewall) Backend() string {
	return fw.Ipt4Cmd
}

// detectBackend determines the iptables backend to use. It checks IPT_BACKEND
// first, then auto-detects by trying iptables-nft and checking for warnings
// that indicate legacy tables are present (e.g. from Docker or the host).
func detectBackend(backend string, logger *log.Logger) (ipt4, ipt6 string) {
	switch backend {
	case "legacy":
		logger.Debug("IPT_BACKEND=legacy, using iptables-legacy")
		return "iptables-legacy", "ip6tables-legacy"
	case "nft":
		logger.Debug("IPT_BACKEND=nft, using iptables-nft")
		return "iptables-nft", "ip6tables-nft"
	}

	// Auto-detect: try nft first, check for warnings indicating legacy is needed.
	output, err := exec.Command("iptables-nft", "-L", "-n").CombinedOutput()
	if err != nil {
		logger.Debug("iptables-nft failed (exit %v), using legacy", err)
		return "iptables-legacy", "ip6tables-legacy"
	}

	if strings.Contains(strings.ToLower(string(output)), "iptables-legacy") {
		logger.Debug("iptables-nft detected legacy tables, using legacy")
		return "iptables-legacy", "ip6tables-legacy"
	}

	logger.Debug("iptables-nft works cleanly, using nft")
	return "iptables-nft", "ip6tables-nft"
}

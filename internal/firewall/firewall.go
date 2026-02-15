package firewall

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/coreos/go-iptables/iptables"
	"github.com/x0lie/pia-tun/internal/log"
)

// Firewall manages iptables rules for VPN killswitch and temporary exemptions.
type Firewall struct {
	ipt4    *iptables.IPTables
	ipt6    *iptables.IPTables
	log     *log.Logger
	backend string
}

// New creates a Firewall with auto-detected or manually specified iptables backend.
// Respects the IPT_BACKEND environment variable ("legacy" or "nft").
func New() (*Firewall, error) {
	logger := &log.Logger{
		Enabled: os.Getenv("_LOG_LEVEL") == "2",
		Prefix:  "firewall",
	}
	ipt4Name, ipt6Name := detectBackend(logger)

	ipt4, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4), iptables.Path(ipt4Name))
	if err != nil {
		return nil, fmt.Errorf("init iptables IPv4 (%s): %w", ipt4Name, err)
	}

	ipt6, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv6), iptables.Path(ipt6Name))
	if err != nil {
		return nil, fmt.Errorf("init iptables IPv6 (%s): %w", ipt6Name, err)
	}

	// Export for killswitch.sh
	os.Setenv("IPT_CMD", ipt4Name)
	os.Setenv("IP6T_CMD", ipt6Name)

	return &Firewall{ipt4: ipt4, ipt6: ipt6, log: logger, backend: ipt4Name}, nil
}

// Backend returns the detected backend name (e.g. "iptables-nft" or "iptables-legacy").
func (fw *Firewall) Backend() string {
	return fw.backend
}

// detectBackend determines the iptables backend to use. It checks IPT_BACKEND
// first, then auto-detects by trying iptables-nft and checking for warnings
// that indicate legacy tables are present (e.g. from Docker or the host).
func detectBackend(logger *log.Logger) (ipt4, ipt6 string) {
	switch os.Getenv("IPT_BACKEND") {
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

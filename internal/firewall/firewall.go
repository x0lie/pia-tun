package firewall

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/coreos/go-iptables/iptables"
	"github.com/x0lie/pia-tun/internal/log"
)

// Firewall manages iptables rules for VPN killswitch and temporary exemptions.
type Firewall struct {
	ipt4Cmd string
	ipt6Cmd string
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

	if err := checkCapNetAdmin(); err != nil {
		return nil, err
	}

	ipt4Cmd, ipt6Cmd := detectBackend(backend, logger)

	ipt4, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv4), iptables.Path(ipt4Cmd))
	if err != nil {
		return nil, fmt.Errorf("init iptables IPv4 (%s): %w", ipt4Cmd, err)
	}

	ipt6, err := iptables.New(iptables.IPFamily(iptables.ProtocolIPv6), iptables.Path(ipt6Cmd))
	if err != nil {
		return nil, fmt.Errorf("init iptables IPv6 (%s): %w", ipt6Cmd, err)
	}

	return &Firewall{ipt4: ipt4, ipt6: ipt6, log: logger, ipt4Cmd: ipt4Cmd, ipt6Cmd: ipt6Cmd}, nil
}

// Backend returns the detected backend name (e.g. "iptables-nft" or "iptables-legacy").
func (fw *Firewall) Backend() string {
	return fw.ipt4Cmd
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
		logger.Debug("iptables-nft detected exiting legacy tables rules, using legacy")
		return "iptables-legacy", "ip6tables-legacy"
	}

	logger.Debug("iptables-nft works cleanly, using nft")
	return "iptables-nft", "ip6tables-nft"
}

func (fw *Firewall) GetDropStats() (packetsIn, bytesIn, packetsOut, bytesOut int64) {
	iptables := fw.ipt4Cmd
	ip6tables := fw.ipt6Cmd

	parseChain := func(iptCmd, chain string) (packets, bytes int64) {
		cmd := exec.Command(iptCmd, "-L", chain, "-v", "-n", "-x")
		output, err := cmd.Output()
		if err != nil {
			if iptCmd == iptables {
				fw.log.Debug("Failed to get iptables stats for %s: %v", chain, err)
			}
			return 0, 0
		}

		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[2] == "DROP" {
				if p, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
					packets += p
				}
				if b, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					bytes += b
				}
			}
		}
		return packets, bytes
	}

	packetsIn, bytesIn = parseChain(iptables, "VPN_IN")
	packetsOut, bytesOut = parseChain(iptables, "VPN_OUT")

	p, b := parseChain(ip6tables, "VPN_IN6")
	packetsIn += p
	bytesIn += b
	p, b = parseChain(ip6tables, "VPN_OUT6")
	packetsOut += p
	bytesOut += b

	return packetsIn, bytesIn, packetsOut, bytesOut
}

func checkCapNetAdmin() error {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fmt.Errorf("cannot read /proc/self/status: %w", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			hex := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
			capEff, err := strconv.ParseUint(hex, 16, 64)
			if err != nil {
				return fmt.Errorf("cannot parse CapEff: %w", err)
			}
			const capNetAdmin = 1 << 12
			if capEff&capNetAdmin == 0 {
				return fmt.Errorf("missing CAP_NET_ADMIN")
			}
			return nil
		}
	}

	return fmt.Errorf("CapEff not found in /proc/self/status")
}

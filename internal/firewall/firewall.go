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
	mu     sync.Mutex
	active bool
}

// New creates a Firewall with auto-detected or manually specified iptables backend.
// Respects the IPT_BACKEND environment variable ("legacy" or "nft").
func New(backend string) (*Firewall, error) {
	logger := log.New("firewall")

	netRaw, err := checkCaps(backend == "legacy")
	if err != nil {
		return nil, err
	}

	if backend, err = detectBackend(backend, netRaw, logger); err != nil {
		return nil, err
	}

	ipt4Cmd := "iptables-" + backend
	ipt6Cmd := "ip6tables-" + backend

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
func detectBackend(backend string, netRaw bool, logger *log.Logger) (string, error) {
	// Confirm user selected backend works
	switch backend {
	case "nft":
		if err := exec.Command("iptables-nft", "-L", "-n").Run(); err != nil {
			return "", fmt.Errorf("iptables-nft not available: %w", err)
		}
		logger.Debug("IPT_BACKEND=legacy, using iptables-nft")
		return "nft", nil
	case "legacy":
		if err := exec.Command("iptables-legacy", "-L", "-n").Run(); err != nil {
			return "", fmt.Errorf("iptables-legacy not available: %w", err)
		}
		logger.Debug("IPT_BACKEND=legacy, using iptables-legacy")
		return "legacy", nil
	}

	// Auto-detect - try nft, fallback to legacy
	output, nftErr := exec.Command("iptables-nft", "-L", "-n").CombinedOutput()
	if nftErr != nil {
		if err := exec.Command("iptables-legacy", "-L", "-n").Run(); err != nil {
			return "", fmt.Errorf("neither iptables backend available: nft: %w, legacy: %s", nftErr, err)
		}
		if !netRaw {
			return "", fmt.Errorf("no iptables-nft available and iptables-legacy requires CAP_NET_RAW")
		}
		logger.Debug("iptables-nft unavailable, using iptables-legacy")
		return "legacy", nil
	}

	// If legacy rules exist, prefer legacy (follow system choice)
	if strings.Contains(strings.ToLower(string(output)), "iptables-legacy") {
		if netRaw {
			logger.Debug("iptables-legacy rules detected, using iptables-legacy")
			return "legacy", nil
		}
		log.Warning("iptables-legacy preferred (pre-existing rules), but no CAP_NET_RAW, using iptables-nft")
		log.Warning("set IPT_BACKEND=nft if working or add CAP_NET_RAW for iptables-legacy auto-selection")
		return "nft", nil
	}

	logger.Debug("iptables-nft works cleanly, using nft")
	return "nft", nil
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

func checkCaps(legacy bool) (bool, error) {
	const capNetAdmin = 1 << 12
	const capNetRaw = 1 << 13
	netRaw := false

	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false, fmt.Errorf("cannot read /proc/self/status: %w", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			hex := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
			capEff, err := strconv.ParseUint(hex, 16, 64)
			if err != nil {
				return netRaw, fmt.Errorf("cannot parse CapEff: %w", err)
			}
			if capEff&capNetAdmin == 0 {
				return netRaw, fmt.Errorf("firewall requires CAP_NET_ADMIN")
			}
			if legacy && capEff&capNetRaw == 0 {
				return netRaw, fmt.Errorf("iptables-legacy requires CAP_NET_RAW")
			}
			if capEff&capNetRaw != 0 {
				netRaw = true
			}
			return netRaw, nil
		}
	}
	return netRaw, fmt.Errorf("CapEff not found in /proc/self/status")
}

package wg

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/x0lie/pia-tun/internal/log"
)

const (
	ifaceName           = "pia0"
	fwmark              = 51820
	routingTable        = 51820
	persistentKeepalive = 25
	defaultMTU          = 1420

	priorityVPN      = 200
	prioritySuppress = 300

	wgGoWaitTimeout  = 3 * time.Second
	wgGoWaitInterval = 100 * time.Millisecond
)

var logger = log.New("wireguard")

type Config struct {
	PrivateKey    string
	PeerPublicKey string
	Endpoint      string
	PeerIP        string
	MTU           int
	IPv6Enabled   bool
	Backend       string
}

// GenerateKeyPair generates a WireGuard private/public key pair.
func GenerateKeyPair(ctx context.Context) (privateKey, publicKey string, err error) {
	cmd := exec.CommandContext(ctx, "wg", "genkey")
	privBytes, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("generate private key: %w", err)
	}
	privateKey = strings.TrimSpace(string(privBytes))

	cmd = exec.CommandContext(ctx, "wg", "pubkey")
	cmd.Stdin = strings.NewReader(privateKey)
	pubBytes, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("derive public key: %w", err)
	}
	publicKey = strings.TrimSpace(string(pubBytes))

	return privateKey, publicKey, nil
}

// Up creates and configures a WireGuard interface and routing rules.
func Up(ctx context.Context, cfg Config) (string, error) {
	// Defensive cleanup
	Down(ctx)

	// Detects backend and creates interface (prefers kernel)
	backend, err := createInterface(ctx, cfg.Backend)
	if err != nil {
		return "", fmt.Errorf("create interface: %w", err)
	}
	logger.Debug("Interface created (%s)", ifaceName)

	// Clean up on any subsequent failure
	defer func() {
		if err != nil {
			logger.Debug("Up failed, cleaning up partial state")
			Down(ctx)
		}
	}()

	// Set private key via /dev/stdin to avoid writing key to disk
	setKeyCmd := exec.CommandContext(ctx, "wg", "set", ifaceName, "private-key", "/dev/stdin")
	setKeyCmd.Stdin = strings.NewReader(cfg.PrivateKey)
	var setKeyStderr bytes.Buffer
	setKeyCmd.Stderr = &setKeyStderr
	if err := setKeyCmd.Run(); err != nil {
		msg := strings.TrimSpace(setKeyStderr.String())
		if msg != "" {
			return "", fmt.Errorf("set private key: %w (%s)", err, msg)
		}
		return "", fmt.Errorf("set private key: %w", err)
	}
	logger.Debug("Private key set")

	// Configure address
	if err := run(ctx, "ip", "address", "add", cfg.PeerIP+"/32", "dev", ifaceName); err != nil {
		return "", fmt.Errorf("add address: %w", err)
	}
	logger.Debug("Address configured: %s", cfg.PeerIP)

	// Set MTU
	mtu := max(cfg.MTU, 1280)
	if err := run(ctx, "ip", "link", "set", "mtu", strconv.Itoa(mtu), "dev", ifaceName); err != nil {
		return "", fmt.Errorf("set MTU: %w", err)
	}
	logger.Debug("MTU set (%v)", mtu)
	if mtu == 1280 {
		log.Success(fmt.Sprintf("MTU set to %v (safe minimum)", mtu))
	} else if mtu != defaultMTU {
		log.Success(fmt.Sprintf("MTU set to %v", mtu))
	}

	// Set fwmark BEFORE peer config to prevent routing loops
	fwmarkStr := strconv.Itoa(fwmark)
	if err := run(ctx, "wg", "set", ifaceName, "fwmark", fwmarkStr); err != nil {
		return "", fmt.Errorf("set fwmark: %w", err)
	}
	logger.Debug("Fwmark set (%d)", fwmark)

	// Configure peer
	allowedIPs := "0.0.0.0/0"
	if cfg.IPv6Enabled {
		allowedIPs = "0.0.0.0/0, ::/0"
	}
	if err := run(ctx, "wg", "set", ifaceName,
		"peer", cfg.PeerPublicKey,
		"endpoint", cfg.Endpoint,
		"allowed-ips", allowedIPs,
		"persistent-keepalive", strconv.Itoa(persistentKeepalive),
	); err != nil {
		return "", fmt.Errorf("configure peer: %w", err)
	}
	logger.Debug("Peer configured: endpoint: %s", cfg.Endpoint)

	// Bring interface up
	if err := run(ctx, "ip", "link", "set", ifaceName, "up"); err != nil {
		return "", fmt.Errorf("bring up interface: %w", err)
	}
	logger.Debug("%s set up", ifaceName)

	// Add VPN route to separate table
	tableStr := strconv.Itoa(routingTable)
	if err := run(ctx, "ip", "route", "add", "0.0.0.0/0", "dev", ifaceName, "table", tableStr); err != nil {
		return "", fmt.Errorf("add VPN route: %w", err)
	}
	logger.Debug("Added VPN route to table %d", routingTable)

	// Add VPN routing rules
	if err := run(ctx, "ip", "rule", "add", "not", "fwmark", fwmarkStr, "table", tableStr, "priority", strconv.Itoa(priorityVPN)); err != nil {
		return "", fmt.Errorf("add VPN routing rule: %w", err)
	}
	logger.Debug("Added VPN routing at priority %d", priorityVPN)
	if err := run(ctx, "ip", "rule", "add", "table", "main", "suppress_prefixlength", "0", "priority", strconv.Itoa(prioritySuppress)); err != nil {
		return "", fmt.Errorf("add suppress rule: %w", err)
	}
	logger.Debug("Added VPN suppression rule at priority %d", prioritySuppress)

	return backend, nil
}

// Down tears down the WireGuard interface, cleans up routing rules, and
// kills any userspace wireguard-go process. Safe to call even if Up was
// never called or failed partway through. All operations are best-effort.
func Down(ctx context.Context) {
	run(ctx, "ip", "link", "set", ifaceName, "down")
	logger.Debug("%s set down", ifaceName)
	run(ctx, "ip", "link", "del", ifaceName)
	logger.Debug("%s deleted", ifaceName)

	// Kill wireguard-go if running (userspace mode)
	exec.CommandContext(ctx, "pkill", "-f", "wireguard-go "+ifaceName).Run()
	os.Remove("/var/run/wireguard/" + ifaceName + ".sock")

	cleanupRoutingRules(ctx)
}

// createInterface creates the WireGuard network interface using kernel
// module or wireguard-go, depending on the backend setting.
func createInterface(ctx context.Context, backend string) (string, error) {
	switch backend {
	case "kernel":
		if err := run(ctx, "ip", "link", "add", ifaceName, "type", "wireguard"); err != nil {
			return "", fmt.Errorf("kernel WireGuard unavailable (WG_BACKEND=kernel): %w", err)
		}
		logger.Debug("Using kernel WireGuard")
		return "kernel", nil

	case "userspace":
		logger.Debug("WG_BACKEND=userspace, skipping kernel WireGuard")
		return startUserspace(ctx)

	default:
		// Auto-detect: try kernel first
		if err := run(ctx, "ip", "link", "add", ifaceName, "type", "wireguard"); err == nil {
			logger.Debug("Using kernel WireGuard")
			return "kernel", nil
		}
		logger.Debug("Kernel WireGuard unavailable, trying userspace")
		return startUserspace(ctx)
	}
}

// startUserspace launches wireguard-go and waits for the interface to appear.
func startUserspace(ctx context.Context) (string, error) {
	if err := ensureTUN(); err != nil {
		return "", err
	}

	logger.Debug("Starting wireguard-go daemon")
	cmd := exec.CommandContext(ctx, "wireguard-go", ifaceName)
	var wgOut bytes.Buffer
	cmd.Stdout = &wgOut
	cmd.Stderr = &wgOut
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start wireguard-go: %w", err)
	}

	if err := waitForInterface(ctx, ifaceName); err != nil {
		out := strings.TrimSpace(wgOut.String())
		if strings.Contains(out, "no such device") {
			return "", fmt.Errorf("TUN kernel module not loaded (run 'sudo modprobe tun' on host)")
		}
		if strings.Contains(out, "operation not permitted") {
			return "", fmt.Errorf("permission denied creating TUN device (check capabilities)")
		}
		return "", fmt.Errorf("wireguard-go failed to create interface: %s", out)
	}

	logger.Debug("Using userspace WireGuard (wireguard-go)")
	return "userspace", nil
}

// ensureTUN ensures /dev/net/tun exists (required for wireguard-go).
func ensureTUN() error {
	if _, err := os.Stat("/dev/net/tun"); err == nil {
		return nil
	}
	if err := os.MkdirAll("/dev/net", 0755); err != nil {
		return fmt.Errorf("create /dev/net: %w", err)
	}
	if err := exec.Command("mknod", "/dev/net/tun", "c", "10", "200").Run(); err != nil {
		return fmt.Errorf("create /dev/net/tun: %w (try '--device /dev/net/tun:/dev/net/tun' in docker run)", err)
	}
	if err := os.Chmod("/dev/net/tun", 0600); err != nil {
		return fmt.Errorf("chmod /dev/net/tun: %w", err)
	}
	return nil
}

// waitForInterface polls for the named interface to appear.
func waitForInterface(ctx context.Context, name string) error {
	deadline := time.After(wgGoWaitTimeout)
	for {
		if exec.CommandContext(ctx, "ip", "link", "show", name).Run() == nil {
			logger.Debug("wireguard-go interface detected")
			return nil
		}
		select {
		case <-deadline:
			return fmt.Errorf("interface %s did not appear within %s", name, wgGoWaitTimeout)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wgGoWaitInterval):
		}
	}
}

// cleanupRoutingRules removes VPN routing rules at priorities 200 and 300.
func cleanupRoutingRules(ctx context.Context) {
	for _, p := range []int{priorityVPN, prioritySuppress} {
		ps := strconv.Itoa(p)
		for {
			if run(ctx, "ip", "rule", "del", "priority", ps) != nil {
				break
			}
			logger.Debug("Removed routing rule at priority %d", p)
		}
	}
}

// run executes a command. On failure, stderr is included in the error.
func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func GetTransferBytes() (rx, tx int64, err error) {
	cmd := exec.Command("wg", "show", "pia0", "transfer")
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return 0, 0, fmt.Errorf("no transfer data")
	}

	parts := strings.Fields(lines[0])
	if len(parts) < 3 {
		return 0, 0, fmt.Errorf("interface transitioning")
	}

	rx, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}

	tx, err = strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, 0, err
	}

	return rx, tx, nil
}

func GetLastHandshake() int64 {
	cmd := exec.Command("wg", "show", "pia0", "latest-handshakes")
	output, err := cmd.Output()
	if err != nil {
		return 0
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return 0
	}

	parts := strings.Fields(lines[0])
	if len(parts) < 2 {
		return 0
	}

	timestamp, _ := strconv.ParseInt(parts[1], 10, 64)
	return timestamp
}

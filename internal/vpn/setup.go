package vpn

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/x0lie/pia-tun/internal/apperrors"
	"github.com/x0lie/pia-tun/internal/cacher"
	"github.com/x0lie/pia-tun/internal/dns"
	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/pia"
	"github.com/x0lie/pia-tun/internal/wg"
)

const apiTimeout = 3 * time.Second

// Config holds configuration for VPN setup.
type Config struct {
	PIAUser    string
	PIAPass    string
	Location   string
	DIPToken   string
	PFRequired bool
	ManualCN   string
	ManualIP   string
	MTU        int
	IPv6       bool
	WGBackend  string
}

// ConnectionInfo holds the result of a successful VPN connection.
// Populated by Setup() and consumed by monitor (metrics), port forwarding,
// and the orchestrator.
type ConnectionInfo struct {
	ServerIP  string
	ServerCN  string
	ClientIP  string
	PFGateway string
	DNS       []string
}

// target is the server to connect to, as chosen by one of the connection modes
type target struct {
	IP string
	CN string
}

// Setup establishes a VPN connection and returns connection info.
// Returns apperrors.ErrFatal on fatal errors (exit program),
// all other error returns are retried in app.connectLoop()
func Setup(ctx context.Context, cfg Config, fw *firewall.Firewall, cache *cacher.Cache, resolver *dns.Resolver) (*ConnectionInfo, error) {
	logger := log.New("vpn")

	// Get Auth Token
	token, err := getToken(ctx, cfg, fw, cache, resolver, logger)
	if err != nil {
		return nil, err
	}

	// Select target server
	var t target
	switch {
	case cfg.ManualCN != "" && cfg.ManualIP != "":
		t = manualTarget(cfg, logger)
	case cfg.DIPToken != "":
		t, err = dipTarget(ctx, cfg, fw, cache, resolver, token, logger)
	default:
		t, err = bestTarget(ctx, cfg, fw, cache, resolver, logger)
	}
	if err != nil {
		return nil, err
	}

	// Establish connection
	return connect(ctx, cfg, fw, cache, t, token, logger)
}

// getToken returns a valid authentication token, using cache if fresh.
func getToken(ctx context.Context, cfg Config, fw *firewall.Firewall, cache *cacher.Cache, resolver *dns.Resolver, logger *log.Logger) (string, error) {
	log.Step("Authenticating with PIA...")

	// Use cached token if fresh
	if fresh, tokenAge := cache.TokenFresh(); fresh {
		logger.Debug("Using cached token (age: %v)", tokenAge)
		log.Success("Using cached token")
		return cache.GetToken(), nil
	}

	token, err := tryAuthIPs(ctx, "auth", fw, cache, resolver, logger, func(ip string) (string, error) {
		return pia.GenerateToken(ctx, apiTimeout, ip, cfg.PIAUser, cfg.PIAPass)
	})
	switch {
	case errors.Is(err, apperrors.ErrFatal):
		return "", fmt.Errorf("%w\n    Check PIA_USER/PASS or secrets pia_user/pass", err)
	case errors.Is(err, apperrors.ErrRateLimited):
		return "", fmt.Errorf("%w\n    To avoid future rate limiting make the token persist:\n    volumes:\n      - ./pia-tun:/run/pia-tun/", err)
	case err != nil:
		return "", err
	}

	log.Success("Auth token acquired")
	cache.SetToken(token)
	return token, nil
}

// manualTarget returns the server set by PIA_CN/PIA_IP
func manualTarget(cfg Config, logger *log.Logger) target {
	logger.Debug("Using manual server override: %s (%s)", cfg.ManualCN, cfg.ManualIP)
	return target{
		IP: cfg.ManualIP,
		CN: cfg.ManualCN,
	}
}

// dipTarget resolves the DIP token to its dedicated server and verifies PF support if required
func dipTarget(ctx context.Context, cfg Config, fw *firewall.Firewall, cache *cacher.Cache, resolver *dns.Resolver, token string, logger *log.Logger) (target, error) {
	logger.Debug("Using DIP token for server selection")
	dipInfo, err := tryAuthIPs(ctx, "dip", fw, cache, resolver, logger, func(ip string) (*pia.DIPInfo, error) {
		return pia.ResolveDIP(ctx, apiTimeout, ip, token, cfg.DIPToken)
	})
	if err != nil {
		return target{}, err
	}
	log.Success("DIP token accepted")

	if cfg.PFRequired {
		if cache.Servers == nil {
			if err := getServers(ctx, fw, cache, resolver, logger); err != nil {
				return target{}, err
			}
		}
		var pfAvailable bool
		for _, srv := range cache.Servers {
			if srv.Region == dipInfo.Region {
				pfAvailable = srv.PF
				break
			}
		}
		if !pfAvailable {
			return target{}, fmt.Errorf("%w: DIP location %s (%s) does not support port forwarding - either set PF_ENABLED=false or contact PIA about changing your DIP region", apperrors.ErrFatal, dipInfo.CN, dipInfo.Region)
		}
	}

	return target{
		IP: dipInfo.IP,
		CN: dipInfo.CN,
	}, nil
}

// bestTarget returns the lowest latency server matching PIA_LOCATIONS
func bestTarget(ctx context.Context, cfg Config, fw *firewall.Firewall, cache *cacher.Cache, resolver *dns.Resolver, logger *log.Logger) (target, error) {
	if cache.Servers == nil {
		if err := getServers(ctx, fw, cache, resolver, logger); err != nil {
			return target{}, err
		}
	}

	srv, latency, err := selectServer(ctx, cfg, fw, cache, resolver, logger)
	if err != nil {
		return target{}, err
	}
	log.Success("Best server: %s (%dms) in %s", srv.CN, latency.Milliseconds(), srv.RegionName)

	return target{
		IP: srv.IP,
		CN: srv.CN,
	}, nil
}

// connect registers a WireGuard key with the target server and brings up the tunnel
func connect(ctx context.Context, cfg Config, fw *firewall.Firewall, cache *cacher.Cache, t target, token string, logger *log.Logger) (*ConnectionInfo, error) {
	// Generate WireGuard key pair
	log.Step("Establishing connection to %s...", t.CN)
	privateKey, publicKey, err := wg.GenerateKeyPair(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to generate WG keypair: %s", apperrors.ErrFatal, err)
	}
	logger.Debug("Generated WireGuard key pair")

	// Register public key with PIA server
	logger.Debug("Registering public key with %s", t.CN)
	if err = fw.AddExemption(t.IP, "1337", "tcp", "addkey"); err != nil {
		return nil, fmt.Errorf("addkey: %w", err)
	}
	var addKeyResp *pia.AddKeyResponse
	if cfg.DIPToken != "" {
		addKeyResp, err = pia.AddKeyDIP(ctx, t.IP, t.CN, cfg.DIPToken, publicKey)
	} else {
		addKeyResp, err = pia.AddKey(ctx, t.IP, t.CN, token, publicKey)
	}
	fw.RemoveExemptions()
	if err != nil {
		if errors.Is(err, apperrors.ErrTokenRejected) {
			cache.ClearToken()
		}
		return nil, err
	}
	logger.Debug("Server accepted public key, peer IP: %s", addKeyResp.PeerIP)
	log.Success("Auth token accepted")

	// Bring up WireGuard tunnel
	wgCfg := wg.Config{
		PrivateKey:    privateKey,
		PeerPublicKey: addKeyResp.ServerKey,
		Endpoint:      fmt.Sprintf("%s:%d", t.IP, addKeyResp.ServerPort),
		PeerIP:        addKeyResp.PeerIP,
		MTU:           cfg.MTU,
		IPv6Enabled:   cfg.IPv6,
		Backend:       cfg.WGBackend,
	}
	backend, err := wg.Up(ctx, wgCfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", apperrors.ErrFatal, err)
	}
	log.Success("Tunnel configured (%s)", backend)

	return &ConnectionInfo{
		ServerIP:  t.IP,
		ServerCN:  t.CN,
		ClientIP:  addKeyResp.PeerIP,
		PFGateway: addKeyResp.ServerVIP,
		DNS:       addKeyResp.DNSServers,
	}, nil
}

// tryAuthIPs runs call against each auth IP behind a firewall exemption until one succeeds.
// Resolves auth IPs if none are cached and clears them if all endpoints fail.
func tryAuthIPs[T any](ctx context.Context, label string, fw *firewall.Firewall, cache *cacher.Cache, resolver *dns.Resolver, logger *log.Logger, call func(ip string) (T, error)) (T, error) {
	var zero T

	// If no cached ips, resolve
	if cache.AuthIPs == nil {
		ips, err := resolver.Resolve(ctx, pia.AuthHostname)
		if err != nil {
			return zero, err
		}
		cache.MergeAuthIPs(ips)
		logger.Debug("Auth IPs resolved and cached")
	}

	// Try each AuthIP until call succeeds
	for _, ip := range cache.AuthIPs {
		logger.Debug("Trying auth IP: %s", ip)
		if err := fw.AddExemption(ip, "443", "tcp", label); err != nil {
			return zero, fmt.Errorf("%s: %w", label, err)
		}
		res, err := call(ip)
		fw.RemoveExemptions()
		if err == nil {
			return res, nil
		}
		if errors.Is(err, apperrors.ErrFatal) || errors.Is(err, apperrors.ErrRateLimited) {
			return zero, err
		}
		logger.Debug("%s via %s failed: %v", label, ip, err)
	}
	cache.ClearAuthIPs()
	log.Warning("Cleared cached auth IPs")
	return zero, fmt.Errorf("%s: all endpoints failed", label)
}

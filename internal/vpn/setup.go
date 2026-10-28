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

// Setup establishes a VPN connection and returns connection info.
// Returns apperrors.ErrFatal on fatal errors (exit program),
// all other error returns are retried in app.connectLoop()
func Setup(ctx context.Context, cfg Config, fw *firewall.Firewall, cache *cacher.Cache, resolver *dns.Resolver) (*ConnectionInfo, error) {
	logger := log.New("vpn")

	var serverIP, serverCN, region string
	var token string
	var srv pia.Server
	var dipInfo *pia.DIPInfo
	var pfAvailable bool
	var latency time.Duration
	var err error

	// Get Auth Token
	if token, err = getToken(ctx, cfg, fw, cache, resolver, logger); err != nil {
		return nil, err
	}

	// Gather serverlist
	if cache.Servers == nil {
		if err = getServers(ctx, fw, cache, resolver, logger); err != nil {
			return nil, err
		}
	}

	// Select Server
	switch {
	case cfg.ManualCN != "" && cfg.ManualIP != "":
		logger.Debug("Using manual server override: %s (%s)", cfg.ManualCN, cfg.ManualIP)
		serverIP = cfg.ManualIP
		serverCN = cfg.ManualCN
		region = "unknown"
	case cfg.DIPToken != "":
		logger.Debug("Using DIP token for server selection")
		if dipInfo, err = resolveDIP(ctx, cfg, fw, cache, resolver, token, logger); err != nil {
			return nil, err
		}
		serverIP = dipInfo.IP
		serverCN = dipInfo.CN
		region = "dedicated"
		log.Success("DIP token accepted")
		for _, srv := range cache.Servers {
			if srv.Region == dipInfo.Region {
				pfAvailable = srv.PF
				break
			}
		}
		if cfg.PFRequired && !pfAvailable {
			return nil, fmt.Errorf("%w: DIP location %s (%s) does not support port forwarding - either set PF_ENABLED=false or contact PIA about changing your DIP region", apperrors.ErrFatal, dipInfo.CN, dipInfo.Region)
		}
	default:
		if srv, latency, err = selectServer(ctx, cfg, fw, cache, resolver, logger); err != nil {
			return nil, err
		}
		log.Success("Best server: %s (%dms) in %s", srv.CN, latency.Milliseconds(), srv.RegionName)
		serverIP = srv.IP
		serverCN = srv.CN
		region = srv.Region
	}

	// Generate WireGuard key pair
	log.Step("Establishing connection to %s...", serverCN)
	privateKey, publicKey, err := wg.GenerateKeyPair(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to generate WG keypair: %s", apperrors.ErrFatal, err)
	}
	logger.Debug("Generated WireGuard key pair")

	// Register public key with PIA server
	logger.Debug("Registering public key with %s", serverCN)
	if err = fw.AddExemption(serverIP, "1337", "tcp", "addkey"); err != nil {
		return nil, fmt.Errorf("addkey: %w", err)
	}
	var addKeyResp *pia.AddKeyResponse
	if cfg.DIPToken != "" {
		addKeyResp, err = pia.AddKeyDIP(ctx, serverIP, serverCN, cfg.DIPToken, publicKey)
	} else {
		addKeyResp, err = pia.AddKey(ctx, serverIP, serverCN, token, publicKey)
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
		Endpoint:      fmt.Sprintf("%s:%d", serverIP, addKeyResp.ServerPort),
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
	logger.Debug("Connected to %s (%s) in %s, latency %dms", serverCN, serverIP, region, latency.Milliseconds())

	return &ConnectionInfo{
		ServerIP:  serverIP,
		ServerCN:  serverCN,
		ClientIP:  addKeyResp.PeerIP,
		PFGateway: addKeyResp.ServerVIP,
		DNS:       addKeyResp.DNSServers,
	}, nil
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

// resolveDIP resolves a DIP token to its server details using cached or resolved auth IPs.
func resolveDIP(ctx context.Context, cfg Config, fw *firewall.Firewall, cache *cacher.Cache, resolver *dns.Resolver, token string, logger *log.Logger) (*pia.DIPInfo, error) {
	info, err := tryAuthIPs(ctx, "dip", fw, cache, resolver, logger, func(ip string) (*pia.DIPInfo, error) {
		return pia.ResolveDIP(ctx, apiTimeout, ip, token, cfg.DIPToken)
	})
	return info, err
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

	// Get T (token or DIPInfo) using AuthIPs
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

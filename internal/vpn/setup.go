package vpn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

	// Step 1: Select server and authenticate
	var serverIP, serverCN, region string
	var token string
	var srv pia.Server
	var latency time.Duration
	var err error

	if cfg.ManualCN != "" && cfg.ManualIP != "" {
		// Manual override - skip server selection, just authenticate
		logger.Debug("Using manual server override: %s (%s)", cfg.ManualCN, cfg.ManualIP)
		serverIP = cfg.ManualIP
		serverCN = cfg.ManualCN
		region = "unknown"
		token, err = getToken(ctx, cfg, fw, cache, resolver, logger)
		if err != nil {
			return nil, err
		}
	} else {
		// Run server selection and auth
		if token, err = getToken(ctx, cfg, fw, cache, resolver, logger); err != nil {
			return nil, err
		}
		if srv, latency, err = selectServer(ctx, cfg, fw, cache, resolver, logger); err != nil {
			return nil, err
		}
		log.Success(fmt.Sprintf("Best server: %s (%dms) in %s", srv.CN, latency.Milliseconds(), srv.RegionName))

		serverIP = srv.IP
		serverCN = srv.CN
		region = srv.Region
	}

	// Step 2: Generate WireGuard key pair
	log.Step(fmt.Sprintf("Establishing connection to %s...", serverCN))
	privateKey, publicKey, err := wg.GenerateKeyPair(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to generate WG keypair: %s", apperrors.ErrFatal, err)
	}
	logger.Debug("Generated WireGuard key pair")

	// Step 3: Register public key with PIA server
	logger.Debug("Registering public key with %s", serverCN)
	if err = fw.AddExemption(serverIP, "1337", "tcp", "addkey"); err != nil {
		return nil, fmt.Errorf("addkey: %w", err)
	}
	addKeyResp, err := pia.AddKey(ctx, serverIP, serverCN, token, publicKey)
	fw.RemoveExemptions()
	if err != nil {
		cache.ClearToken()
		return nil, err
	}
	logger.Debug("Server accepted public key, peer IP: %s", addKeyResp.PeerIP)
	log.Success("Auth token accepted")

	// Step 4: Bring up WireGuard tunnel
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
		return nil, fmt.Errorf("%w: failed to bring up wireguard: %s", apperrors.ErrFatal, err)
	}
	log.Success(fmt.Sprintf("Tunnel configured (%s)", backend))
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

	token, err := authenticate(ctx, cfg, fw, cache, resolver, logger)
	if err != nil {
		return "", err
	}
	log.Success("Auth token acquired")

	cache.SetToken(token)
	return token, nil
}

// authenticate obtains a new token from PIA.
func authenticate(ctx context.Context, cfg Config, fw *firewall.Firewall, cache *cacher.Cache, resolver *dns.Resolver, logger *log.Logger) (string, error) {
	client := pia.NewDirectClient(apiTimeout)

	// Try cached auth IPs first
	for _, ip := range cache.AuthIPs {
		logger.Debug("Trying cached auth IP: %s", ip)
		token, err := tryAuth(ctx, client, ip, cfg.PIAUser, cfg.PIAPass, fw)
		if err == nil {
			return token, nil
		}
		// Check for auth errors (fatal, don't retry)
		if errors.Is(err, apperrors.ErrFatal) {
			return "", fmt.Errorf("%w\n    Check PIA_USER/PASS or secrets pia_user/pass", err)
		}
		logger.Debug("Auth via %s failed: %v", ip, err)
	}

	// Fall back to DNS resolution
	ips, err := resolver.Resolve(ctx, pia.AuthHostname)
	if err != nil {
		return "", err
	}

	cache.MergeAuthIPs(ips)

	for _, ip := range ips {
		logger.Debug("Trying resolved auth IP: %s", ip)
		token, err := tryAuth(ctx, client, ip, cfg.PIAUser, cfg.PIAPass, fw)
		if err == nil {
			return token, nil
		}
		// Check for auth errors (fatal, don't retry)
		if errors.Is(err, apperrors.ErrFatal) {
			return "", fmt.Errorf("%w\n    Check PIA_USER/PASS or secrets pia_user/pass", err)
		}
		logger.Debug("Auth via %s failed: %v", ip, err)
	}

	return "", fmt.Errorf("auth: all endpoints failed")
}

// tryAuth attempts authentication with a single IP.
func tryAuth(ctx context.Context, client *http.Client, ip, user, pass string, fw *firewall.Firewall) (string, error) {
	comment := "auth"
	err := fw.AddExemption(ip, "443", "tcp", comment)
	if err != nil {
		return "", fmt.Errorf("auth: %w", err)
	}
	defer fw.RemoveExemptions()

	return pia.GenerateToken(ctx, client, ip, user, pass)
}

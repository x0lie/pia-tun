package vpn

import (
	"context"
	"fmt"
	"net/http"
	"time"

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
	ServerIP     string
	ServerCN     string
	ClientIP     string
	PFGateway    string
	DNS          []string
	Location     string
	LocationName string
	Latency      time.Duration
	WGMode       string
}

// Setup establishes a VPN connection and returns connection info.
// Returns *pia.AuthError for credential failures (fatal).
// Returns *pia.ConnectivityError for network failures (retry with WAN check).
func Setup(ctx context.Context, cfg Config, fw *firewall.Firewall, cache *cacher.Cache, resolver *dns.Resolver) (*ConnectionInfo, error) {
	logger := log.New("vpn")

	// Defensive cleanup
	wg.Down(ctx, logger)

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
		return nil, &pia.ConnectivityError{Op: "keygen", Msg: "generate WireGuard keys", Err: err}
	}
	logger.Debug("Generated WireGuard key pair")

	// Step 3: Register public key with PIA server
	comment := "addkey"
	logger.Debug("Registering public key with %s", serverCN)
	if err = fw.AddExemption(serverIP, "1337", "tcp", comment); err != nil {
		return nil, &pia.ConnectivityError{Op: "addkey", Msg: "add firewall exemption", Err: err}
	}
	addKeyResp, err := pia.AddKey(ctx, serverIP, serverCN, token, publicKey)
	fw.RemoveExemptions(comment)
	if err != nil {
		if _, isTokenRejected := err.(*pia.TokenRejectedError); isTokenRejected {
			cache.ClearToken()
		}
		return nil, err
	}
	logger.Debug("Server accepted public key, peer IP: %s", addKeyResp.PeerIP)
	log.Success("Auth token accepted")

	// Step 4: Bring up WireGuard tunnel
	allowedIPs := "0.0.0.0/0"
	if cfg.IPv6 {
		allowedIPs = "0.0.0.0/0, ::/0"
	}
	wgCfg := wg.Config{
		PrivateKey:    privateKey,
		PeerPublicKey: addKeyResp.ServerKey,
		Endpoint:      fmt.Sprintf("%s:%d", serverIP, addKeyResp.ServerPort),
		PeerIP:        addKeyResp.PeerIP,
		MTU:           cfg.MTU,
		AllowedIPs:    allowedIPs,
		Backend:       cfg.WGBackend,
	}
	iface, err := wg.Up(ctx, wgCfg, logger)
	if err != nil {
		return nil, &pia.ConnectivityError{Op: "wireguard", Msg: "bring up tunnel", Err: err}
	}
	log.Success(fmt.Sprintf("WG tunnel configured (%s)", iface.Backend))

	return &ConnectionInfo{
		ServerIP:     serverIP,
		ServerCN:     serverCN,
		ClientIP:     addKeyResp.PeerIP,
		PFGateway:    addKeyResp.ServerVIP,
		DNS:          addKeyResp.DNSServers,
		Location:     region,
		LocationName: region, // TODO: look up human-readable name if needed
		Latency:      latency,
		WGMode:       iface.Backend,
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
		if _, isAuth := err.(*pia.AuthError); isAuth {
			return "", err
		}
		logger.Debug("Auth via %s failed: %v", ip, err)
	}

	// Fall back to DNS resolution
	ips, err := resolver.Resolve(ctx, "www.privateinternetaccess.com")
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
		if _, isAuth := err.(*pia.AuthError); isAuth {
			return "", err
		}
		logger.Debug("Auth via %s failed: %v", ip, err)
	}

	return "", &pia.ConnectivityError{Op: "auth", Msg: "all auth endpoints failed"}
}

// tryAuth attempts authentication with a single IP.
func tryAuth(ctx context.Context, client *http.Client, ip, user, pass string, fw *firewall.Firewall) (string, error) {
	comment := "auth"
	err := fw.AddExemption(ip, "443", "tcp", comment)
	if err != nil {
		return "", &pia.ConnectivityError{Op: "auth", Msg: "add firewall exemption", Err: err}
	}
	defer fw.RemoveExemptions(comment)

	return pia.GenerateToken(ctx, client, ip, user, pass)
}

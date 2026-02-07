package vpn

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/x0lie/pia-tun/internal/firewall"
	"github.com/x0lie/pia-tun/internal/log"
	"github.com/x0lie/pia-tun/internal/pia"
	"github.com/x0lie/pia-tun/internal/wg"
)

const (
	tokenMaxAge        = 23 * time.Hour
	apiTimeout         = 5 * time.Second
	latencyTestTimeout = 1 * time.Second
)

// SetupConfig holds configuration for VPN setup.
type SetupConfig struct {
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

// Setup establishes a VPN connection and returns connection info.
// Returns *pia.AuthError for credential failures (fatal).
// Returns *pia.ConnectivityError for network failures (retry with WAN check).
func Setup(ctx context.Context, cfg SetupConfig, fw *firewall.Firewall, cache *CacheState, resolver *pia.Resolver, logger *log.Logger) (*ConnectionInfo, error) {
	// Step 1: Select server and authenticate
	var serverIP, serverCN, region string
	var latency time.Duration
	var token string
	var srvErr, authErr error

	if cfg.ManualCN != "" && cfg.ManualIP != "" {
		// Manual override - skip server selection, just authenticate
		logger.Debug("Using manual server override: %s (%s)", cfg.ManualCN, cfg.ManualIP)
		serverIP = cfg.ManualIP
		serverCN = cfg.ManualCN
		region = "manual"
		token, authErr = getToken(ctx, cfg, fw, cache, resolver, logger)
	} else {
		// Run server selection and auth in parallel
		var srv pia.CachedServer
		token, authErr = getToken(ctx, cfg, fw, cache, resolver, logger)
		srv, latency, srvErr = selectServer(ctx, cfg, fw, cache, resolver, logger)

		if srvErr == nil {
			serverIP = srv.IP
			serverCN = srv.CN
			region = srv.Region
			logger.Debug("Selected server: %s (%s) in %s, latency %dms", serverCN, serverIP, region, latency.Milliseconds())
			log.Success(fmt.Sprintf("Best server: %s (%dms) in %s", serverCN, latency.Milliseconds(), srv.RegionName))
		}
	}

	// Handle errors with priority: AuthError (fatal) > both failed > individual failures
	if _, isAuth := authErr.(*pia.AuthError); isAuth {
		return nil, authErr // Fatal: bad credentials
	}
	if srvErr != nil && authErr != nil {
		// Both failed - likely WAN/connectivity issue
		return nil, &pia.ConnectivityError{
			Op:  "setup",
			Msg: fmt.Sprintf("server selection and auth both failed (server: %v, auth: %v)", srvErr, authErr),
		}
	}
	if srvErr != nil {
		return nil, fmt.Errorf("server selection: %w", srvErr)
	}
	if authErr != nil {
		return nil, authErr
	}

	// Step 2: Generate WireGuard key pair
	log.Step(fmt.Sprintf("Establishing connection to %s...", serverCN))
	privateKey, publicKey, err := wg.GenerateKeyPair(ctx)
	if err != nil {
		return nil, &pia.ConnectivityError{Op: "keygen", Msg: "generate WireGuard keys", Err: err}
	}
	logger.Debug("Generated WireGuard key pair")

	// Step 3: Register public key with PIA server
	logger.Debug("Registering public key with %s", serverCN)
	exemption, err := fw.AddTemporaryExemption(serverIP, "1337", "tcp", "addkey")
	if err != nil {
		return nil, &pia.ConnectivityError{Op: "addkey", Msg: "add firewall exemption", Err: err}
	}
	addKeyResp, err := pia.AddKey(ctx, serverIP, serverCN, token, publicKey)
	fw.RemoveTemporaryExemption(exemption)
	if err != nil {
		return nil, err // AddKey returns typed errors
	}
	logger.Debug("Server accepted public key, peer IP: %s", addKeyResp.PeerIP)
	log.Success("Key registered")

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
	logger.Debug("WireGuard tunnel up (backend: %s)", iface.Backend)
	log.Success(fmt.Sprintf("WireGuard tunnel up (%s)", iface.Backend))

	// Step 5: Add VPN to killswitch
	if err := fw.AddVPN("51820", cfg.IPv6); err != nil {
		wg.Down(ctx, logger)
		return nil, &pia.ConnectivityError{Op: "firewall", Msg: "add VPN to killswitch", Err: err}
	}
	log.Success("VPN added to killswitch")

	return &ConnectionInfo{
		Token:        token,
		ServerIP:     serverIP,
		ServerCN:     serverCN,
		ClientIP:     addKeyResp.PeerIP,
		PFGateway:    addKeyResp.ServerVIP,
		Location:     region,
		LocationName: region, // TODO: look up human-readable name if needed
		Latency:      latency,
		WGMode:       iface.Backend,
	}, nil
}

// selectServer fetches the server list, merges with cache, and selects by latency.
// Flow: fetch fresh (cached IP or DNS) → merge with cache → filter → latency test
func selectServer(ctx context.Context, cfg SetupConfig, fw *firewall.Firewall, cache *CacheState, resolver *pia.Resolver, logger *log.Logger) (pia.CachedServer, time.Duration, error) {
	// Fetch fresh server list (uses cached serverlist IPs, falls back to DNS)
	log.Step(fmt.Sprintf("Selecting server across %s...", cfg.Location))
	freshServers, err := fetchServerList(ctx, cache, resolver, fw, logger)
	if err != nil {
		return pia.CachedServer{}, 0, err
	}

	cache.MergeServers(freshServers)

	candidates := FilterServers(cache.Servers, cfg.Location, cfg.PFRequired)
	if len(candidates) == 0 {
		return pia.CachedServer{}, 0, &pia.ConnectivityError{
			Op:  "serverlist",
			Msg: fmt.Sprintf("no servers found for location %q (pf_required=%v)", cfg.Location, cfg.PFRequired),
		}
	}

	logger.Debug("Found %d candidates after filtering", len(candidates))
	return SelectServer(ctx, candidates, fw, latencyTestTimeout, logger)
}

// fetchServerList fetches the server list using cached IPs or DNS resolution.
func fetchServerList(ctx context.Context, cache *CacheState, resolver *pia.Resolver, fw *firewall.Firewall, logger *log.Logger) ([]pia.CachedServer, error) {
	client := pia.NewDirectClient(apiTimeout)

	// Try cached serverlist IPs first
	for _, ip := range cache.ServerListIPs {
		logger.Debug("Trying cached serverlist IP: %s", ip)
		exemption, err := fw.AddTemporaryExemption(ip, "443", "tcp", "serverlist")
		if err != nil {
			logger.Debug("Failed to add exemption: %v", err)
			continue
		}
		regions, err := pia.FetchServerList(ctx, client, ip)
		fw.RemoveTemporaryExemption(exemption)
		if err == nil {
			logger.Debug("Fetched server list via cached IP %s", ip)
			return pia.FlattenRegions(regions), nil
		}
		logger.Debug("Serverlist fetch from %s failed: %v", ip, err)
	}

	// Fall back to DNS resolution
	ips, err := resolver.Resolve(ctx, "serverlist.piaservers.net")
	if err != nil {
		return nil, err
	}

	cache.MergeIPs(&cache.ServerListIPs, ips, 5)

	for _, ip := range ips {
		logger.Debug("Trying resolved serverlist IP: %s", ip)
		exemption, err := fw.AddTemporaryExemption(ip, "443", "tcp", "serverlist")
		if err != nil {
			continue
		}
		regions, err := pia.FetchServerList(ctx, client, ip)
		fw.RemoveTemporaryExemption(exemption)
		if err == nil {
			logger.Debug("Fetched server list via resolved IP %s", ip)
			return pia.FlattenRegions(regions), nil
		}
		logger.Debug("Serverlist fetch from %s failed: %v", ip, err)
	}

	return nil, &pia.ConnectivityError{Op: "serverlist", Msg: "all serverlist endpoints failed"}
}

// getToken returns a valid authentication token, using cache if fresh.
func getToken(ctx context.Context, cfg SetupConfig, fw *firewall.Firewall, cache *CacheState, resolver *pia.Resolver, logger *log.Logger) (string, error) {
	log.Step("Authenticating with PIA...")
	// Use cached token if fresh
	if cache.TokenFresh(tokenMaxAge) {
		logger.Debug("Using cached token (age: %s)", time.Since(cache.TokenTime).Round(time.Second))
		log.Success("Using cached token")
		return cache.Token, nil
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
func authenticate(ctx context.Context, cfg SetupConfig, fw *firewall.Firewall, cache *CacheState, resolver *pia.Resolver, logger *log.Logger) (string, error) {
	client := pia.NewDirectClient(apiTimeout)

	// Try cached auth IPs first
	for _, ip := range cache.AuthIPs {
		logger.Debug("Trying cached auth IP: %s", ip)
		token, err := tryAuth(ctx, client, ip, cfg.PIAUser, cfg.PIAPass, fw, logger)
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

	cache.MergeIPs(&cache.AuthIPs, ips, 5)

	for _, ip := range ips {
		logger.Debug("Trying resolved auth IP: %s", ip)
		token, err := tryAuth(ctx, client, ip, cfg.PIAUser, cfg.PIAPass, fw, logger)
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
func tryAuth(ctx context.Context, client *http.Client, ip, user, pass string, fw *firewall.Firewall, logger *log.Logger) (string, error) {
	exemption, err := fw.AddTemporaryExemption(ip, "443", "tcp", "auth")
	if err != nil {
		return "", &pia.ConnectivityError{Op: "auth", Msg: "add firewall exemption", Err: err}
	}
	defer fw.RemoveTemporaryExemption(exemption)

	return pia.GenerateToken(ctx, client, ip, user, pass)
}

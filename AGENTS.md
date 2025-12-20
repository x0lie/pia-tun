# AGENTS.md

**Guide for AI agents working with the pia-tun codebase**

This is a Private Internet Access (PIA) WireGuard VPN container with leak protection, automatic reconnection, SOCKS5/HTTP proxies, port forwarding, and Prometheus metrics.

---

## Project Overview

**Type:** Docker container / Go + Bash hybrid system  
**Primary Languages:** Go (cmd/), Bash (scripts/, run.sh)  
**Purpose:** Secure VPN tunnel with killswitch, health monitoring, and port forwarding  
**Architecture:** Multi-process container orchestrated by run.sh

### Core Components

1. **run.sh** - Main entrypoint, orchestrates lifecycle
2. **Go binaries** - Three compiled services:
   - `cmd/proxy/` → `/usr/local/bin/proxy` - SOCKS5/HTTP proxy server
   - `cmd/monitor/` → `/usr/local/bin/monitor` - VPN health monitor
   - `cmd/portforward/` → `/usr/local/bin/portforward` - Port forwarding manager
3. **Shell scripts** - VPN setup, firewall management, connection verification
4. **Docker** - Multi-stage build, Alpine 3.19 base

---

## Essential Commands

### Building

```bash
# Build Docker image locally
docker build -t pia-tun:local .

# Multi-arch build (like CI/CD)
docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 -t pia-tun .
```

### Testing

```bash
# Run test suite (requires PIA credentials)
export PIA_USER="p1234567"
export PIA_PASS="your_password"
export PIA_LOCATION="us_california"
export PORT_FORWARDING="false"
export IMAGE_NAME="pia-tun:test"
chmod +x testing/test.sh
./testing/test.sh

# Run leak tests (inside container)
docker exec pia-tun /usr/local/bin/leaktest \
  -duration=60s \
  -concurrency=50 \
  -protocols=http,https,dns,udp \
  -real-ip="1.2.3.4"
```

**Note:** No Go unit tests exist. Testing is done via `testing/test.sh` (integration tests) and `testing/leaktest/` (leak detection).

### Development Workflow

```bash
# Build and run locally with docker-compose
docker compose -f docker-compose.yml up --build

# View logs
docker logs -f pia-tun

# Debug inside container
docker exec -it pia-tun bash

# Check VPN status
docker exec pia-tun wg show pia

# Check health
docker exec pia-tun /usr/local/bin/monitor

# Test proxy
curl -x socks5://localhost:1080 https://ifconfig.me
```

### CI/CD

Three GitHub Actions workflows:

1. **test.yml** - Runs on push to main/develop and PRs
   - Runs `testing/test.sh`
   - Trivy security scan (non-blocking)
2. **develop.yml** - Runs on push to develop branch
   - Builds and pushes `olsonalexw/k8s-wireguard-pia:develop` (amd64 only)
3. **release.yml** - Runs on version tags (v1.0.0, etc.)
   - Multi-arch build (amd64, arm64, armv7)
   - Pushes `x0lie/pia-tun:latest` and `x0lie/pia-tun:VERSION`
   - Creates GitHub release with auto-generated changelog

**Note:** Image names differ between workflows due to migration in progress.

---

## Code Organization

### Directory Structure

```
/home/olson/Projects/pia-tun-private/
├── cmd/                          # Go services
│   ├── monitor/                  # Health monitoring daemon
│   │   ├── main.go              # Config, lifecycle, reconnection logic
│   │   ├── health.go            # Interface checks, connectivity tests
│   │   └── metrics.go           # Prometheus metrics endpoint
│   ├── portforward/             # PIA port forwarding
│   │   ├── main.go              # Config, lifecycle
│   │   ├── client.go            # HTTP client for PIA API
│   │   └── keepalive.go         # Signature refresh, bind keepalive
│   └── proxy/                   # Network proxies
│       └── main.go              # SOCKS5 + HTTP proxy implementation
├── scripts/                      # Bash libraries
│   ├── killswitch.sh            # Firewall management (nftables/iptables)
│   ├── vpn.sh                   # PIA auth, server selection, WireGuard config
│   ├── verify_connection.sh     # Post-connection verification
│   ├── proxy_go.sh              # Proxy lifecycle management
│   ├── port_monitor.sh          # Monitors port changes, triggers API updates
│   ├── port_api_updater.sh      # Updates torrent clients via REST APIs
│   └── ui.sh                    # Logging, colors, status display
├── testing/                      # Test infrastructure
│   ├── test.sh                  # Integration test suite
│   └── leaktest/                # Leak detection tool (Go)
├── ca/                          # PIA certificate
│   └── rsa_4096.crt
├── run.sh                       # Main entrypoint
├── Dockerfile                   # Multi-stage build
├── docker-compose.yml           # Example deployment
└── PROJECT_STRUCTURE.md         # Detailed component documentation
```

### Inter-Process Communication

- **Named pipes:** `/tmp/vpn_reconnect_pipe`, `/tmp/port_change_pipe`
- **Flag files:** `/tmp/reconnecting`, `/tmp/pf_signature_failed`, `/tmp/killswitch_up`, `/tmp/port_forwarding_complete`
- **PID files:** `/tmp/proxy.pid`, `/tmp/portforward.pid`
- **State files:** `/tmp/pia_token`, `/tmp/client_ip`, `/tmp/meta_cn`, `/tmp/pf_gateway`, `/tmp/server_latency`, `/tmp/real_ip`
- **Config files:** `/etc/wireguard/pia.conf`, `/etc/wireguard/port`

---

## Code Patterns & Conventions

### Bash Scripts

**Style:**
- Use `set -euo pipefail` at the top of every script
- Source dependencies explicitly: `source /app/scripts/ui.sh`
- Function names use `snake_case`
- Local variables use `local` keyword
- Prefer inline defaults: `VAR=${VAR:-default}`
- Export only what child processes need

**Logging:**
- Use functions from `ui.sh`: `show_step`, `show_success`, `show_error`, `show_warning`, `show_debug`
- Use `log_debug`, `log_info`, `log_error` for level-aware logging
- Debug output goes to stderr (`>&2`)
- Respect `_LOG_LEVEL` variable (0=error, 1=info, 2=debug)

**Error Handling:**
- Critical failures exit with `exit 1`
- Non-critical failures log and continue
- Use `|| true` to prevent exit on acceptable failures
- Validate inputs before operations

**Common Patterns:**

```bash
# Sourcing libraries
source /app/scripts/ui.sh
source /app/scripts/killswitch.sh

# Logging
show_step "Starting operation..."
show_debug "Debug details: $variable"
show_success "Operation complete"

# Temporary exemptions (surgical firewall holes)
add_temporary_exemption "1.1.1.1" "443" "tcp" "dns_resolve"
# ... perform operation ...
remove_temporary_exemption "dns_resolve"

# Error handling
if ! operation; then
    show_error "Operation failed"
    return 1
fi

# Named pipe communication (non-blocking)
echo "message" > /tmp/pipe_name &
```

### Go Code

**Style:**
- Standard Go formatting (gofmt)
- Package comment at top of main.go
- Struct-based config with env parsing
- Error wrapping with `fmt.Errorf("message: %w", err)`
- Context-aware operations for graceful shutdown
- Timeouts on all network operations

**Configuration Pattern:**

```go
// Read from environment with defaults
getEnvInt := func(key string, defaultVal int) int {
    if val := os.Getenv(key); val != "" {
        if i, err := strconv.Atoi(val); err == nil {
            return i
        }
    }
    return defaultVal
}

config := &Config{
    CheckInterval: time.Duration(getEnvInt("CHECK_INTERVAL", 15)) * time.Second,
    DebugMode:     getEnvInt("_LOG_LEVEL", 0) == 2,
}
```

**Secrets Pattern:**

```go
// Prefer Docker secrets over environment variables
func getSecret(envVar, secretPath string) string {
    if data, err := os.ReadFile(secretPath); err == nil {
        return strings.TrimSpace(string(data))
    }
    return os.Getenv(envVar)
}

proxyUser = getSecret("PROXY_USER", "/run/secrets/proxy_user")
```

**Logging Pattern:**

```go
// Debug mode based on _LOG_LEVEL
if config.DebugMode {
    log.Printf("[DEBUG] %s: %v", operation, details)
}
```

**HTTP Client Pattern:**

```go
// Bind to specific interface
transport := &http.Transport{
    DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
        iface, _ := net.InterfaceByName("pia")
        addrs, _ := iface.Addrs()
        ipNet, _ := addrs[0].(*net.IPNet)
        
        localAddr := &net.TCPAddr{IP: ipNet.IP}
        dialer := &net.Dialer{LocalAddr: localAddr, Timeout: 10 * time.Second}
        return dialer.DialContext(ctx, network, addr)
    },
    TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
}
```

**Retry Pattern:**

```go
// Time-based retry with exponential backoff
maxDuration := 5 * time.Minute
backoff := 2 * time.Second
maxBackoff := 30 * time.Second
startTime := time.Now()

for {
    result, err := operation()
    if err == nil {
        return result, nil
    }
    
    // Distinguish API errors from network errors
    if apiErr, ok := err.(*APIError); ok {
        return nil, apiErr  // Don't retry API errors
    }
    
    if time.Since(startTime) > maxDuration {
        return nil, fmt.Errorf("operation timed out after %s", maxDuration)
    }
    
    time.Sleep(backoff)
    backoff = time.Duration(float64(backoff) * 1.5)
    if backoff > maxBackoff {
        backoff = maxBackoff
    }
}
```

### Color Codes

Both Bash and Go use ANSI color codes:
- Red (`\033[0;31m`): Errors, failures
- Green (`\033[0;32m`): Success, connected
- Blue (`\033[0;34m`): Steps, information
- Yellow (`\033[0;33m`): Warnings, reconnecting
- Reset (`\033[0m`): End color

---

## Critical Security Patterns

### Killswitch Architecture

The firewall has three layers:

1. **Baseline Killswitch** (always active)
   - Default DROP policy
   - Allows: loopback, established connections, local networks
   - Blocks: all outbound traffic until VPN is added

2. **Bypass Routing Table** (table 100)
   - Routes specific IPs (NIST time servers) via eth0
   - Used for WAN health checks (distinguish internet outage from VPN failure)
   - No firewall manipulation needed

3. **Surgical Exemptions** (temporary)
   - Minimal firewall holes during VPN setup
   - Tagged with comments for easy removal
   - Immediately removed after use

**Critical Order:**
- Setup: Baseline → Add VPN → Remove exemptions
- Teardown: Remove VPN → Teardown interface (NOT: Teardown → Remove VPN)

**Firewall Backends:**
- Prefer nftables (modern, O(1) lookups, sets)
- Fallback to iptables (legacy, chain-based)
- Auto-detected at runtime

### Leak Prevention

**During Normal Operation:**
- All traffic routed through `pia` interface
- IPv6 completely blocked (if `DISABLE_IPV6=true`)
- DNS locked to PIA DNS or custom servers
- Proxy authentication uses constant-time comparison (prevent timing attacks)

**During Reconnection:**
- Killswitch remains active during teardown
- VPN removed from killswitch BEFORE interface teardown
- `/tmp/reconnecting` flag prevents health checks
- Health monitor waits for WAN before reconnecting

**Verification Steps:**
1. Interface status (wg show pia)
2. External IP check (must differ from real IP)
3. DNS validation (PIA DNS or configured DNS)
4. IPv6 leak check (must timeout)

---

## Environment Variables

### Required (Authentication)
- `PIA_USER` - PIA username (p-numbers format)
- `PIA_PASS` - PIA password

### VPN Configuration
- `PIA_LOCATION` - Comma-separated server list (default: us_california)
- `PORT_FORWARDING` - Enable port forwarding (default: false)
- `DISABLE_IPV6` - Block IPv6 (default: true)
- `DNS` - DNS servers or "pia" (default: pia)
- `LOCAL_NETWORK` - CIDR ranges for LAN access (comma-separated)
- `LOCAL_PORTS` - Ports accessible from LAN (comma-separated)
- `HANDSHAKE_TIMEOUT` - WireGuard handshake timeout (default: 180)

### Proxy Services
- `PROXY_ENABLED` - Enable SOCKS5/HTTP proxies (default: false)
- `SOCKS5_PORT` - SOCKS5 port (default: 1080)
- `HTTP_PROXY_PORT` - HTTP proxy port (default: 8888)
- `PROXY_USER` - Proxy username (optional)
- `PROXY_PASS` - Proxy password (optional)

### Port Forwarding & API Updates
- `PORT_API_TYPE` - Client type: qbittorrent, transmission, deluge, rtorrent, custom
- `PORT_API_URL` - API endpoint for port updates
- `PORT_API_USER` - API username (optional)
- `PORT_API_PASS` - API password (optional)
- `PORT_API_CMD` - Custom command with {PORT} placeholder
- `PORT_API_ENABLED` - Auto-set by run.sh when PORT_API_TYPE is set
- `PORT_FILE` - Port file location (default: /etc/wireguard/port)
- `WEBHOOK_URL` - Webhook for port change notifications
- `PF_BIND_INTERVAL` - Bind refresh interval (default: 900s)
- `PF_SIGNATURE_REFRESH_DAYS` - Signature refresh interval (default: 31)
- `PF_SIGNATURE_SAFETY_HOURS` - Safety margin before expiry (default: 24)

### Monitoring
- `METRICS` - Enable Prometheus metrics (default: false)
- `METRICS_PORT` - Metrics port (default: 9090)
- `CHECK_INTERVAL` - Health check interval (default: 15s)
- `MAX_FAILURES` - Failures before reconnect (default: 2-3)
- `MONITOR_PARALLEL_CHECKS` - Parallel health checks (default: false)
- `RESTART_SERVICES` - Containers to restart after reconnection (comma-separated)

### Logging
- `LOG_LEVEL` - error/info/debug or 0/1/2 (default: info)
- `_LOG_LEVEL` - Internal numeric level (set by ui.sh)
- `TZ` - Timezone (default: UTC)

### Docker Secrets (Alternative)
- `/run/secrets/pia_user`, `/run/secrets/pia_pass`
- `/run/secrets/proxy_user`, `/run/secrets/proxy_pass`

---

## Important Gotchas

### 1. Killswitch Cleanup Required

**Problem:** Orphaned firewall rules prevent subsequent containers from networking.

**Solution:** `cleanup()` in run.sh calls `cleanup_killswitch` to remove all rules. Critical for Kubernetes where network namespaces may be reused.

### 2. VPN Removal Order

**Problem:** Removing VPN from killswitch AFTER interface teardown causes leak window.

**Correct Order:**
```bash
remove_vpn_from_killswitch  # First
wg-quick down pia           # Second
```

### 3. Port Forwarding Death Window

**Problem:** PIA kills ports after 15 minutes without bind.

**Solution:** Bind interval (900s) + bind retry (3min) + signature retry (2min) = 20min max, leaving 4-minute safety margin.

### 4. Executable Duplication in Docker Build

**Problem:** Running `chmod +x` on Go binaries after COPY duplicates layers (10MB overhead).

**Solution:** Mark binaries executable in builder stage:
```dockerfile
RUN chmod +x /build/proxy /build/monitor /build/portforward
COPY --from=go-builder /build/proxy /usr/local/bin/proxy
# Don't chmod again
```

### 5. API Error vs Network Error

**Problem:** Retrying API errors (status != OK) wastes time and delays reconnection.

**Solution:**
- Network errors: Retry with backoff
- API errors: Immediately escalate (trigger signature refresh or reconnection)

```go
type APIError struct { ... }

if apiErr, ok := err.(*APIError); ok {
    return nil, apiErr  // Stop retrying
}
```

### 6. TLS Verification Disabled

**Problem:** PIA API uses self-signed certificates.

**Solution:** HTTP clients use `InsecureSkipVerify: true` (equivalent to `curl -k`). This is intentional and matches bash implementation.

### 7. Metrics Mutex Deadlock

**Problem:** Updating metrics from health check while serving metrics can deadlock.

**Solution:** Keep mutex-protected sections minimal. Don't call external commands inside mutex.

### 8. Named Pipe Blocking

**Problem:** Writing to named pipe blocks if reader isn't active.

**Solution:** Write in goroutine or with timeout:
```bash
echo "message" > /tmp/pipe &
```

### 9. Reconnection During Port Forwarding Setup

**Problem:** Monitor triggers reconnection before port forwarding completes.

**Solution:** `run.sh` waits up to 30s for `/tmp/port_forwarding_complete` flag before starting monitor.

### 10. Real IP Capture Timing

**Problem:** Capturing real IP after VPN is up shows VPN IP.

**Solution:** Capture real IP once at first startup, save to `/tmp/real_ip`, reuse on reconnections.

---

## Testing Strategy

### Integration Tests (testing/test.sh)

**Tests:**
1. Image build verification
2. VPN connection (120s timeout)
3. IP address change verification
4. DNS leak detection
5. Killswitch leak test (10s stress test)
6. Port forwarding (60s timeout, if enabled)
7. Proxy connectivity (SOCKS5, HTTP)
8. Reconnection (trigger + 45s timeout)
9. Reconnection leak test (30s stress test)

**Configuration:**
- Set `DEBUG_PAUSE=true` to pause on failures
- Set `STRICT_MODE=true` to exit on first failure
- Adjust timeouts via environment variables

**Run:**
```bash
PIA_USER=p123 PIA_PASS=pass ./testing/test.sh
```

### Leak Tests (testing/leaktest/)

**Purpose:** Stress-test killswitch during VPN downtime.

**Protocols Tested:**
- HTTP (port 80)
- HTTPS (port 443)
- DNS (port 53 UDP)
- Random UDP (port 12345)
- Bypass routes (NIST time servers)

**Run:**
```bash
docker exec pia-tun /usr/local/bin/leaktest \
  -duration=60s \
  -concurrency=50 \
  -protocols=http,https,dns,udp,bypass \
  -real-ip="1.2.3.4" \
  -output=results.json
```

**Expected:** Zero successful connections to external IPs (except bypass routes).

### Manual Testing

```bash
# Check VPN status
docker exec pia-tun wg show pia

# Check firewall rules
docker exec pia-tun nft list ruleset
docker exec pia-tun iptables -L -n -v

# Check health
curl http://localhost:9090/metrics

# Test proxy
curl -x socks5://user:pass@localhost:1080 https://ifconfig.me

# Force reconnection
docker exec pia-tun pkill -USR1 monitor

# Check logs
docker logs -f pia-tun

# Debug mode
docker run -e LOG_LEVEL=debug pia-tun
```

---

## Key Dependencies

### Runtime (Alpine 3.19)
- `bash` - Shell for scripts
- `curl` - HTTP requests (PIA API, IP checks)
- `jq` - JSON parsing
- `ca-certificates` - TLS validation
- `wireguard-tools-wg` - WireGuard management (`wg`, `wg-quick`)
- `nftables` - Modern firewall (preferred)
- `iptables` - Legacy firewall (fallback)
- `iproute2-minimal` - Network routing (`ip` command)

### Go (v1.23.0)
- `github.com/prometheus/client_golang` - Metrics endpoint

**Note:** Go services use only standard library (except Prometheus).

---

## Build Optimization

### Multi-Stage Build

**Stage 1 (golang:1.23-alpine):**
- Build three binaries with CGO disabled
- Strip symbols (`-ldflags="-w -s"`)
- Remove path info (`-trimpath`)
- Mark executable in builder stage

**Stage 2 (alpine:3.19):**
- Install minimal runtime dependencies
- Copy binaries (already executable)
- Strip system executables
- Aggressive cleanup (APK cache, docs, man pages, Python)

**Result:** ~50MB final image (from ~1GB Go builder).

### Size Reduction Techniques

1. Static binaries (CGO_ENABLED=0)
2. Symbol stripping (-w -s)
3. Multi-stage build
4. Minimal base image (Alpine)
5. Single chmod in builder stage
6. Remove unnecessary packages
7. Strip system binaries

---

## Deployment Patterns

### Docker Compose Example

```yaml
services:
  pia-tun:
    image: x0lie/pia-tun:latest
    container_name: pia-tun
    cap_add:
      - NET_ADMIN
    cap_drop:
      - all
    environment:
      - PIA_LOCATION=ca_ontario,ca_toronto
      - PORT_FORWARDING=true
      - PROXY_ENABLED=true
      - PORT_API_TYPE=qbittorrent
      - PORT_API_URL=http://localhost:8080
      - LOCAL_NETWORK=192.168.1.0/24,172.17.0.0/16
    secrets:
      - pia_user
      - pia_pass
    ports:
      - 8080:8080

  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    network_mode: "service:pia-tun"
    depends_on:
      - pia-tun

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

### Kubernetes Considerations

- Must call `cleanup_killswitch` on shutdown
- Use init container pattern for dependent services
- Monitor via `/metrics` endpoint
- Use secrets for credentials
- Set resource limits (CPU/memory)

---

## Debugging Tips

### Enable Debug Logging

```bash
docker run -e LOG_LEVEL=debug pia-tun
```

### Check VPN Interface

```bash
docker exec pia-tun wg show pia
docker exec pia-tun ip addr show pia
docker exec pia-tun ip route show
```

### Check Firewall Rules

```bash
# nftables
docker exec pia-tun nft list ruleset

# iptables
docker exec pia-tun iptables -L -n -v
docker exec pia-tun iptables -t nat -L -n -v
```

### Check State Files

```bash
docker exec pia-tun cat /tmp/meta_cn          # Server name
docker exec pia-tun cat /tmp/client_ip        # Tunnel IP
docker exec pia-tun cat /tmp/pia_token        # Auth token
docker exec pia-tun cat /etc/wireguard/port   # Forwarded port
docker exec pia-tun cat /tmp/real_ip          # Pre-VPN IP
```

### Check Processes

```bash
docker exec pia-tun ps aux
docker exec pia-tun pgrep -a monitor
docker exec pia-tun pgrep -a portforward
docker exec pia-tun pgrep -a proxy
```

### Test Connectivity

```bash
# External IP
docker exec pia-tun curl -s https://ifconfig.me

# DNS
docker exec pia-tun nslookup google.com

# Proxy
curl -x socks5://localhost:1080 https://ifconfig.me
```

### View Metrics

```bash
# Prometheus format
curl http://localhost:9090/metrics

# JSON format
curl http://localhost:9090/metrics?format=json
```

---

## Git Workflow

**Branches:**
- `main` - Stable releases
- `develop` - Development branch (amd64 builds pushed)

**Commits:**
- Use conventional commit format
- Tag releases with `v1.0.0` format
- PRs trigger tests before merge

**Release Process:**
1. Merge develop → main
2. Tag with `git tag v1.0.0`
3. Push tag: `git push origin v1.0.0`
4. GitHub Actions builds multi-arch and publishes to Docker Hub

---

## Related Documentation

- **PROJECT_STRUCTURE.md** - Detailed file-by-file breakdown (30KB)
- **Dockerfile** - Build configuration with optimization notes
- **docker-compose.yml** - Example deployment
- **testing/test.sh** - Integration test suite
- **scripts/*.sh** - Shell script libraries with inline documentation

---

## Quick Reference

### File Locations

| Purpose | Path |
|---------|------|
| Main entrypoint | `/app/run.sh` |
| Go binaries | `/usr/local/bin/{proxy,monitor,portforward}` |
| WireGuard config | `/etc/wireguard/pia.conf` |
| Forwarded port | `/etc/wireguard/port` |
| PIA auth token | `/tmp/pia_token` |
| Server metadata | `/tmp/meta_cn`, `/tmp/client_ip`, `/tmp/pf_gateway` |
| Real IP | `/tmp/real_ip` |
| Status flags | `/tmp/{reconnecting,killswitch_up,port_forwarding_complete,pf_signature_failed}` |
| Named pipes | `/tmp/{vpn_reconnect_pipe,port_change_pipe}` |
| PID files | `/tmp/{proxy,portforward}.pid` |

### Common Tasks

| Task | Command |
|------|---------|
| View logs | `docker logs -f pia-tun` |
| Check VPN status | `docker exec pia-tun wg show pia` |
| Check firewall | `docker exec pia-tun nft list ruleset` |
| Test proxy | `curl -x socks5://localhost:1080 https://ifconfig.me` |
| View metrics | `curl http://localhost:9090/metrics` |
| Debug inside | `docker exec -it pia-tun bash` |
| Run tests | `PIA_USER=p123 PIA_PASS=pass ./testing/test.sh` |
| Build image | `docker build -t pia-tun:local .` |

### Port Reference

| Port | Service | Default |
|------|---------|---------|
| 1080 | SOCKS5 proxy | Configurable via `SOCKS5_PORT` |
| 8888 | HTTP proxy | Configurable via `HTTP_PROXY_PORT` |
| 9090 | Prometheus metrics | Configurable via `METRICS_PORT` |
| Dynamic | PIA forwarded port | Saved to `/etc/wireguard/port` |

---

## Additional Notes

### Performance Characteristics

- **Killswitch:** nftables O(1) lookups, iptables O(n)
- **Health checks:** 15s interval by default, parallel mode available
- **Port forwarding:** 15min bind interval, 31-day signature refresh
- **Reconnection:** 3 failures trigger reconnect, WAN backoff prevents flapping

### Security Posture

- Strict killswitch with zero leak windows
- Docker secrets preferred over environment variables
- Constant-time password comparison (timing attack prevention)
- TLS verification disabled for PIA API (intentional, matches bash)
- IPv6 blocked by default (prevent IPv6 leaks)

### Known Limitations

- No Go unit tests (only integration tests)
- Image name inconsistency in CI (olsonalexw/k8s-wireguard-pia vs x0lie/pia-tun)
- ARM builds removed from CI (see commit c7ae673)

---

**Last Updated:** December 11, 2025  
**For Questions:** Check PROJECT_STRUCTURE.md for detailed component documentation

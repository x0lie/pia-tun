# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**pia-tun** is a Docker container that provides a secure WireGuard VPN tunnel to Private Internet Access (PIA). The project aims to become the best and most downloaded image for containerized PIA clients, maximizing security, speed, and functionality.

Key features:
- WireGuard VPN with automatic reconnection
- Advanced killswitch using nftables (with iptables fallback)
- Port forwarding with automatic API updates to torrent clients
- SOCKS5 and HTTP proxy support
- Health monitoring with Prometheus metrics
- Zero-leak architecture during reconnection

## Architecture

### Component Structure

The codebase is organized into three main layers:

1. **Shell Scripts** (`/app/scripts/`): Core VPN management and orchestration
   - `run.sh`: Main entrypoint, orchestrates initial connection and reconnection loop
   - `vpn.sh`: PIA authentication, server selection, WireGuard configuration
   - `killswitch.sh`: Firewall management (nftables/iptables) with surgical exemptions
   - `port_forwarding.sh`: PF signature acquisition and binding
   - `port_monitor.sh`: Monitors forwarded port and updates torrent client APIs
   - `connectivity_check.sh`: External connectivity verification
   - `ui.sh`: Logging and UI utilities

2. **Go Applications** (`cmd/`): Performance-critical monitoring and proxy
   - `cmd/monitor/`: VPN health monitor with metrics collection
   - `cmd/proxy/`: SOCKS5/HTTP proxy implementation

3. **Docker**: Single container deployment with minimal attack surface

### Security Architecture

**Killswitch Implementation** (scripts/killswitch.sh):
- Three-layer defense:
  1. **Baseline killswitch**: Blocks all traffic except local networks and VPN
  2. **Bypass routing table (table 100)**: Routes specific IPs (WAN health checks) via eth0, bypassing VPN
  3. **Surgical exemptions**: Temporary firewall holes for PIA API calls during setup (DNS resolution, authentication)
- Auto-detects nftables (modern, O(1) lookups) or falls back to iptables
- No leak windows during reconnection

**Reconnection Flow**:
1. Health monitor detects failure → creates `/tmp/vpn_reconnect_requested`
2. Main loop (`run.sh`) detects flag → calls `perform_reconnection()`
3. VPN torn down → baseline killswitch remains active
4. New server selected → WireGuard reestablished → killswitch updated
5. Services (proxy, port forwarding) restarted

### Key Workflows

**Initial Connection** (`run.sh:initial_connect()`):
1. Capture real IP for leak detection
2. Setup baseline killswitch (blocks everything except local/VPN)
3. VPN setup: authenticate → select server → configure WireGuard → add surgical exemptions
4. Bring up WireGuard interface
5. Add VPN to killswitch (allow routing through tunnel)
6. Verify connection (DNS, IPv6, external IP)

**Port Forwarding** (scripts/port_forwarding.sh):
- Requests signature from PF gateway: `/getSignature?token=...`
- Binds port every 10 minutes (keepalive): `/bindPort` with signature+payload
- Signature refresh: Every 30 days or 24 hours before expiry (safety margin)
- On signature failure: Creates `/tmp/pf_signature_failed` → monitor triggers reconnect

**Health Monitoring** (cmd/monitor/main.go):
- Checks every `CHECK_INTERVAL` seconds (default 15s)
- Parallel checks: Interface status + connectivity (ping 1.1.1.1/8.8.8.8, HTTP)
- Failure threshold: `MAX_FAILURES` (default 3) consecutive failures → reconnect
- WAN checks use bypass routing (table 100) to detect internet outages without firewall changes
- Metrics endpoint on port 9090 (Prometheus format)

## Development Commands

### Building

```bash
# Build Docker image
docker build -t pia-tun .

# Multi-arch build (requires buildx)
docker buildx build --platform linux/amd64,linux/arm64 -t pia-tun .
```

### Running

```bash
# Run with docker-compose (recommended)
docker-compose up -d

# Run manually with required capabilities
docker run --rm -it \
  --cap-add=NET_ADMIN \
  --cap-drop=ALL \
  -e PIA_USER=p1234567 \
  -e PIA_PASS='password' \
  -e PIA_LOCATION=ca_ontario \
  -e PORT_FORWARDING=true \
  pia-tun

# Run with Docker secrets (more secure)
docker run --rm -it \
  --cap-add=NET_ADMIN \
  --cap-drop=ALL \
  --secret pia_user \
  --secret pia_pass \
  -e PIA_LOCATION=ca_ontario \
  pia-tun
```

### Testing and Debugging

```bash
# Enable debug logging
docker run ... -e LOG_LEVEL=debug pia-tun

# Check metrics
curl http://localhost:9090/metrics
curl http://localhost:9090/metrics?format=prometheus
curl http://localhost:9090/health

# Test killswitch (should block non-VPN traffic)
docker exec pia-tun nft list table inet vpn_filter
docker exec pia-tun ip rule show
docker exec pia-tun ip route show table 100

# Verify WireGuard status
docker exec pia-tun wg show pia

# Check for IP leaks
docker exec pia-tun curl ifconfig.me  # Should show VPN IP
docker exec pia-tun curl -6 ifconfig.co  # Should fail (IPv6 blocked)

# Test reconnection
docker exec pia-tun wg set pia peer $(docker exec pia-tun wg show pia peers) remove
```

### Working with Go Code

```bash
# Build Go binaries locally
cd cmd/monitor && go build -o monitor .
cd cmd/proxy && go build -o proxy .

# Run tests (if any exist)
go test ./...

# Format code
go fmt ./...
```

## Important Environment Variables

**Required:**
- `PIA_USER` / `/run/secrets/pia_user`: PIA username
- `PIA_PASS` / `/run/secrets/pia_pass`: PIA password
- `PIA_LOCATION`: Server location(s), comma-separated (e.g., `ca_ontario,ca_toronto`)

**Networking:**
- `LOCAL_NETWORK`: CIDR ranges for LAN access (e.g., `192.168.1.0/24,172.17.0.0/16`)
- `KILLSWITCH_EXEMPT_PORTS`: Ports exempt from killswitch (comma-separated)
- `DNS`: DNS server (`pia` or custom IP)
- `DISABLE_IPV6`: Block IPv6 (default: `true`)

**Port Forwarding:**
- `PORT_FORWARDING`: Enable port forwarding (`true`/`false`)
- `PORT_API_TYPE`: Auto-update torrent client (`qbittorrent`, `deluge`, `transmission`, `rtorrent`)
- `PORT_API_URL`: API endpoint (e.g., `http://localhost:8080`)
- `PORT_API_USER` / `PORT_API_PASS`: API credentials

**Proxy:**
- `PROXY_ENABLED`: Enable SOCKS5/HTTP proxy (`true`/`false`)
- `SOCKS5_PORT`: SOCKS5 port (default: 1080)
- `HTTP_PROXY_PORT`: HTTP proxy port (default: 8888)
- `PROXY_USER` / `PROXY_PASS`: Proxy authentication

**Monitoring:**
- `CHECK_INTERVAL`: Health check interval in seconds (default: 15)
- `MAX_FAILURES`: Failure threshold before reconnect (default: 3)
- `METRICS`: Enable Prometheus metrics (default: `false`)
- `METRICS_PORT`: Metrics server port (default: 9090)
- `LOG_LEVEL`: Logging verbosity (`info`, `debug`)

## Code Conventions

### Shell Scripts
- All scripts use `bash` with `set -e` (exit on error)
- Debug logging via `show_debug` (respects `LOG_LEVEL`)
- UI functions in `ui.sh`: `show_step`, `show_success`, `show_error`, `show_warning`
- Firewall functions prefixed with `nft_` (nftables) or `ipt_` (iptables)
- Temporary files in `/tmp/`: `pia_token`, `client_ip`, `meta_cn`, `pf_gateway`, etc.

### Go Code
- Standard Go project layout: `cmd/` for binaries
- No external dependencies (stdlib only)
- Optimized builds: `-ldflags="-w -s"` (strip debug info)
- Configuration via environment variables
- Logging to stderr with timestamps

### Dockerfile
- Multi-stage build: Go builder → Alpine runtime
- Minimal dependencies (bash, curl, jq, wireguard-tools, nftables, iptables)
- Executables marked in builder stage (prevents duplication)
- Requires `NET_ADMIN` capability, all others dropped

## Critical Implementation Details

### Killswitch Bypass Routes (scripts/killswitch.sh:48-68)
WAN health checks (NIST time servers) bypass VPN via routing table 100:
```bash
ip route add default via $gateway dev $interface table 100
ip rule add to 129.6.15.28 table 100 priority 50  # NIST time servers
```
This allows monitor to distinguish internet outages from VPN failures without firewall manipulation.

### Surgical Exemptions (scripts/killswitch.sh)
During VPN setup, temporary firewall holes are created for PIA API access:
- `add_temporary_exemption <ip> <port> <proto> <tag>`: Allow specific traffic
- `remove_temporary_exemption <tag>`: Remove exemption
- Tags: `dns_resolve`, `auth`, `api`, `pf_gateway`, etc.

### Port Forwarding Keepalive (scripts/port_forwarding.sh)
Signature expires if not bound within ~25 minutes. Script binds every 10 minutes:
```bash
BIND_INTERVAL=600  # 10 minutes
```
Signature refreshes 24 hours before 30-day expiry (safety margin).

### Reconnection Coordination
- `/tmp/vpn_reconnect_requested`: Monitor requests reconnection
- `/tmp/reconnecting`: Active reconnection in progress (skip health checks)
- `/tmp/pf_signature_failed`: Port forwarding signature failure → reconnect
- `/tmp/port_forwarding_complete`: Port forwarding setup finished

## Testing Recommendations

When modifying the codebase:

1. **Killswitch**: Always verify no leaks during reconnection
   ```bash
   # In one terminal: watch IP
   docker exec pia-tun watch -n 1 curl -s ifconfig.me
   # In another: force reconnection
   docker exec pia-tun touch /tmp/vpn_reconnect_requested
   ```

2. **Port Forwarding**: Test signature refresh and binding
   ```bash
   docker logs -f pia-tun | grep -i "port\|signature"
   ```

3. **Metrics**: Validate Prometheus output
   ```bash
   curl http://localhost:9090/metrics?format=prometheus | grep vpn_
   ```

4. **Container Size**: Check image size after changes
   ```bash
   docker images pia-tun --format "{{.Size}}"
   ```

## Dependent Services Pattern

For services that need to share the VPN network (e.g., qBittorrent):

```yaml
services:
  pia-tun:
    image: x0lie/pia-tun
    cap_add: [NET_ADMIN]
    cap_drop: [ALL]
    # ... config ...

  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent
    network_mode: "service:pia-tun"  # Share network namespace
    depends_on: [pia-tun]
```

**Note**: Docker does not support automatic dependent service restarts on VPN reconnection. Use `RESTART_SERVICES` environment variable or configure torrent clients to handle port changes gracefully.

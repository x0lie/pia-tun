# pia-tun

A feature-rich Docker container for Wireguard + Private Internet Access (PIA) VPN connectivity. Designed for security, reliability, and throughput.

![Title image](img/pia-tun-image.png)

## Installation

**Docker Hub:**
```bash
docker pull x0lie/pia-tun:latest
```

**GitHub Container Registry:**
```bash
docker pull ghcr.io/x0lie/pia-tun:latest
```

### Available Tags

| Tag | Description | Architectures | Updates | Use Case |
|-----|-------------|---------------|---------|----------|
| `latest` | Latest stable release | amd64, arm64, armv7 | With every release | General use |
| `1.2.3` | Specific patch version | amd64, arm64, armv7 | Never (immutable) | Production (pinned) |
| `1.2` | Latest patch in 1.2.x series | amd64, arm64, armv7 | With 1.2.x releases | Auto security updates |
| `1` | Latest minor in 1.x series | amd64, arm64, armv7 | With 1.x.x releases | Auto feature updates |
| `develop` | Per-commit from `develop` | amd64 only | Every commit | Development/Testing |

**Security Note:** Patch versions are released promptly when security updates are available. Automated dependency monitoring ensures timely updates for Alpine packages and Go dependencies.

## Features

- **WireGuard VPN** with automatic latency-based server selection
- **Kill-Switch Firewall** - Zero-leak architecture with nft/iptables auto-detection. Optimized for maximum throughput.
- **Port Forwarding** - Automatic signature management and keep-alive with torrent client API integration
- **Port Syncing** - Syncs forwarded port to an API endpoint
- **SOCKS5/HTTP Proxies** - Built-in dual-protocol proxy server with optional authentication
- **Prometheus Metrics** - Export health, connection, and performance metrics
- **Smart Health Monitoring** - Distinguishes between WAN outages and VPN failures
- **Local Network Access** - Configurable LAN routing with granular port control
- **High Throughput** - Tested at greater than 80% of line-speed with configurable MTU
- **Multi-Architecture** - Supports amd64, arm64, and armv7

## Quick Start

### Minimal Docker Compose

```yaml
services:
  pia-tun:
    image: x0lie/pia-tun:latest
    container_name: pia-tun
    cap_add:
      - NET_ADMIN
    cap_drop:
      - ALL
    environment:
      - PIA_LOCATION=ca_ontario
    secrets:
      - pia_user
      - pia_pass

  dependent-service:
    image: example
    name: vpn-user
    network_mode: "service:pia-tun" # Allows dependent to utilize VPN connection

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

### More Examples

See [`docs/`](docs/docker-compose%20examples/) for complete configurations:
- **[qbittorrent-compose.yml](docs/docker-compose%20examples/qbittorrent-compose.yml)** - qBittorrent with automatic port forwarding
- **[traefik-compose.yml](docs/docker-compose%20examples/traefik-simple-compose.yml)** - Basic Traefik reverse proxy setup

## Configuration

### VPN Settings

| Variable | Description | Default |
|----------|-------------|---------|
| `PIA_USER` | PIA username (or use `/run/secrets/pia_user`) | Required |
| `PIA_PASS` | PIA password (or use `/run/secrets/pia_pass`) | Required |
| `PIA_LOCATION` | Comma-separated locations (e.g., `ca_ontario,ca_toronto`). Tests latency and selects the best server. Invalid values will log all available regions. | Required |
| `TZ` | Timezone for logging | `UTC` |
| `LOG_LEVEL` | Logging verbosity: `error`, `info`, `debug` | `info` |

### Network & Firewall

| Variable | Description | Default |
|----------|-------------|---------|
| `LOCAL_NETWORKS` | CIDR ranges for LAN access, comma-separated. Supports both IPv4 and IPv6 (e.g., `172.18.0.0/16,fd00::/64`) | None |
| `LOCAL_PORTS` | Ports accessible from LAN (comma-separated) | None |
| `DNS` | DNS provider: `pia` (10.0.0.243), or specific IP (e.g., `8.8.8.8`). Routes through VPN tunnel unless the IP is in `LOCAL_NETWORKS` | `pia` |
| `IPV6_ENABLED` | Enable IPv6 routing to VPN interface. **Note:** PIA does not support IPv6. See IPv6 section below. | `false` |

**Local Network Behavior:**
- `LOCAL_NETWORKS` + `LOCAL_PORTS`: Bidirectional access on specified ports (expose services to LAN)
- `LOCAL_NETWORKS` only: Outbound access to LAN services, no listening ports exposed
- Neither configured: All traffic routed through VPN (maximum security)

**IPv6 Behavior:**

PIA does not currently support IPv6 over WireGuard. IPv6 internet traffic will not work regardless of settings.

| Setting | Local IPv6 | Internet IPv6 | ICMPv6 | Use Case |
|---------|-----------|---------------|--------|----------|
| `IPV6_ENABLED=false` (default) | ✓ Works via `LOCAL_NETWORKS` | ✗ Blocked | ✗ Blocked | Recommended - Local IPv6 only, internet blocked |
| `IPV6_ENABLED=true` | ✓ Works via `LOCAL_NETWORKS` | ✗ Routed to pia0 but PIA drops it | ✓ Allowed | Future-proofing for when PIA adds IPv6 support |

**Example - Local IPv6 access:**
```yaml
environment:
  - IPV6_ENABLED=false                      # Keep internet IPv6 blocked
  - LOCAL_NETWORKS=172.18.0.0/16,fd00::/64  # IPv4 + IPv6 local networks
  - LOCAL_PORTS=8080,9090
# Result: Container can access local IPv6 services, internet IPv6 is blocked
```

### Port Forwarding

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT_FORWARDING` | Enable PIA port forwarding. Automatically enabled when `PORT_SYNC_CLIENT` or `PORT_SYNC_CMD` is set. | `false` |
| `PORT_SYNC_CLIENT` | Torrent client type: `qbittorrent`, `transmission`, `deluge`, `rtorrent` | None |
| `PORT_SYNC_URL` | Client API endpoint (e.g., `http://localhost:8080`) | None |
| `PORT_SYNC_USER` | Client API username (or use `/run/secrets/port_sync_user`) | None |
| `PORT_SYNC_PASS` | Client API password (or use `/run/secrets/port_sync_pass`) | None |
| `PORT_SYNC_CMD` | Custom command for port updates (use `{PORT}` placeholder) | None |
| `PORT_FILE` | File to write forwarded port | `/run/pia-tun/port` |

### Proxy Settings

| Variable | Description | Default |
|----------|-------------|---------|
| `PROXY_ENABLED` | Enable SOCKS5/HTTP proxies (listen on localhost only by default) | `false` |
| `SOCKS5_PORT` | SOCKS5 listen port | `1080` |
| `HTTP_PROXY_PORT` | HTTP proxy listen port | `8888` |
| `PROXY_USER` | Proxy authentication username (or use `/run/secrets/proxy_user`) | None |
| `PROXY_PASS` | Proxy authentication password (or use `/run/secrets/proxy_pass`) | None |

**Note:** To expose proxy ports to your LAN, add them to `LOCAL_PORTS` (e.g., `LOCAL_PORTS=1080,8888`).

### Health Monitoring

| Variable | Description | Default |
|----------|-------------|---------|
| `HC_INTERVAL` | Health check frequency (seconds) | `15` |
| `HC_MAX_FAILURES` | Consecutive failures before reconnection | `3` |

### Metrics & Observability

| Variable | Description | Default |
|----------|-------------|---------|
| `METRICS` | Enable Prometheus metrics endpoint (listen on localhost only by default) | `true` |
| `METRICS_PORT` | Metrics server port | `9090` |

**Note:** To expose metrics to your LAN, add the port to `LOCAL_PORTS` (e.g., `LOCAL_PORTS=9090`).

**Endpoints:**
- `/metrics` - Prometheus format
- `/metrics?format=json` - JSON statistics

**Exported metrics:**
- Health check success/failure counts
- Reconnection counter
- Check duration histogram
- Connection status (up/down)
- Kill-switch active status
- WireGuard handshake timestamp
- Port forwarding status and port number
- Transfer bytes (RX/TX)
- Server latency and uptime

## Security

### Kill-Switch Protection

The firewall uses a DROP-first architecture optimized for security and throughput:
- **Zero leak windows**: DROP rule is established immediately and never removed
- **Optimized rule ordering**: Established/Related connections matched first for maximum performance
- **Auto-detection**: Automatically selects best available iptables backend (iptables-nft → iptables → iptables-legacy)
- **Default-deny**: All traffic blocked except loopback and VPN interface
- **Firewall persistence**: Rules remain active during reconnections
- **Local network control**: LAN access requires explicit `LOCAL_NETWORKS` configuration
- **Bypass routing**: WAN health checks use policy routing (no firewall exemptions)

**Container Lifecycle:**
- On startup: Kill-switch immediately applied
- During reconnections: Firewall remains active (zero leak window)
- On normal shutdown: Firewall rules cleanly removed after dependents stop
- On crash/OOM: Firewall remains active until network namespace destroyed

**Dependent Services:**

For containers sharing the network namespace (`network_mode: "service:pia-tun"`), use Docker Compose healthchecks to ensure the kill-switch is active before dependent services start. See the [Coordinating with Dependent Services](#coordinating-with-dependent-services) section for detailed examples and best practices.

**Important:** As long as you use `depends_on` with the killswitch healthcheck, you will never have a leak occur during startup.

### Secrets Management

**Docker secrets:**

```yaml
services:
  pia-tun:
    secrets:
      - pia_user
      - pia_pass
      - proxy_user
      - proxy_pass
      - port_sync_user
      - port_sync_pass

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
  proxy_user:
    file: ./secrets/proxy_user
  proxy_pass:
    file: ./secrets/proxy_pass
  port_sync_user:
    file: ./secrets/port_sync_user
  port_sync_pass:
    file: ./secrets/port_sync_pass
```

Secrets are read from `/run/secrets/` and never logged.

## Data Persistence

**This container is ephemeral by design.** No persistent storage is required for normal operation.

- WireGuard keys and configuration are regenerated on each container start
- Port forwarding automatically requests a new port from PIA
- All runtime state lives in memory or temporary filesystems

**Recommendation:** Most users should NOT use persistent volumes. Ephemeral operation provides:
- Fresh connections on every restart (more secure)
- No stale configuration issues
- Simpler troubleshooting
- Clean slate for debugging

## Coordinating with Dependent Services

### Monitoring Port Changes

When port forwarding is enabled, some dependent services may need to know when the port changes. Most modern clients handle port and interface changes during runtime well, but if your dependent is more brittle, several methods are available for restarting on port changes:

**1. Port File Monitoring**

Watch the port file for changes:
```bash
# Using inotifywait
inotifywait -m /run/pia-tun/port | while read; do
    NEW_PORT=$(cat /run/pia-tun/port)
    echo "Port changed to: $NEW_PORT"
    # Restart your service or update configuration
done
```

**2. Webhook Integration**

Use `PORT_SYNC_CMD` to POST to your own webhook service with {PORT}:
```yaml
environment:
  - PORT_SYNC_CMD=curl -s "http://localhost:8081/api/v2/app/setPreferences" --data "json={\"listen_port\":{PORT}}"
```

Your chosen API receives a POST request when the port changes, allowing you to:
- Restart dependent containers (via Docker API)
- Update load balancer configuration
- Trigger custom automation

**3. Docker Compose Healthcheck**

For coordinating initial startup only (not for liveness monitoring):
```yaml
services:
  pia-tun:
    healthcheck:
      test: ["CMD", "test", "-f", "/tmp/killswitch_up"]
      interval: 5s
      timeout: 3s
      retries: 3
      start_period: 30s

  dependent:
    network_mode: "service:pia-tun"
    depends_on:
      pia-tun:
        condition: service_healthy
```

**Important:** Do NOT use healthchecks as liveness probes that restart pia-tun. The container manages its own health monitoring and reconnection logic internally.

## Advanced Features

### Custom Port Update Commands

Execute custom commands when the port changes using `PORT_SYNC_CMD`. This works independently or alongside `PORT_SYNC_CLIENT`:

**Option 1: Custom command only**
```yaml
environment:
  - PORT_SYNC_CMD=/path/to/script.sh {PORT}
```

**Option 2: Both client sync AND custom command**
```yaml
environment:
  - PORT_SYNC_CLIENT=qbittorrent
  - PORT_SYNC_URL=http://localhost:8080
  - PORT_SYNC_CMD=/path/to/notify-webhook.sh {PORT}
```

The `{PORT}` placeholder is replaced with the forwarded port number. When a new port is obtained, the command retries indefinitely until successful.

### WAN Outage Handling

The health monitor distinguishes internet outages from VPN failures:
- Tests WAN connectivity via bypass routing
- Bypass routes are only to NIST servers on port 13
- Waits indefinitely for internet recovery with exponential backoff (5s → 160s)
- Prevents unnecessary reconnections and log spam during ISP downtime

## Troubleshooting

### Finding Available PIA Locations

The container will log all available regions if you provide an invalid location.

Alternatively, query the PIA server list directly:
```bash
curl -s 'https://serverlist.piaservers.net/vpninfo/servers/v6' | head -n -1 | jq -r '.regions[].id' | sort
```

### Verify VPN is Working

Check your IP is different from your real IP:
```bash
docker exec pia-tun curl -s ifconfig.me
```

View container logs:
```bash
docker logs pia-tun
```

### Common Issues

**Container exits immediately:**
- Check that `NET_ADMIN` capability is granted
- Verify PIA credentials are correct
- Check logs for authentication errors

**API port updater not reaching client:**
- Verify `PORT_SYNC_URL` is accessible from pia-tun container
- Check `PORT_SYNC_USER` and `PORT_SYNC_PASS` are correct
- Review logs for API communication errors

**Cannot access metrics/proxy from LAN:**
- Ensure the port is explicitly added to `LOCAL_PORTS` (e.g., `LOCAL_PORTS=9090` for metrics, `LOCAL_PORTS=1080,8888` for proxies)
- Services are localhost-only by default for security

## License

MIT License - See [LICENSE](LICENSE) for details

## Support

- **Issues:** https://github.com/x0lie/pia-tun/issues
- **Discussions:** https://github.com/x0lie/pia-tun/discussions

## Acknowledgments

Built with:
- [WireGuard](https://www.wireguard.com/) - Fast, modern VPN protocol
- [Private Internet Access](https://www.privateinternetaccess.com/) - VPN service provider
- [Alpine Linux](https://alpinelinux.org/) - Lightweight container base
- [Prometheus](https://prometheus.io/) - Metrics and monitoring

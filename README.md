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
- **Firewall** - Zero-leak with nftables and iptables fallback. Deny-by-default design
- **Port Forwarding** - Automatic signature management and keep-alive with torrent client API integration
- **Port Syncing** - Syncs forwarded port to an API endpoint
- **SOCKS5/HTTP Proxies** - Built-in dual-protocol proxy server with optional authentication
- **Prometheus Metrics** - Export health, connection, and performance metrics
- **Smart Health Monitoring** - Distinguishes between WAN outages and VPN failures
- **Local Network Access** - Configurable LAN routing with firewall exemptions
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

See [`docs/docker-compose examples/`](docs/docker-compose%20examples/) for complete configurations:
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
| `DNS` | DNS provider: `pia` (10.0.0.243), or specific IP (e.g., `8.8.8.8`) | `pia` |
| `IPV6_ENABLED` | Allow IPv6 traffic through VPN | `false` |
| `LOG_LEVEL` | Logging verbosity: `error`, `info`, `debug` | `info` |

### Network & Firewall

| Variable | Description | Default |
|----------|-------------|---------|
| `LOCAL_NETWORKS` | CIDR ranges for LAN access (comma-separated) | None |
| `LOCAL_PORTS` | Ports accessible from LAN (comma-separated) | None |

### Port Forwarding

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT_FORWARDING` | Enable PIA port forwarding. Automatically enabled when `PORT_SYNC_TYPE` is set. | `false` |
| `PORT_SYNC_TYPE` | Torrent client type: `qbittorrent`, `transmission`, `deluge`, `rtorrent`, `custom` | None |
| `PORT_SYNC_URL` | Client API endpoint (e.g., `http://localhost:8080`) | None |
| `PORT_SYNC_USER` | Client API username | None |
| `PORT_SYNC_PASS` | Client API password | None |
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

The firewall operates in default-deny mode:
- All traffic blocked except loopback and VPN interface
- Firewall persists during reconnections to prevent leaks
- Local network access requires explicit configuration
- WAN health checks use bypass routing (no firewall holes)

**Container Lifecycle:**
- On startup: Kill-switch activates before VPN connection
- During reconnections: Firewall remains active (no leak window)
- On normal shutdown: Firewall rules are cleanly removed after dependents are stopped
- On crash/OOM: Firewall remains active until network namespace is destroyed

**Dependent Services:**

For containers sharing the network namespace (`network_mode: "service:pia-tun"`), use Docker Compose healthchecks to ensure the kill-switch is active before dependent services start. See the [Coordinating with Dependent Services](#coordinating-with-dependent-services) section for detailed examples and best practices.

**Important:** As long as you use `depends_on` with the killswitch healthcheck, you're unlikely to ever have a leak occur during startup.

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

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
  proxy_user:
    file: ./secrets/proxy_user
  proxy_pass:
    file: ./secrets/proxy_pass
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

**1. Port File Monitoring (Simple)**

Watch the port file for changes:
```bash
# Using inotifywait
inotifywait -m /run/pia-tun/port | while read; do
    NEW_PORT=$(cat /run/pia-tun/port)
    echo "Port changed to: $NEW_PORT"
    # Restart your service or update configuration
done
```

**2. Webhook Integration (Recommended)**

Use `PORT_SYNC_TYPE=custom` to POST to your own webhook service:
```yaml
environment:
  - PORT_SYNC_TYPE=custom
  - PORT_SYNC_URL=http://your-service:8080/webhook/port-changed
```

Your webhook receives a POST request when the port changes, allowing you to:
- Restart dependent containers (via Docker API)
- Update load balancer configuration
- Trigger custom automation

**3. Docker Compose Healthcheck (Startup Coordination)**

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

For non-standard clients:

```yaml
environment:
  - PORT_SYNC_TYPE=custom
  - PORT_SYNC_CMD=/path/to/script.sh {PORT}
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

## Architecture

For detailed technical documentation, see [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md).

**Key Components:**
- **run.sh** - Main orchestration and lifecycle management
- **vpn.sh** - PIA authentication and WireGuard configuration
- **killswitch.sh** - Firewall (nftables/iptables hybrid)
- **monitor** (Go) - Health checks and Prometheus metrics
- **proxy** (Go) - SOCKS5/HTTP proxy server
- **portforward** (Go) - Port forwarding management

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

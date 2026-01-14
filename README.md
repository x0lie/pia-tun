# pia-tun

A feature-rich Docker container for Wireguard + Private Internet Access (PIA) VPN connectivity. Designed for security, reliability, and throughput.

![Title image](img/pia-tun-image.png)

## Installation

### Docker Registries

Available on both Docker Hub and GitHub Container Registry:

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

**Recommendations:**
- **Production (stable):** Pin to specific version (`1.2.3`)
- **Production (auto-updates):** Use minor version (`1.2`) for automatic security patches
- **Always latest stable:** Use `latest`
- **Testing unreleased features:** Use `develop`

**Security Note:** Patch versions are released promptly when security updates are available. Automated dependency monitoring ensures timely updates for Alpine packages and Go dependencies.

## Features

- **WireGuard VPN** with automatic latency-based server selection
- **Firewall** - Zero-leak with nftables and iptables fallback. Deny-by-default design
- **Port Forwarding** - Automatic signature management and keep-alive with torrent client API integration
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
| `DNS` | DNS provider: `pia` (PIA DNS), `custom` (Docker default), or specific IP (e.g., `8.8.8.8`) | `pia` |
| `DISABLE_IPV6` | Block IPv6 traffic | `true` |
| `LOG_LEVEL` | Logging verbosity: `error`, `info`, `debug` | `info` |

### Network & Firewall

| Variable | Description | Default |
|----------|-------------|---------|
| `LOCAL_NETWORK` | CIDR ranges for LAN access (comma-separated) | None |
| `LOCAL_PORTS` | Ports accessible from LAN (comma-separated) | None |

### Port Forwarding

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT_FORWARDING` | Enable PIA port forwarding. Automatically enabled when `PORT_API_TYPE` is set. | `false` |
| `PORT_API_TYPE` | Torrent client type: `qbittorrent`, `transmission`, `deluge`, `rtorrent`, `custom` | None |
| `PORT_API_URL` | Client API endpoint (e.g., `http://localhost:8080`) | None |
| `PORT_API_USER` | Client API username | None |
| `PORT_API_PASS` | Client API password | None |
| `PORT_API_CMD` | Custom command for port updates (use `{PORT}` placeholder) | None |
| `PORT_FILE` | File to write forwarded port | `/etc/wireguard/port` |

### Proxy Settings

| Variable | Description | Default |
|----------|-------------|---------|
| `PROXY_ENABLED` | Enable SOCKS5/HTTP proxies. Proxy ports are automatically accessible from LAN when enabled. | `false` |
| `SOCKS5_PORT` | SOCKS5 listen port. | `1080` |
| `HTTP_PROXY_PORT` | HTTP proxy listen port | `8888` |
| `PROXY_USER` | Proxy authentication username (or use `/run/secrets/proxy_user`) | None |
| `PROXY_PASS` | Proxy authentication password (or use `/run/secrets/proxy_pass`) | None |

### Health Monitoring

| Variable | Description | Default |
|----------|-------------|---------|
| `CHECK_INTERVAL` | Health check frequency (seconds) | `15` |
| `MAX_FAILURES` | Consecutive failures before reconnection | `3` |
| `MONITOR_PARALLEL_CHECKS` | Run connectivity tests in parallel | `true` |

### Metrics & Observability

| Variable | Description | Default |
|----------|-------------|---------|
| `METRICS` | Enable Prometheus metrics endpoint. Metrics port is automatically accessible from LAN when enabled. | `false` |
| `METRICS_PORT` | Metrics server port | `9090` |

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
For containers sharing the network namespace, you can coordinate
startup to ensure the kill-switch is active before dependent services begin:

```yaml
services:
  pia-tun:
    image: x0lie/pia-tun:latest
    # ... config ...
    healthcheck:
      test: ["CMD", "test", "-f", "/tmp/killswitch_up"]
      interval: 5s
      timeout: 3s
      retries: 3
      start_period: 30s

  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    network_mode: "service:pia-tun"
    depends_on:
      pia-tun:
        condition: service_healthy
```

**Important:** This healthcheck is ONLY for coordinating dependent service startup.
Do not use it, or others, as a liveness probe that restarts the container, as pia-tun manages
its own health monitoring internally. For manual restarts, restart pia-tun and
dependent services simultaneously, or use the proxy connection method which
doesn't require coordinated restarts. As long as you utilize dependsOn, you're unlikely to ever have a leak occur

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

## Advanced Features

### Custom Port Update Commands

For non-standard clients:

```yaml
environment:
  - PORT_API_TYPE=custom
  - PORT_API_CMD=/path/to/script.sh {PORT}
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
- Verify `PORT_API_URL` is accessible from pia-tun container
- Check `PORT_API_USER` and `PORT_API_PASS` are correct
- Review logs for API communication errors

**Cannot access metrics endpoint:**
- Ensure `METRICS=true` is set
- Metrics port is automatically accessible when enabled

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

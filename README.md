# pia-tun

A feature-rich Docker container for Wireguard + Private Internet Access (PIA) VPN connectivity. Designed for security, reliability, and throughput.

<img src="img/pia-tun-image.png" alt="Title image" width="65%">

## Features

- **WireGuard VPN** with automatic latency-based server selection
- **Kill-Switch Firewall** - Zero-leak architecture with nft/iptables auto-detection. Optimized for maximum throughput
- **Resilient Design** - Network failures do not break functionality
- **Port Forwarding** - Automatic signature management and keep-alive
- **Port Syncing** - Automatically syncs forwarded port to an API endpoint
- **SOCKS5/HTTP Proxies** - Built-in dual-protocol proxy server with optional authentication
- **Prometheus Metrics** - Export health, connection, and performance metrics
- **Smart Health Monitoring** - Distinguishes between WAN outages and VPN failures
- **Local Network Access** - Configurable LAN routing with granular port control
- **High Throughput** - Tested at greater than 95% of line-speed with configurable MTU
- **Multi-Architecture** - Supports amd64, arm64, and armv7

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

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

### More Examples

See [`docs/docker-compose-examples/`](docs/docker-compose-examples/) for more typical configurations:
- **[qbittorrent.yml](docs/docker-compose-examples/qbittorrent.yml)** - qBittorrent with automatic port forwarding
- **[traefik.yml](docs/docker-compose-examples/traefik.yml)** - Basic Traefik reverse proxy setup

### TL;DR Critical Points

- Do NOT use 9.9.9.9:853 or 9.9.9.11:853 in dependent DNS settings
- Do NOT use livenessProbes or docker restart policies
- Use `network_mode: "service:pia-tun"` for dependents
- Use `depends_on: pia-tun` for dependents
- Use `PS_CLIENT` for easy port updates for dependents
- Access from LAN requires `LOCAL_NETWORKS`
- Use Docker secrets for credentials

## Configuration

### VPN Settings

| Variable | Description | Default |
|----------|-------------|---------|
| `PIA_USER` | PIA username (or use `/run/secrets/pia_user`) | Required |
| `PIA_PASS` | PIA password (or use `/run/secrets/pia_pass`) | Required |
| `PIA_LOCATION` | Comma-separated locations (e.g., `ca_ontario,ca_toronto`). Tests latency and selects the best server. Invalid values will log all available regions. | Required |
| `TZ` | Timezone for logging | `UTC` |
| `LOG_LEVEL` | Logging verbosity: `error`, `info`, `debug` | `info` |
| `WG_BACKEND` | WireGuard implementation: `kernel` (faster) or `userspace` (wireguard-go). Auto-detected if not set. | Auto |
| `MTU` | Max Packet Size for the WireGuard Interface (pia0) | 1420 |

### Network & Firewall

| Variable | Description | Default |
|----------|-------------|---------|
| `LOCAL_NETWORKS` | CIDR ranges for LAN access, comma-separated. Supports both IPv4 and IPv6 (e.g., `172.18.0.0/16,fd00::/64`) | None |
| `DNS` | DNS provider: `pia` (10.0.0.243), or specific IP (e.g., `8.8.8.8`). Routes through VPN tunnel unless the IP is in `LOCAL_NETWORKS` | `pia` |
| `IPV6_ENABLED` | Enable IPv6 routing to VPN interface. **Note:** PIA does not yet support IPv6. See IPv6 section below. | `false` |
| `IPT_BACKEND` | iptables backend: `nft` or `legacy`. Auto-detected if not set. | Auto |

### Port Forwarding

| Variable | Description | Default |
|----------|-------------|---------|
| `PF_ENABLED` | Enable PIA port forwarding. Automatically enabled when `PS_CLIENT` or `PS_CMD` is set. | `false` |
| `PS_CLIENT` | Client type: `qbittorrent`, `transmission`, `deluge`, `rtorrent` | None |
| `PS_URL` | `PS_CLIENT` API endpoint. Auto-set to localhost:{default-port} based on PS_CLIENT setting. | Auto |
| `PS_USER` | Client API username | None |
| `PS_PASS` | Client API password | None |
| `PS_CMD` | Custom command for port updates (use `{PORT}` placeholder). Can be used alongside or instead of `PS_CLIENT` | None |
| `PORT_FILE` | File to write forwarded port | `/run/pia-tun/port` |

### Proxy Settings

| Variable | Description | Default |
|----------|-------------|---------|
| `PROXY_ENABLED` | Enable SOCKS5/HTTP proxies | `false` |
| `SOCKS5_PORT` | SOCKS5 listen port | `1080` |
| `HTTP_PROXY_PORT` | HTTP proxy listen port | `8888` |
| `PROXY_USER` | Proxy authentication username (or use `/run/secrets/proxy_user`) | None |
| `PROXY_PASS` | Proxy authentication password (or use `/run/secrets/proxy_pass`) | None |

### Health Monitoring

| Variable | Description | Default |
|----------|-------------|---------|
| `HC_INTERVAL` | Health check frequency (seconds) | `10` |
| `HC_FAILURE_WINDOW` | Time in seconds for disconnected state before reconnection | `30` |

### Metrics & Observability

| Variable | Description | Default |
|----------|-------------|---------|
| `METRICS_ENABLED` | Enable Prometheus metrics endpoint (listen on localhost only by default) | `true` |
| `METRICS_PORT` | Metrics server port | `9090` |

### Behavior

#### Local Network Access
- `LOCAL_NETWORKS`: Allows your LAN to reach internal services (proxy, metrics, dependent web UIs).
- You can include more than one CIDR range and/or specific IP addresses.
- Example: `LOCAL_NETWORKS=192.168.1.0/24, 172.17.0.2`

#### IPv6 Notes
- PIA does not currently support IPv6. IPv6 internet traffic will not work regardless of settings
- IPV6_ENABLED is simply future-proofing for PIA's eventual integration of IPv6 capability
- `LOCAL_NETWORKS` accepts IPv6 ranges and addresses, and functions regardless of IPV6_ENABLED

#### Port-syncing
- **Auto-detection**: `PS_URL` defaults to "http://localhost:{8080,9091,8112}" when `PS_CLIENT` set to qbittorrent/rtorrent, transmission, or deluge respectively.
- **Docker secrets**: Recommended for credentials to avoid exposure in environment variables.
- **localhost bypass**: For clients like qBit using `network_mode: "service:pia-tun"`, enabling "Bypass authentication for clients on localhost" in client settings eliminates the need for credentials.

#### Kill-Switch Protection

This container implements a strict firewall to prevent any traffic from leaking outside the VPN tunnel — even during startup, reconnects, crashes, or misconfigurations.

**Container Lifecycle:**
- On startup: Kill-switch immediately applied
- During reconnections: Firewall remains active (zero leak window)
- On normal shutdown: Firewall rules cleanly removed after dependents stop
- On crash/OOM: Firewall remains active until network namespace destroyed

**Smart Exemptions and DNS Handling:**
- Exemptions are specific: the PIA and DNS IPs on tcp port 443 and 853 respectively
- Exemptions are only used by necessity and only just before/after a request is made
- Caching process keeps fresh login token and PIA's IPs to avoid needing dns resolution for reconnects (uses 9.9.9.9 and 9.9.9.11 DoT to resolve)

**Critical:** Do NOT set your dependent services to use 9.9.9.9 or 9.9.9.11 on tcp port 853

For more details see [docs/firewall.md](docs/firewall.md).

#### Secrets Management

Docker secrets can be used for all credentials
- pia_user, pia_pass, ps_user, ps_pass, proxy_user and proxy_pass

Secrets are read from `/run/secrets/` and never logged.

#### Coordinating with Dependent Services

Some dependent services may not handle live port updates well. If your dependent is older or more brittle, several methods are available for restarting on port changes - see [`docs/dependent-restarts.md`](docs/dependent-restarts.md) for potential solutions.

For coordinating initial startup and shutdown ordering:
```yaml
services:
  pia-tun:

  dependent:
    network_mode: "service:pia-tun"
    depends_on:
      - pia-tun
```

Do NOT use livenessProbes or docker restart policies. These will restart pia-tun unnecessarily and fail to restart dependents, leaving them in a broken or leaking state.

#### Custom Port Update Commands

Execute custom commands when the port changes using `PS_CMD`. This works independently or alongside `PS_CLIENT`:

**Option 1: Custom command only**
```yaml
environment:
  - PS_CMD=/path/to/script.sh {PORT}
```

**Option 2: Both client sync AND custom command**
```yaml
environment:
  - PS_CLIENT=qbittorrent
  - PS_CMD=/path/to/notify-webhook.sh {PORT}
```

The `{PORT}` placeholder is replaced with the forwarded port number. When a new port is obtained, the command retries indefinitely until successful.

## Troubleshooting

See [`docs/troubleshooting.md`](docs/troubleshooting.md) for common issues and solutions.

## License

MIT License - See [LICENSE](LICENSE) for details

## Support

- **Issues:** https://github.com/x0lie/pia-tun/issues
- **Discussions:** https://github.com/x0lie/pia-tun/discussions

## Acknowledgments

Built with:
- [WireGuard](https://www.wireguard.com/) - Fast, modern VPN protocol
- [Prometheus](https://prometheus.io/) - Metrics and monitoring
- [Alpine Linux](https://alpinelinux.org/) - Lightweight container base

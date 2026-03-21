# pia-tun

A feature-rich Docker container for Wireguard + Private Internet Access (PIA) VPN connectivity. Designed for security, reliability, and throughput.

<img src="https://raw.githubusercontent.com/x0lie/pia-tun/main/img/pia-tun-image.png" alt="Title image" width="65%">

## Features

- **WireGuard VPN** with automatic latency-based server selection
- **Port Forwarding** - Automatic signature management and keep-alive
- **Port Syncing** - Automatically syncs forwarded port to an API endpoint
- **SOCKS5/HTTP Proxies** - Built-in dual-protocol proxy server with optional authentication
- **High Throughput** - Tested at greater than 95% of line-speed with automatic MSS Clamping
- **Kill-Switch Firewall** - Zero-leak architecture with nft/iptables auto-detection
- **Resilient Design** - Network failures do not break functionality
- **Smart Reconnects** - Distinguishes between WAN outages and VPN failures
- **Prometheus Metrics** - Export health, connection, and performance metrics
- **Local Network Access** - Configurable LAN routing with strong defaults
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

See [`docs/docker-compose-examples/`](https://github.com/x0lie/pia-tun/tree/main/docs/docker-compose-examples/) for more typical configurations:
- **[qbittorrent.yml](https://github.com/x0lie/pia-tun/blob/main/docs/docker-compose-examples/qbittorrent.yml)** - qBittorrent with automatic port forwarding
- **[traefik.yml](https://github.com/x0lie/pia-tun/blob/main/docs/docker-compose-examples/traefik.yml)** - Basic Traefik reverse proxy setup

### TL;DR Critical Points

- Do NOT use 9.9.9.9:853 or 9.9.9.11:853 (DoT) in dependent settings
- Do NOT use health-based restart policies
- Use `network_mode: "service:pia-tun"` for dependents
- Use `depends_on: pia-tun` for dependents
- Use `PS_CLIENT` for easy port syncing to dependents or `PS_SCRIPT` for custom endpoints
- Access from containers + LAN often requires `LOCAL_NETWORKS=auto,192.168.1.0/24` or similar

## Configuration

### VPN Settings

| Variable       | Description                                                                                           | Default  |
|----------------|-------------------------------------------------------------------------------------------------------|----------|
| `PIA_USER`     | PIA username (or use `/run/secrets/pia_user`)                                                         | Required |
| `PIA_PASS`     | PIA password (or use `/run/secrets/pia_pass`)                                                         | Required |
| `PIA_LOCATION` | Comma-separated locations (e.g., `ca_ontario,ca_toronto`). Tests latency and selects the best server. | `all`    |
| `LOG_LEVEL`    | Logging verbosity: `error`, `info`, `debug`, `trace`                                                  | `info`   |
| `WG_BACKEND`   | WireGuard implementation: `kernel` (faster) or `userspace` (wireguard-go). Auto-detected if not set.  | Auto     |
| `MTU`          | Max Packet Size for the WireGuard Interface (pia0)                                                    | 1420     |

### Network/Firewall

| Variable         | Description                                                                                                                    | Default |
|------------------|--------------------------------------------------------------------------------------------------------------------------------|---------|
| `LOCAL_NETWORKS` | CIDR ranges for tunnel bypass. Supports `auto`, IPv4 and IPv6 (e.g., `auto,192.168.1.0/24,fd00::/64`) or `all` or `none`       | `auto`  |
| `DNS`            | Supports `pia`, `system`, DoT (e.g., `tls://one.one.one.one,dns.mullvad.net`), or Do53 (e.g., `1.1.1.1,8.8.8.8`). Round-robin. | `pia`   |
| `IPT_BACKEND`    | iptables backend: `nft` or `legacy`. Auto-detected if not set.                                                                 | Auto    |

### Port Forwarding & Syncing

| Variable     | Description                                                                                                           | Default             |
|--------------|-----------------------------------------------------------------------------------------------------------------------|---------------------|
| `PF_ENABLED` | Enable PIA port forwarding. Automatically enabled when `PS_CLIENT` or `PS_SCRIPT` is set.                             | `false`             |
| `PS_CLIENT`  | Client type: `qbittorrent`, `transmission`, `deluge`                                                                  | None                |
| `PS_URL`     | `PS_CLIENT` API endpoint. Auto-set to localhost:{default-port} based on PS_CLIENT setting.                            | Auto                |
| `PS_USER`    | Client API username                                                                                                   | None                |
| `PS_PASS`    | Client API password                                                                                                   | None                |
| `PS_SCRIPT`  | Custom script executed after port refresh (use `{PORT}` placeholder). Can be used alongside or instead of `PS_CLIENT` | None                |
| `PORT_FILE`  | File to write forwarded port                                                                                          | `/run/pia-tun/port` |

### Proxy Settings

| Variable          | Description                                                      | Default |
|-------------------|------------------------------------------------------------------|---------|
| `PROXY_ENABLED`   | Enable SOCKS5/HTTP proxies                                       | `false` |
| `SOCKS5_PORT`     | SOCKS5 listen port                                               | `1080`  |
| `HTTP_PROXY_PORT` | HTTP proxy listen port                                           | `8888`  |
| `PROXY_USER`      | Proxy authentication username (or use `/run/secrets/proxy_user`) | None    |
| `PROXY_PASS`      | Proxy authentication password (or use `/run/secrets/proxy_pass`) | None    |

### Health Monitoring & Reconnects

| Variable            | Description                                                | Default |
|---------------------|------------------------------------------------------------|---------|
| `HC_INTERVAL`       | Health check frequency (seconds)                           | `10`    |
| `HC_FAILURE_WINDOW` | Time in seconds for disconnected state before reconnection | `30`    |

### Metrics & Observability

| Variable          | Description                                                              | Default |
|-------------------|--------------------------------------------------------------------------|---------|
| `METRICS_ENABLED` | Enable Prometheus metrics endpoint (listen on localhost only by default) | `true`  |
| `METRICS_PORT`    | Metrics server port                                                      | `9090`  |
| `INSTANCE_NAME`   | Prometheus label for users running more than one container               | None    |

### Behavior

#### Local Network Access
- `LOCAL_NETWORKS=auto` will allow bidirectional access on the network(s) it exists on.
- For many setups you will need something like `LOCAL_NETWORKS=auto,192.168.1.0/24` to access dependent web UI.
- `LOCAL_NETWORKS` accepts both private/public and IPv4/IPv6 CIDRs (single addresses need /32).
- If `DNS=pia` and `LOCAL_NETWORKS` includes PIA's DNS, DNS routes take priority (routes through tunnel).

#### Port-syncing
- **Auto-detection**: `PS_URL` defaults to "http://localhost:{8080,9091,8112}" when `PS_CLIENT` set to qbittorrent, transmission, or deluge respectively.
- **Docker secrets**: Recommended for credentials to avoid exposure in environment variables.
- **localhost bypass**: For clients like qBit using `network_mode: "service:pia-tun"`, enabling "Bypass authentication for clients on localhost" in client settings eliminates the need for credentials.

#### Kill-Switch Protection

This container implements a strict firewall to prevent any traffic from leaking outside the VPN tunnel — during startup, reconnects, crashes, or most misconfigurations.

**Container Lifecycle:**
- On startup: Kill-switch immediately applied (~25ms on amd64)
- During reconnections: Firewall remains active (zero leak window)
- On normal shutdown: Firewall rules cleanly removed after dependents stop
- On crash/OOM/unexpected-exit: Firewall remains active until network namespace destroyed

**Smart Exemptions and DNS Handling:**
- Exemptions are specific: the PIA and DNS IPs on tcp port 443 and 853 respectively
- Exemptions are only used by necessity and only for the duration required.
- Caching process keeps fresh login token and PIA's IPs to avoid needing dns resolution for reconnects (uses 9.9.9.9 and 9.9.9.11 DoT to resolve)

**Critical:** Do NOT set your dependent services to use 9.9.9.9 or 9.9.9.11 on tcp port 853 for the above reasons

For more details see [docs/firewall.md](https://github.com/x0lie/pia-tun/blob/main/docs/firewall.md).

#### Secrets Management

Docker secrets can be used for all credentials
- pia_user, pia_pass, ps_user, ps_pass, proxy_user and proxy_pass

Secrets are read from `/run/secrets/` and never logged.

#### Coordinating with Dependent Services

Some dependent services may not handle live port updates well. If your dependent is older or more brittle, several methods are available for restarting on port changes - see [`docs/dependent-restarts.md`](https://github.com/x0lie/pia-tun/blob/main/docs/dependent-restarts.md) for potential solutions.

For coordinating initial startup and shutdown ordering:
```yaml
services:
  pia-tun:

  dependent:
    network_mode: "service:pia-tun"
    depends_on:
      - pia-tun
```

Do NOT use livenessProbes or docker restart policies. Docker restart policies will restart pia-tun unnecessarily and fail to restart dependents, leaving them in a broken or even leaking state.

#### Custom Port Update Script

Execute custom scripts when the forwarded port changes using `PS_SCRIPT`. This works independently of `PS_CLIENT` (you can use both).
Can be useful as a webhook, or to update clients not supported by `PS_CLIENT`. Script will be retried indefinitely until success, like with `PS_CLIENT`.

**Using PS_SCRIPT**:
```yaml
environment:
  - PS_SCRIPT=/app/your-script.sh {PORT}
volumes:
  - ./your-script.sh:/app/your-script.sh
```

## Troubleshooting

See [`docs/troubleshooting.md`](https://github.com/x0lie/pia-tun/blob/main/docs/troubleshooting.md) for common issues and solutions.

## License

MIT License - See [LICENSE](https://github.com/x0lie/pia-tun/blob/main/LICENSE) for details

## Support

- **Issues:** https://github.com/x0lie/pia-tun/issues
- **Discussions:** https://github.com/x0lie/pia-tun/discussions

## Acknowledgments

Built with:
- [WireGuard](https://www.wireguard.com/) - Fast, modern VPN protocol
- [Prometheus](https://prometheus.io/) - Metrics and monitoring
- [Alpine Linux](https://alpinelinux.org/) - Lightweight container base

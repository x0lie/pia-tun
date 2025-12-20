# pia-tun

A Docker container for Wireguard + Private Internet Access (PIA) VPN connectivity. Designed for high throughput, automatic health monitoring, and seamless integration with torrent clients and other network-dependent services.

![Title image](img/pia-tun-image.png)

## Features

- **WireGuard VPN** for PIA clients with latency-based server selection
- **Firewall** - Zero-leak with nftables and iptables fallback. Deny-by-default design
- **Smart Server Selection** - Multi-location support with automatic latency-based selection
- **Port Forwarding** - Automatic signature management and keep-alive with torrent client API integration
- **SOCKS5/HTTP Proxies** - Built-in dual-protocol proxy server with optional authentication
- **Prometheus Metrics** - Export health, connection, and performance metrics
- **Smart Health Monitoring** - Distinguishes between WAN outages and VPN failures
- **Local Network Access** - Configurable LAN routing with firewall exemptions
- **High Throughput** - Tested at 800+ Mbps with configurable MTU
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
      - all
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

### Typical Docker Compose with qBittorrent

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
      - TZ=America/New_York
      - PIA_LOCATION=ca_ontario,ca_ontario-so,ca_toronto
      - PORT_FORWARDING=true
      - PORT_API_TYPE=qbittorrent
      - PORT_API_URL=http://localhost:8080
      - LOCAL_NETWORK=192.168.1.0/24,172.17.0.0/16
      - LOCAL_PORTS=8080
    secrets:
      - pia_user
      - pia_pass
    ports:
      - 8080:8080
    healthcheck:
      test: ["CMD", "test", "-f", "/tmp/killswitch_up"]
      interval: 5s
      timeout: 2s
      retries: 3
      start_period: 3s

  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent
    environment:
      - TZ=America/New_York
      - PUID=1000
      - PGID=1000
      - WEBUI_PORT=8080
    volumes:
      - ./qbittorrent/config:/config
      - ./qbittorrent/downloads:/downloads
    network_mode: "service:pia-tun"
    depends_on:
      pia-tun:
        condition: service_healthy

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

## Configuration

### VPN Settings

| Variable | Description | Default |
|----------|-------------|---------|
| `PIA_USER` | PIA username (or use `/run/secrets/pia_user`) | Required |
| `PIA_PASS` | PIA password (or use `/run/secrets/pia_pass`) | Required |
| `PIA_LOCATION` | Comma-separated locations (e.g., `ca_ontario,ca_toronto`). Tests latency and selects the best server. | Required |
| `TZ` | Timezone for logging | `UTC` |
| `DNS` | DNS provider: `pia` (default), `custom`, or specific IPs | `pia` |
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
| `PORT_FORWARDING` | Enable PIA port forwarding | `false` |
| `PORT_API_TYPE` | Torrent client type: `qbittorrent`, `transmission`, `deluge`, `rtorrent`, `custom` | None |
| `PORT_API_URL` | Client API endpoint (e.g., `http://localhost:8080`) | None |
| `PORT_API_USER` | Client API username | None |
| `PORT_API_PASS` | Client API password | None |
| `PORT_API_CMD` | Custom command for port updates (use `{PORT}` placeholder) | None |
| `PORT_FILE` | File to write forwarded port | `/etc/wireguard/port` |

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
| `CHECK_INTERVAL` | Health check frequency (seconds) | `15` |
| `MAX_FAILURES` | Consecutive failures before reconnection | `3` |
| `MONITOR_PARALLEL_CHECKS` | Run connectivity tests in parallel | `true` |

### Metrics & Observability

| Variable | Description | Default |
|----------|-------------|---------|
| `METRICS` | Enable Prometheus metrics endpoint | `false` |
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

## Kubernetes Deployment

### Basic Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pia-tun
spec:
  replicas: 1
  selector:
    matchLabels:
      app: pia-tun
  template:
    metadata:
      labels:
        app: pia-tun
    spec:
      containers:
      - name: pia-tun
        image: x0lie/pia-tun:latest
        securityContext:
          capabilities:
            add: ["NET_ADMIN"]
            drop: ["ALL"]
        env:
        - name: PIA_USER
          valueFrom:
            secretKeyRef:
              name: pia-credentials
              key: username
        - name: PIA_PASS
          valueFrom:
            secretKeyRef:
              name: pia-credentials
              key: password
        - name: PIA_LOCATION
          value: "ca_ontario"
        - name: PORT_FORWARDING
          value: "true"
        livenessProbe:
          exec:
            command: ["/app/scripts/healthcheck.sh"]
          initialDelaySeconds: 30
          periodSeconds: 60
        readinessProbe:
          exec:
            command: ["/app/scripts/healthcheck.sh"]
          initialDelaySeconds: 10
          periodSeconds: 10
```

## Security

### Kill-Switch Protection

The firewall operates in default-deny mode:
- All traffic blocked except loopback and VPN interface
- Kill-switch remains active during reconnections and after OOM kills
- Local network access requires explicit configuration
- WAN health checks use bypass routing (no firewall holes)

### Secrets Management

**Prefer Docker secrets over environment variables:**

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

The `{PORT}` placeholder is replaced with the forwarded port number.
Upon new port it will try indefinitely until success is achieved.

### WAN Outage Handling

The health monitor distinguishes internet outages from VPN failures:
- Tests WAN connectivity via bypass routing
- Bypass routes are only to NIST servers on port 13
- Waits for internet recovery with exponential backoff (5s → 160s)
- Prevents unnecessary reconnections during ISP downtime

## Architecture

For detailed technical documentation, see [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md).

**Key Components:**
- **run.sh** - Main orchestration and lifecycle management
- **vpn.sh** - PIA authentication and WireGuard configuration
- **killswitch.sh** - Firewall (nftables/iptables hybrid)
- **monitor** (Go) - Health checks and Prometheus metrics
- **proxy** (Go) - SOCKS5/HTTP proxy server
- **portforward** (Go) - Port forwarding management

## Building from Source

```bash
git clone https://github.com/x0lie/pia-tun.git
cd pia-tun
docker build -t pia-tun:local .
```

### Multi-Architecture Build

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64,linux/arm/v7 \
  -t x0lie/pia-tun:latest \
  --push .
```

## License

MIT License - See [LICENSE](LICENSE) for details

## Contributing

Contributions welcome! Please open an issue or pull request.

## Support

- **Issues:** https://github.com/x0lie/pia-tun/issues
- **Discussions:** https://github.com/x0lie/pia-tun/discussions

## Acknowledgments

Built with:
- [WireGuard](https://www.wireguard.com/) - Fast, modern VPN protocol
- [Private Internet Access](https://www.privateinternetaccess.com/) - VPN service provider
- [Alpine Linux](https://alpinelinux.org/) - Lightweight container base
- [Prometheus](https://prometheus.io/) - Metrics and monitoring

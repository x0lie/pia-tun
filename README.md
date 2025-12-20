# pia-tun

A Docker container for Wireguard + Private Internet Access (PIA) VPN connectivity. Designed for high throughput, automatic health monitoring, and seamless integration with torrent clients and other network-dependent services.

## Features

- **WireGuard VPN** with automatic PIA server authentication and configuration
- **Kill-Switch Firewall** - Zero-leak protection with nftables and iptables fallback. Deny-by-default design
- **Intelligent Server Selection** - Multi-location support with automatic latency-based routing
- **Port Forwarding** - Automatic signature management and keep-alive with torrent client API integration
- **SOCKS5/HTTP Proxies** - Built-in dual-protocol proxy server with optional authentication
- **Prometheus Metrics** - Export health, connection, and performance metrics
- **Smart Health Monitoring** - Distinguishes between WAN outages and VPN failures
- **Local Network Access** - Configurable LAN routing with firewall exemptions
- **High Throughput** - Tested at 800+ Mbps with configurable MTU
- **Multi-Architecture** - Supports amd64, arm64, and armv7

## Quick Start

### Using Docker Compose

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
      - PIA_LOCATION=ca_ontario,ca_toronto
      - PORT_FORWARDING=true
      - LOCAL_NETWORK=192.168.1.0/24
      - LOCAL_PORTS=8080
    secrets:
      - pia_user
      - pia_pass
    ports:
      - 8080:8080

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

### Using Docker CLI

```bash
docker run -d \
  --name pia-tun \
  --cap-add=NET_ADMIN \
  --cap-drop=all \
  -e PIA_USER="p1234567" \
  -e PIA_PASS="your_password" \
  -e PIA_LOCATION="ca_ontario" \
  -e PORT_FORWARDING=true \
  -e LOCAL_NETWORK="192.168.1.0/24" \
  -p 8080:8080 \
  x0lie/pia-tun:latest
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
| `HANDSHAKE_TIMEOUT` | WireGuard connection timeout (seconds) | `180` |

### Network & Firewall

| Variable | Description | Default |
|----------|-------------|---------|
| `LOCAL_NETWORK` | CIDR ranges for LAN access (comma-separated) | None |
| `LOCAL_PORTS` | Ports accessible from LAN (comma-separated) | None |

**Example:** Allow LAN access to qBittorrent WebUI:
```yaml
environment:
  - LOCAL_NETWORK=192.168.1.0/24,172.17.0.0/16
  - LOCAL_PORTS=8080
ports:
  - 8080:8080
```

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

**Supported Clients:**
- **qBittorrent** - Cookie-based authentication, preferences API
- **Transmission** - Session-based JSON-RPC
- **Deluge** - Web UI daemon JSON-RPC
- **rTorrent** - XML-RPC `network.port_range.set`
- **Custom** - Define your own update command

**Example with qBittorrent:**
```yaml
qbittorrent:
  image: lscr.io/linuxserver/qbittorrent:latest
  container_name: qbittorrent
  network_mode: "service:pia-tun"
  depends_on:
    - pia-tun

services:
  pia-tun:
    environment:
      - PORT_FORWARDING=true
      - PORT_API_TYPE=qbittorrent
      - PORT_API_URL=http://localhost:8080
      - PORT_API_USER=admin
      - PORT_API_PASS=adminpass
```

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

**Health checks:**
- WireGuard interface status
- External connectivity (ICMP ping to 1.1.1.1, 8.8.8.8)
- HTTP GET validation
- WAN detection (distinguishes internet outages from VPN failures)

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

**Example Prometheus scrape config:**
```yaml
scrape_configs:
  - job_name: 'pia-tun'
    static_configs:
      - targets: ['pia-tun:9090']
```

### Advanced Options

| Variable | Description | Default |
|----------|-------------|---------|
| `LOG_LEVEL` | Logging verbosity: `error`, `info`, `debug` | `info` |

## Usage with Torrent Clients

### qBittorrent

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
      - PORT_FORWARDING=true
      - PORT_API_TYPE=qbittorrent
      - PORT_API_URL=http://localhost:8080
      - PORT_API_USER=admin                                     # Or allow passwordless LAN access in qbit settings
      - PORT_API_PASS=adminpass
      - LOCAL_NETWORK=192.168.1.0/24
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

### Transmission

```yaml
pia-tun:
  environment:
    - PORT_API_TYPE=transmission
    - PORT_API_URL=http://localhost:9091
    - PORT_API_USER=transmission
    - PORT_API_PASS=password
```

### Deluge

```yaml
pia-tun:
  environment:
    - PORT_API_TYPE=deluge
    - PORT_API_URL=http://localhost:8112
    - PORT_API_PASS=deluge
```

### rTorrent

```yaml
pia-tun:
  environment:
    - PORT_API_TYPE=rtorrent
    - PORT_API_URL=http://localhost:8080
```

### Custom Client

```yaml
pia-tun:
  environment:
    - PORT_FORWARDING=true
    - PORT_API_TYPE=custom
    - PORT_API_CMD=curl -X POST http://myapp:8080/port -d port={PORT}
```

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

## Verification & Testing

### Check VPN Status

```bash
# View container logs
docker logs pia-tun

# Check WireGuard interface
docker exec pia-tun wg show pia

# Verify external IP (should be PIA's IP)
docker exec pia-tun curl https://ifconfig.me

# Check DNS
docker exec pia-tun nslookup google.com

# View forwarded port
docker exec pia-tun cat /etc/wireguard/port
```

### Test Proxies

```bash
# SOCKS5
curl -x socks5://localhost:1080 https://ifconfig.me

# HTTP
curl -x http://localhost:8888 https://ifconfig.me
```

### View Metrics

```bash
# Prometheus format
curl http://localhost:9090/metrics

# JSON format
curl http://localhost:9090/metrics?format=json
```

## Troubleshooting

### Container Exits Immediately

**Error:** `The container requires the NET_ADMIN capability to run`

**Solution:** Add `--cap-add=NET_ADMIN` or ensure `cap_add: [NET_ADMIN]` in compose file

---

**Error:** `Could not authenticate - Credential change or account expired?`

**Solution:** Verify PIA credentials are correct and account is active

### Cannot Access Local Network

**Problem:** LAN devices cannot reach services running in the container

**Solution:** Configure local network access:
```yaml
environment:
  - LOCAL_NETWORK=192.168.1.0/24
  - LOCAL_PORTS=8080
ports:
  - 8080:8080
```

### Port Forwarding Not Working

**Check port forwarding status:**
```bash
docker exec pia-tun cat /etc/wireguard/port
```

**Common issues:**
- Selected region doesn't support port forwarding (use different location)
- API credentials incorrect for torrent client

**Verify API updates:**
```bash
# Check logs for port update messages
docker logs pia-tun | grep -i "port"
```

### VPN Connection Drops

**Symptoms:** Frequent reconnections, unstable connection

**Diagnostics:**
```bash
# Check metrics for failure patterns
curl http://localhost:9090/metrics?format=json

# View reconnection count
docker logs pia-tun | grep -i "reconnect"
```

**Solutions:**
- Adjust `MAX_FAILURES` and `CHECK_INTERVAL` for your network conditions
- Try different `PIA_LOCATION` with lower latency
- Check if WAN is unstable (monitor distinguishes this automatically)

### DNS Resolution Fails

**Problem:** Cannot resolve domain names inside container

**Solution:** Try different DNS providers:
```yaml
environment:
  - DNS=custom  # Uses 1.1.1.1, 8.8.8.8
```

Or specify custom DNS:
```yaml
environment:
  - DNS=208.67.222.222,208.67.220.220
```

### Dependent Services Start Before VPN

**Problem:** Torrent client starts before kill-switch is active

**Solution:** Use healthcheck with depends_on:
```yaml
pia-tun:
  healthcheck:
    test: ["CMD", "test", "-f", "/tmp/killswitch_up"]
    interval: 5s
    timeout: 2s
    retries: 3
    start_period: 3s

qbittorrent:
  depends_on:
    pia-tun:
      condition: service_healthy
```

### IPv6 Leaks

**Problem:** IPv6 traffic bypassing VPN

**Solution:** IPv6 is blocked by default. If issues persist, verify:
```bash
docker exec pia-tun ip6tables -L -v
```

To allow IPv6 through VPN:
```yaml
environment:
  - DISABLE_IPV6=false
```

## Security

### Kill-Switch Protection

The firewall operates in default-deny mode:
- All traffic blocked except loopback and VPN interface
- Kill-switch remains active during reconnections and after OOM kills
- Local network access requires explicit configuration
- WAN health checks use routing table bypass (no firewall holes) and only allow connections to NIST servers on port 13

### Recommended Docker Configuration

```yaml
cap_add:
  - NET_ADMIN
cap_drop:
  - all
```

This minimizes attack surface by granting only the required capability.

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

## Performance

### Throughput

Tested performance:
- **Download:** over 800 mbps on a 940 mbps line

### Optimizations

- Efficient firewall rules tuned for throughput
- Lightweight Alpine base with go binary

## Advanced Features

### Custom Port Update Commands

For non-standard clients:

```yaml
environment:
  - PORT_API_TYPE=custom
  - PORT_API_CMD=/path/to/script.sh {PORT}
```

The `{PORT}` placeholder is replaced with the forwarded port number.

### WAN Outage Handling

The health monitor automatically detects internet outages:
- Uses bypass routing to test WAN connectivity
- Waits for internet recovery with exponential backoff (5s � 160s)
- Avoids unnecessary VPN reconnections and log spam during ISP downtime
- Logs WAN status changes

## Architecture

For detailed technical documentation, see [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md).

**Key Components:**
- **run.sh** - Main orchestration and lifecycle management
- **vpn.sh** - PIA authentication and WireGuard configuration
- **killswitch.sh** - Firewall (nftables/iptables hybrid)
- **monitor** (Go) - Health checks and Prometheus metrics
- **proxy** (Go) - SOCKS5/HTTP proxy server
- **portforward** (Go) - Port forwarding management and API updates

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

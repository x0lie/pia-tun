# PIA WireGuard Docker Container README Outline

> **Note on Google Search Results**: Google typically uses the **Project Title** as the search result title, and pulls the **meta description** or first paragraph under the title as the snippet/subsection. The first 155-160 characters are most important for SEO. Sections marked with `[SEO]` are prime for Google indexing.

---

## Project Title and Introduction `[SEO - Title Tag]`
**Title Options (Choose one that matches your Docker Hub/GitHub repo name):**
- "PIA WireGuard Docker - Secure VPN Container with Port Forwarding"
- "pia-tun: Private Internet Access VPN Container for Homelab"
- "Docker PIA VPN: WireGuard Container with qBittorrent Integration"

**Meta Description / First Paragraph `[SEO - Most Important 155 chars]`:**
```
A lightweight Docker container that routes traffic through Private Internet Access (PIA) 
using WireGuard. Features automatic port forwarding, killswitch protection, SOCKS5/HTTP 
proxies, and seamless integration with torrent clients (qBittorrent, Deluge, Transmission). 
Perfect for homelab privacy and security.
```

**Visual Elements:**
- Project logo or PIA + WireGuard + Docker badge
- Architecture diagram: Container → WireGuard Tunnel → Internet
- Status badges:
  - Docker pulls
  - Docker image size
  - Build status (GitHub Actions)
  - License (MIT/Apache/GPL)
  - Latest version tag

**Key Features Highlight (Bullet Points for Skimmers):**
- 🔒 **Automatic Killswitch**: Blocks all traffic if VPN disconnects
- ⚡ **WireGuard Protocol**: Faster than OpenVPN, lower latency
- 🔄 **Auto Port Forwarding**: Works with PIA's port forwarding API
- 🌐 **SOCKS5/HTTP Proxy**: Route other containers or devices through VPN
- 📊 **Prometheus Metrics**: Monitor VPN health, bandwidth, uptime
- 🐳 **Easy Integration**: Works with qBittorrent, Deluge, Transmission, rTorrent
- 🏠 **Homelab Ready**: Local network access, Docker Compose examples

---

## Quick Start (2 Minute Setup) `[SEO - "pia docker setup", "wireguard docker quick start"]`

### Prerequisites
- Docker and Docker Compose installed
- Active PIA subscription (get credentials from [privateinternetaccess.com](https://privateinternetaccess.com))
- For port forwarding: PIA account with port forwarding enabled

### Basic Setup (VPN Only)
**One-Command Docker Run:**
```bash
docker run -d \
  --name pia-vpn \
  --cap-add NET_ADMIN \
  --cap-drop all \
  -e PIA_USER=p1234567 \
  -e PIA_PASS=your_password \
  -e PIA_LOCATION=us_california \
  -e PORT_FORWARDING=false \
  x0lie/pia-tun:latest
```

**Verify Connection:**
```bash
# Check VPN status
docker logs pia-vpn

# Verify external IP changed
docker exec pia-vpn curl ifconfig.me
```

### Docker Compose with qBittorrent `[SEO - "qbittorrent docker pia", "qbittorrent vpn docker compose"]`
**File: `docker-compose.yml`**
```yaml
services:
  pia-vpn:
    image: x0lie/pia-tun:latest
    container_name: pia-vpn
    cap_add:
      - NET_ADMIN
    cap_drop:
      - all
    environment:
      - TZ=America/New_York
      - PIA_LOCATION=ca_ontario,ca_toronto,ca         # Comma-separated priority list
      - PORT_FORWARDING=true                           # Enable port forwarding
      - PORT_API_TYPE=qbittorrent                      # Auto-update qBittorrent port
      - PORT_API_URL=http://localhost:8080
      - LOCAL_NETWORK=192.168.1.0/24,172.17.0.0/16     # Allow LAN access
      - PROXY_ENABLED=true                             # Enable SOCKS5/HTTP proxy
    secrets:
      - pia_user
      - pia_pass
    ports:
      - 8080:8080        # qBittorrent WebUI
      - 1080:1080        # SOCKS5 proxy
      - 8888:8888        # HTTP proxy

  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=America/New_York
      - WEBUI_PORT=8080
    volumes:
      - ./qbittorrent/config:/config
      - ./qbittorrent/downloads:/downloads
    network_mode: "service:pia-vpn"                    # Route through VPN
    depends_on:
      - pia-vpn

secrets:
  pia_user:
    file: ./secrets/pia_user                           # Create file with PIA username
  pia_pass:
    file: ./secrets/pia_pass                           # Create file with PIA password
```

**Start Services:**
```bash
# Create secrets
mkdir -p secrets
echo "p1234567" > secrets/pia_user
echo "your_password" > secrets/pia_pass
chmod 600 secrets/*

# Start containers
docker-compose up -d

# Check logs
docker-compose logs -f pia-vpn
```

**Access qBittorrent:**
- WebUI: http://localhost:8080
- Default credentials: admin / adminadmin (change immediately!)
- Port will be auto-updated when VPN connects

---

## Why Use This Container? `[SEO - "why use pia docker", "vpn docker container benefits"]`

### For Homelab Users
- **Privacy First**: ISP cannot see torrent traffic, streaming sites, or browsing
- **IP Leak Protection**: Killswitch ensures no traffic escapes if VPN drops
- **Port Forwarding**: Automatic PIA port forwarding for better torrent performance
- **Container Isolation**: VPN runs separately, easy to restart without affecting other services
- **Local Network Access**: Still access Plex, NAS, Home Assistant, etc. on your LAN

### vs. VPN on Host Machine
| Feature | This Container | Host-Level VPN |
|---------|---------------|----------------|
| **Selective Routing** | ✅ Only specified containers | ❌ Entire system |
| **Easy Restart** | ✅ Just restart container | ❌ Reboot or reconnect manually |
| **No System Changes** | ✅ Isolated in container | ❌ Modifies host networking |
| **Resource Usage** | ✅ Minimal (~50MB RAM) | 🟡 Varies |
| **Kubernetes Ready** | ✅ Yes | ❌ No |

### vs. Gluetun and Other Containers
- **PIA Optimized**: Built specifically for PIA's WireGuard + port forwarding API
- **Faster Reconnects**: 2-minute signature retry, 20-second total recovery time
- **Port Forwarding**: Automatic updates to torrent clients (qBittorrent, Deluge, etc.)
- **Health Monitoring**: Built-in Go monitor with Prometheus metrics
- **Smaller Image**: ~50MB vs 100MB+ for multi-provider containers

---

## Features `[SEO - "pia docker features", "wireguard docker container features"]`

### Core VPN Features
- **WireGuard Protocol**: Modern, fast, secure VPN protocol (vs. slower OpenVPN)
- **Automatic Server Selection**: Latency-based server selection from location list
- **Killswitch (Firewall)**: nftables/iptables rules prevent IP leaks
- **DNS Leak Protection**: Forces all DNS through PIA's encrypted DNS servers
- **IPv6 Blocking**: Optionally disable IPv6 to prevent leaks
- **Split Tunneling**: Allow local network access while routing internet through VPN

### Port Forwarding `[SEO - "pia port forwarding docker", "automatic port forwarding container"]`
- **Automatic PIA Port Forwarding**: Uses PIA's official API (not all providers support this!)
- **Auto-Bind Refresh**: Keeps port alive with 15-minute bind refresh
- **Signature Management**: 31-day signature lifecycle with 24-hour safety margin
- **Torrent Client Integration**: Auto-updates qBittorrent, Deluge, Transmission, rTorrent
- **Failover Handling**: Escalates bind failures to signature refresh, triggers reconnect on API errors
- **Webhook Notifications**: Optional webhook when port changes (for dynamic DNS, etc.)

### Proxy Services
- **SOCKS5 Proxy**: Route non-containerized apps through VPN (port 1080)
- **HTTP/HTTPS Proxy**: Web traffic proxy with CONNECT support (port 8888)
- **Optional Authentication**: Secure proxies with username/password
- **Container Access**: Other containers can use VPN via proxy without network_mode

### Monitoring & Metrics `[SEO - "prometheus vpn metrics", "vpn docker monitoring"]`
- **Health Checks**: Interface status, DNS resolution, external IP verification
- **Prometheus Endpoint**: /metrics on port 9090
- **Grafana Compatible**: Import dashboard for VPN stats
- **Metrics Include**:
  - VPN connection status (up/down)
  - Health check success rate
  - Bandwidth (RX/TX bytes)
  - Server latency
  - Port forwarding status
  - Reconnection count
  - WireGuard handshake age
  - WAN connectivity (distinguishes VPN vs internet outages)

### Reliability Features
- **Automatic Reconnection**: Detects failures and reconnects without manual intervention
- **WAN Detection**: Won't reconnect during internet outages (waits for WAN to return)
- **Exponential Backoff**: Smart retry logic for API calls
- **Service Restart**: Optionally restart dependent containers after reconnect
- **Graceful Shutdown**: Cleans up firewall rules (critical for Kubernetes)

---

## Configuration Reference `[SEO - "pia docker environment variables", "pia-tun configuration"]`

### Required Variables
| Variable | Description | Example |
|----------|-------------|---------|
| `PIA_USER` | PIA username (p-numbers) | `p1234567` |
| `PIA_PASS` | PIA password | `your_password` |

**Alternative: Docker Secrets (Recommended)**
```yaml
secrets:
  pia_user:
    file: ./secrets/pia_user    # Create file: echo "p1234567" > secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass    # Create file: echo "password" > secrets/pia_pass
```

### Server Selection
| Variable | Description | Default |
|----------|-------------|---------|
| `PIA_LOCATION` | Comma-separated server priority list | `us_california` |

**Finding Server Names:**
```bash
# List all PIA servers
curl -s https://serverlist.piaservers.net/vpninfo/servers/v6 | jq -r '.regions[].id' | sort

# Common servers:
# - us_california, us_east, us_texas, us_seattle
# - ca_toronto, ca_ontario, ca_vancouver
# - uk_london, uk_manchester
# - de_frankfurt, nl_amsterdam, ch_zurich
# - au_sydney, au_melbourne
# - jp_tokyo, sg_singapore, hk
```

**Server Selection Strategy:**
- List multiple servers: `PIA_LOCATION=ca_ontario,ca_toronto,ca_vancouver`
- Container tests latency and picks fastest
- Failover if first server is unavailable

### Port Forwarding
| Variable | Description | Default |
|----------|-------------|---------|
| `PORT_FORWARDING` | Enable PIA port forwarding | `false` |
| `PORT_API_TYPE` | Torrent client type | *(empty)* |
| `PORT_API_URL` | API endpoint for port updates | *(empty)* |
| `PORT_API_USER` | API username | *(empty)* |
| `PORT_API_PASS` | API password | *(empty)* |
| `PORT_API_CMD` | Custom command for port update | *(empty)* |
| `WEBHOOK_URL` | Webhook for port change notifications | *(empty)* |

**Supported Torrent Clients (`PORT_API_TYPE`):**
- `qbittorrent` - qBittorrent WebUI API
- `deluge` - Deluge JSON-RPC
- `transmission` - Transmission RPC
- `rtorrent` - rTorrent XML-RPC
- `custom` - Use `PORT_API_CMD` with `{PORT}` placeholder

**Example: qBittorrent**
```yaml
environment:
  - PORT_FORWARDING=true
  - PORT_API_TYPE=qbittorrent
  - PORT_API_URL=http://localhost:8080
  - PORT_API_USER=admin              # Optional if auth enabled
  - PORT_API_PASS=adminadmin         # Optional if auth enabled
```

**Example: Custom Command**
```yaml
environment:
  - PORT_FORWARDING=true
  - PORT_API_TYPE=custom
  - PORT_API_CMD=curl -X POST http://myservice.local/api/port -d '{"port":{PORT}}'
```

### Network Configuration
| Variable | Description | Default |
|----------|-------------|---------|
| `LOCAL_NETWORK` | CIDR ranges for LAN access | *(empty)* |
| `LOCAL_PORTS` | Ports accessible from LAN | *(empty)* |
| `DISABLE_IPV6` | Block IPv6 traffic | `true` |
| `DNS` | DNS servers (comma-separated) | `pia` |

**Examples:**
```yaml
# Allow LAN access to container
LOCAL_NETWORK: "192.168.1.0/24,172.17.0.0/16"

# Allow LAN access to specific ports (WebUI, proxy, etc.)
LOCAL_PORTS: "8080,1080,8888,9090"

# Use custom DNS
DNS: "1.1.1.1,8.8.8.8"

# Use PIA DNS (recommended for privacy)
DNS: "pia"
```

### Proxy Configuration
| Variable | Description | Default |
|----------|-------------|---------|
| `PROXY_ENABLED` | Enable SOCKS5/HTTP proxies | `false` |
| `SOCKS5_PORT` | SOCKS5 proxy port | `1080` |
| `HTTP_PROXY_PORT` | HTTP proxy port | `8888` |
| `PROXY_USER` | Proxy username | *(empty)* |
| `PROXY_PASS` | Proxy password | *(empty)* |

**Using Proxies:**
```bash
# SOCKS5 proxy (for curl, Firefox, etc.)
curl --socks5 localhost:1080 ifconfig.me

# HTTP proxy (for wget, browsers, etc.)
export http_proxy=http://localhost:8888
export https_proxy=http://localhost:8888
curl ifconfig.me
```

**With Authentication:**
```yaml
environment:
  - PROXY_ENABLED=true
  - PROXY_USER=myuser
  - PROXY_PASS=mypassword
secrets:
  proxy_user:
    file: ./secrets/proxy_user
  proxy_pass:
    file: ./secrets/proxy_pass
```
```bash
# Access with auth
curl --socks5 myuser:mypassword@localhost:1080 ifconfig.me
```

### Monitoring & Metrics
| Variable | Description | Default |
|----------|-------------|---------|
| `METRICS` | Enable Prometheus metrics | `false` |
| `METRICS_PORT` | Metrics endpoint port | `9090` |
| `CHECK_INTERVAL` | Health check interval (seconds) | `15` |
| `MAX_FAILURES` | Failures before reconnect | `3` |
| `MONITOR_PARALLEL_CHECKS` | Parallel health checks | `true` |

**Accessing Metrics:**
```bash
# Prometheus format
curl http://localhost:9090/metrics

# JSON format
curl http://localhost:9090/metrics?format=json
```

### Advanced Options
| Variable | Description | Default |
|----------|-------------|---------|
| `LOG_LEVEL` | Logging verbosity (error/info/debug) | `info` |
| `TZ` | Container timezone | `UTC` |
| `HANDSHAKE_TIMEOUT` | WireGuard handshake timeout (s) | `180` |
| `RESTART_SERVICES` | Containers to restart after reconnect | *(empty)* |

**Example: Restart Dependent Services**
```yaml
environment:
  - RESTART_SERVICES=qbittorrent,sonarr,radarr
```

---

## Usage Examples

### Example 1: Basic VPN (No Port Forwarding)
**Use Case:** Just want to browse with VPN, no torrent port forwarding needed.

```bash
docker run -d \
  --name pia-vpn \
  --cap-add NET_ADMIN \
  --cap-drop all \
  -e PIA_USER=p1234567 \
  -e PIA_PASS=yourpassword \
  -e PIA_LOCATION=us_california \
  x0lie/pia-tun:latest

# Test VPN
docker exec pia-vpn curl ifconfig.me
```

### Example 2: qBittorrent with Port Forwarding `[SEO]`
**Use Case:** Torrent client with automatic port forwarding for better speeds.

See **Quick Start** section above for full docker-compose.yml.

### Example 3: Deluge with Port Forwarding
```yaml
services:
  pia-vpn:
    image: x0lie/pia-tun:latest
    cap_add: [NET_ADMIN]
    cap_drop: [all]
    environment:
      - PIA_LOCATION=nl_amsterdam,de_frankfurt
      - PORT_FORWARDING=true
      - PORT_API_TYPE=deluge
      - PORT_API_URL=http://localhost:8112/json
      - PORT_API_PASS=deluge              # Deluge WebUI password
      - LOCAL_NETWORK=192.168.1.0/24
    secrets:
      - pia_user
      - pia_pass
    ports:
      - 8112:8112

  deluge:
    image: lscr.io/linuxserver/deluge:latest
    environment:
      - PUID=1000
      - PGID=1000
    volumes:
      - ./deluge/config:/config
      - ./deluge/downloads:/downloads
    network_mode: "service:pia-vpn"
    depends_on:
      - pia-vpn

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

### Example 4: Transmission with Port Forwarding
```yaml
services:
  pia-vpn:
    image: x0lie/pia-tun:latest
    cap_add: [NET_ADMIN]
    cap_drop: [all]
    environment:
      - PIA_LOCATION=ca_toronto
      - PORT_FORWARDING=true
      - PORT_API_TYPE=transmission
      - PORT_API_URL=http://localhost:9091/transmission/rpc
      - PORT_API_USER=admin              # If auth enabled
      - PORT_API_PASS=password
    secrets:
      - pia_user
      - pia_pass
    ports:
      - 9091:9091

  transmission:
    image: lscr.io/linuxserver/transmission:latest
    volumes:
      - ./transmission/config:/config
      - ./downloads:/downloads
    network_mode: "service:pia-vpn"
    depends_on:
      - pia-vpn

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

### Example 5: Multiple Containers via Proxy (No network_mode)
**Use Case:** Route Firefox, wget, or other apps through VPN without sharing network namespace.

```yaml
services:
  pia-vpn:
    image: x0lie/pia-tun:latest
    cap_add: [NET_ADMIN]
    cap_drop: [all]
    environment:
      - PIA_LOCATION=uk_london
      - PROXY_ENABLED=true
      - PROXY_USER=myuser
      - PROXY_PASS=mypassword
    secrets:
      - pia_user
      - pia_pass
    ports:
      - 1080:1080        # SOCKS5
      - 8888:8888        # HTTP

  firefox:
    image: jlesage/firefox
    environment:
      # Configure Firefox to use SOCKS5 proxy
      - FIREFOX_PROXY=socks5://myuser:mypassword@pia-vpn:1080
    depends_on:
      - pia-vpn

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

**Or use environment variables in any container:**
```bash
docker run -it --rm \
  -e http_proxy=http://pia-vpn:8888 \
  -e https_proxy=http://pia-vpn:8888 \
  alpine sh -c "apk add curl && curl ifconfig.me"
```

### Example 6: Prometheus + Grafana Monitoring
```yaml
services:
  pia-vpn:
    image: x0lie/pia-tun:latest
    cap_add: [NET_ADMIN]
    cap_drop: [all]
    environment:
      - PIA_LOCATION=us_east
      - METRICS=true
      - METRICS_PORT=9090
    secrets:
      - pia_user
      - pia_pass
    ports:
      - 9090:9090

  prometheus:
    image: prom/prometheus
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
    ports:
      - 9091:9090

  grafana:
    image: grafana/grafana
    ports:
      - 3000:3000
    depends_on:
      - prometheus

secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

**prometheus.yml:**
```yaml
scrape_configs:
  - job_name: 'pia-vpn'
    static_configs:
      - targets: ['pia-vpn:9090']
```

**View Metrics:**
- Prometheus: http://localhost:9091
- Grafana: http://localhost:3000 (admin/admin)
- Add Prometheus data source: http://prometheus:9090
- Create dashboard with metrics like `vpn_connection_up`, `vpn_bytes_received_total`, etc.

### Example 7: Kubernetes Deployment
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pia-vpn
spec:
  replicas: 1
  selector:
    matchLabels:
      app: pia-vpn
  template:
    metadata:
      labels:
        app: pia-vpn
    spec:
      containers:
      - name: pia-vpn
        image: x0lie/pia-tun:latest
        securityContext:
          capabilities:
            add: ["NET_ADMIN"]
            drop: ["all"]
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
          value: "us_california"
        - name: PORT_FORWARDING
          value: "true"
        - name: METRICS
          value: "true"
        ports:
        - containerPort: 9090
          name: metrics
---
apiVersion: v1
kind: Secret
metadata:
  name: pia-credentials
type: Opaque
stringData:
  username: p1234567
  password: yourpassword
```

---

## Security Considerations

### Killswitch Protection
- **Firewall-Based**: Uses nftables/iptables with DROP policy
- **Default Deny**: All traffic blocked except VPN tunnel and local network
- **Leak Prevention**: Even if VPN disconnects, no traffic escapes
- **Verification**: Health monitor checks firewall rules are active

**How It Works:**
1. Container starts → Killswitch activates (blocks all traffic)
2. VPN connects → Firewall allows traffic through WireGuard interface
3. VPN disconnects → Firewall reverts to blocking (killswitch active)
4. Health monitor detects failure → Triggers reconnection

### DNS Leak Protection
- **Forced DNS**: All DNS queries routed through PIA's encrypted DNS servers
- **No System DNS**: Container ignores host machine's DNS settings
- **Validation**: Startup checks confirm PIA DNS is being used
- **Custom DNS**: Can override with `DNS=1.1.1.1,8.8.8.8` if preferred

### Credential Security
- **Docker Secrets (Recommended)**: Store credentials in files, not environment variables
- **No Plaintext Logs**: Passwords never logged (even in debug mode)
- **File Permissions**: Secrets should be chmod 600
- **Environment Variables**: Use only for testing (visible in `docker inspect`)

**Best Practice:**
```bash
# Create secrets directory
mkdir -p secrets
chmod 700 secrets

# Store credentials
echo "p1234567" > secrets/pia_user
echo "yourpassword" > secrets/pia_pass
chmod 600 secrets/*

# Use in docker-compose.yml
secrets:
  pia_user:
    file: ./secrets/pia_user
  pia_pass:
    file: ./secrets/pia_pass
```

### Container Isolation
- **Minimal Capabilities**: Only `NET_ADMIN` required, drops all others
- **No Privileged Mode**: Runs as unprivileged container
- **Read-Only Filesystem**: Optionally run with `--read-only` (requires tmpfs mounts)
- **User Namespace**: Compatible with Docker user namespaces

### IPv6 Leak Prevention
- **Default Disabled**: `DISABLE_IPV6=true` blocks all IPv6 traffic
- **Leak Testing**: Health monitor checks for IPv6 leaks on startup
- **Dual-Stack Option**: Can enable IPv6 if PIA server supports it

### Firewall Cleanup
- **Graceful Shutdown**: Removes all firewall rules on container stop
- **Kubernetes Safe**: Prevents orphaned rules in reused network namespaces
- **Signal Handling**: Catches SIGTERM, SIGINT, SIGQUIT for cleanup

---

## Troubleshooting `[SEO - "pia docker troubleshooting", "wireguard docker not working"]`

### VPN Won't Connect

**Symptom:** Container starts but VPN never connects, or "Authentication failed" error.

**Solutions:**
1. **Check PIA Credentials:**
   ```bash
   # View your credentials (should be p-numbers format)
   cat secrets/pia_user    # Should output: p1234567
   cat secrets/pia_pass
   
   # Test authentication manually
   docker exec pia-vpn curl -s -u "p1234567:yourpassword" \
     "https://www.privateinternetaccess.com/gtoken/generateToken"
   # Should return: {"token":"your-token-here"}
   ```

2. **Verify PIA Account is Active:**
   - Login to [privateinternetaccess.com](https://privateinternetaccess.com)
   - Check subscription status (expired accounts won't authenticate)

3. **Check Server Name:**
   ```bash
   # List valid server IDs
   curl -s https://serverlist.piaservers.net/vpninfo/servers/v6 | jq -r '.regions[].id' | grep -i "california"
   
   # Use exact ID from list
   PIA_LOCATION=us_california
   ```

4. **Enable Debug Logging:**
   ```yaml
   environment:
     - LOG_LEVEL=debug
   ```
   ```bash
   docker-compose up -d
   docker-compose logs -f pia-vpn
   ```

### Port Forwarding Not Working

**Symptom:** `PORT_FORWARDING=true` but no port assigned, or port not updating in torrent client.

**Solutions:**
1. **Check Server Supports Port Forwarding:**
   - Not all PIA servers support port forwarding!
   - Container auto-filters servers, but if your location has none, it will fail
   - Use servers in: US, CA, UK, DE, NL, CH, AU, etc.
   - Avoid: Asia servers often don't support port forwarding

2. **Verify Port File:**
   ```bash
   docker exec pia-vpn cat /etc/wireguard/port
   # Should output: 12345 (your port number)
   ```

3. **Check Port Forwarding Logs:**
   ```bash
   docker-compose logs -f pia-vpn | grep -i "port"
   # Look for: "Port forwarding active: 12345"
   ```

4. **Test API Connection:**
   ```bash
   # qBittorrent example
   docker exec pia-vpn curl -v http://localhost:8080/api/v2/app/preferences
   # Should return JSON with "listen_port" field
   ```

5. **Manual Port Update:**
   ```bash
   # Get current port
   PORT=$(docker exec pia-vpn cat /etc/wireguard/port)
   
   # Update qBittorrent manually
   docker exec pia-vpn curl -X POST \
     "http://localhost:8080/api/v2/app/setPreferences" \
     -d "json={\"listen_port\":$PORT}"
   ```

### No Internet Access / Killswitch Blocking Everything

**Symptom:** Container starts but health checks fail, or "No connectivity" messages.

**Solutions:**
1. **Check Killswitch Status:**
   ```bash
   docker exec pia-vpn nft list ruleset
   # Look for: "iifname pia accept" (VPN allowed)
   ```

2. **Verify WireGuard Interface:**
   ```bash
   docker exec pia-vpn wg show
   # Should show peer, endpoint, handshake time
   
   docker exec pia-vpn ip addr show pia
   # Should show IP address assigned by PIA
   ```

3. **Test DNS Resolution:**
   ```bash
   docker exec pia-vpn cat /etc/resolv.conf
   # Should show: nameserver 10.0.0.243 (PIA DNS)
   
   docker exec pia-vpn nslookup google.com
   # Should resolve successfully
   ```

4. **Check for WAN Outage:**
   ```bash
   # Container waits for WAN before reconnecting
   docker exec pia-vpn curl -m 5 http://1.1.1.1
   # If this fails, your internet connection is down (not VPN issue)
   ```

### Cannot Access Local Network

**Symptom:** Can't reach local devices (192.168.x.x) while VPN is connected.

**Solutions:**
1. **Add LOCAL_NETWORK:**
   ```yaml
   environment:
     - LOCAL_NETWORK=192.168.1.0/24,172.17.0.0/16
   ```

2. **Allow Specific Ports:**
   ```yaml
   environment:
     - LOCAL_NETWORK=192.168.1.0/24
     - LOCAL_PORTS=8080,9091,32400     # WebUI ports accessible from LAN
   ```

3. **Test Local Access:**
   ```bash
   # From host machine
   curl http://192.168.1.100:8080     # Should reach container
   
   # From container to LAN device
   docker exec pia-vpn curl http://192.168.1.1
   ```

### Health Monitor Keeps Reconnecting

**Symptom:** Container constantly reconnects, logs show "VPN unhealthy, reconnecting".

**Solutions:**
1. **Check Server Latency:**
   ```bash
   # Some servers are overloaded or far away
   docker exec pia-vpn cat /tmp/server_latency
   # If >200ms, try different location
   ```

2. **Increase Failure Threshold:**
   ```yaml
   environment:
     - MAX_FAILURES=5        # Default is 3
     - CHECK_INTERVAL=30     # Default is 15 seconds
   ```

3. **Disable Parallel Checks:**
   ```yaml
   environment:
     - MONITOR_PARALLEL_CHECKS=false
   ```

4. **Check for Firewall Issues:**
   ```bash
   # Test external connectivity manually
   docker exec pia-vpn curl -m 10 https://1.1.1.1
   docker exec pia-vpn ping -c 3 8.8.8.8
   ```

### Slow Speeds / High Latency

**Symptom:** VPN connected but speeds are slower than expected.

**Solutions:**
1. **Choose Closer Server:**
   ```yaml
   environment:
     # List multiple close servers
     - PIA_LOCATION=us_california,us_seattle,us_silicon_valley
   ```

2. **Test Speed:**
   ```bash
   docker exec pia-vpn curl -o /dev/null -s -w "Speed: %{speed_download} bytes/sec\n" \
     http://speedtest.tele2.net/100MB.zip
   ```

3. **Check WireGuard Handshake:**
   ```bash
   docker exec pia-vpn wg show pia latest-handshakes
   # Should be recent (< 2 minutes)
   ```

### Container Crashes / Won't Start

**Symptom:** Container exits immediately or crashes during startup.

**Solutions:**
1. **Check Logs:**
   ```bash
   docker logs pia-vpn
   # Look for error messages
   ```

2. **Verify NET_ADMIN Capability:**
   ```yaml
   services:
     pia-vpn:
       cap_add:
         - NET_ADMIN    # Required!
       cap_drop:
         - all          # Recommended but not required
   ```

3. **Check /dev/net/tun:**
   ```bash
   # On host
   ls -l /dev/net/tun
   # Should exist (created by kernel module)
   
   # Load module if missing
   sudo modprobe tun
   ```

4. **Test Minimal Config:**
   ```bash
   docker run -it --rm --cap-add NET_ADMIN \
     -e PIA_USER=p1234567 \
     -e PIA_PASS=yourpassword \
     -e PIA_LOCATION=us_california \
     -e LOG_LEVEL=debug \
     x0lie/pia-tun:latest
   ```

### Metrics Not Showing

**Symptom:** Prometheus endpoint not accessible or returns empty data.

**Solutions:**
1. **Enable Metrics:**
   ```yaml
   environment:
     - METRICS=true
     - METRICS_PORT=9090
   ports:
     - 9090:9090
   ```

2. **Test Endpoint:**
   ```bash
   curl http://localhost:9090/metrics
   # Should return Prometheus-format metrics
   
   curl "http://localhost:9090/metrics?format=json"
   # Should return JSON stats
   ```

3. **Check Firewall:**
   ```bash
   docker exec pia-vpn netstat -tuln | grep 9090
   # Should show: tcp 0.0.0.0:9090 LISTEN
   ```

---

## Common Commands

```bash
# View VPN status
docker logs pia-vpn

# Check connection health
docker exec pia-vpn curl ifconfig.me

# View current port (if port forwarding enabled)
docker exec pia-vpn cat /etc/wireguard/port

# Test DNS resolution
docker exec pia-vpn nslookup google.com

# Check WireGuard status
docker exec pia-vpn wg show

# View firewall rules
docker exec pia-vpn nft list ruleset

# Restart VPN
docker restart pia-vpn

# View metrics
curl http://localhost:9090/metrics?format=json

# Check server info
docker exec pia-vpn cat /tmp/meta_cn        # Server name
docker exec pia-vpn cat /tmp/server_latency # Ping time

# Force reconnect (trigger health monitor)
docker exec pia-vpn pkill -f monitor

# Interactive shell for debugging
docker exec -it pia-vpn /bin/bash
```

---

## Installation & Updates

### Docker Hub (Recommended)
```bash
# Pull latest version
docker pull x0lie/pia-tun:latest

# Pull specific version
docker pull x0lie/pia-tun:v1.0.0

# Update running container
docker-compose pull
docker-compose up -d
```

### GitHub Container Registry
```bash
docker pull ghcr.io/yourusername/pia-tun:latest
```

### Build from Source
```bash
# Clone repository
git clone https://github.com/yourusername/pia-tun.git
cd pia-tun

# Build image
docker build -t pia-tun:local .

# Run
docker run -d --cap-add NET_ADMIN \
  -e PIA_USER=p1234567 \
  -e PIA_PASS=yourpassword \
  pia-tun:local
```

### Platform Support
- **linux/amd64**: x86_64 (Intel/AMD)
- **linux/arm64**: ARM 64-bit (Raspberry Pi 4, Apple M1/M2)
- **linux/arm/v7**: ARM 32-bit (Raspberry Pi 3)

---

## Advanced Topics

### Custom Firewall Rules
**Use Case:** Need specific firewall rules beyond killswitch.

The container uses nftables/iptables. You can add custom rules:
```bash
# Add rule to allow specific IP
docker exec pia-vpn nft add rule inet killswitch output ip daddr 1.2.3.4 accept

# Add rule before killswitch DROP policy
docker exec pia-vpn iptables -I OUTPUT 1 -d 1.2.3.4 -j ACCEPT
```

**Persistence:** Rules reset on reconnect. For permanent changes, fork and modify `scripts/killswitch.sh`.

### WireGuard Configuration
**View Generated Config:**
```bash
docker exec pia-vpn cat /etc/wireguard/pia.conf
```

**Example Output:**
```ini
[Interface]
PrivateKey = <private-key>
Address = 10.x.x.x/32
DNS = 10.0.0.243
MTU = 1280

[Peer]
PublicKey = <server-public-key>
Endpoint = 123.45.67.89:1337
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25
```

**Custom MTU:**
```yaml
# Not directly configurable, but can modify /etc/wireguard/pia.conf in entrypoint script
# Fork and change scripts/vpn.sh generate_config() function
```

### Health Monitor Tuning
**Parallel vs Serial Checks:**
- **Parallel (default)**: Faster checks, tests ping + HTTP simultaneously
- **Serial**: More reliable on slow networks

```yaml
environment:
  - MONITOR_PARALLEL_CHECKS=false    # Serial mode
  - CHECK_INTERVAL=30                # Check every 30s
  - MAX_FAILURES=5                   # Tolerate 5 failures (150s) before reconnect
```

**WAN Detection:**
Uses bypass routing table to test internet connectivity (NIST time servers) without going through VPN. This distinguishes:
- **VPN failure**: WAN up, VPN down → Reconnect
- **Internet outage**: WAN down, VPN down → Wait for WAN

### Service Dependencies
**Restart containers after VPN reconnects:**
```yaml
environment:
  - RESTART_SERVICES=qbittorrent,sonarr,radarr
```

**How It Works:**
1. VPN reconnects
2. Container runs: `docker restart qbittorrent sonarr radarr`
3. Services restart with new VPN connection

**Requires:** Docker socket mounted (security risk, not recommended for production)
```yaml
volumes:
  - /var/run/docker.sock:/var/run/docker.sock
```

### Port Forwarding Internals
**Signature Lifecycle:**
1. Initial signature request (5-min retry window)
2. Bind port with signature (3-min retry window)
3. Refresh bind every 15 minutes (prevents port death)
4. Refresh signature every 31 days (PIA requirement)

**Failure Escalation:**
- **Bind API error** → Immediate signature refresh
- **Bind network error (3min)** → Signature refresh
- **Signature API error** → Immediate reconnect
- **Signature network error (2min)** → Reconnect

**Port Death Window:** 15min bind + 3min retry + 2min sig retry = 20min max (4min safety margin before 24min PIA timeout)

### Webhook Notifications
**Not yet implemented** (placeholder in code).

Planned feature:
```yaml
environment:
  - WEBHOOK_URL=https://myservice.com/webhook
```

Will POST on port changes:
```json
{
  "port": 12345,
  "ip": "123.45.67.89",
  "timestamp": "2024-12-06T12:34:56Z"
}
```

---

## Development & Contributing

### Building Locally
```bash
git clone https://github.com/yourusername/pia-tun.git
cd pia-tun

# Build image
docker build -t pia-tun:dev .

# Run with debug logging
docker run -it --rm --cap-add NET_ADMIN \
  -e PIA_USER=p1234567 \
  -e PIA_PASS=yourpassword \
  -e LOG_LEVEL=debug \
  pia-tun:dev
```

### Testing
```bash
# Run leak tests
cd testing/leaktest
go run . --duration 60s

# Manual connection test
docker exec pia-vpn /app/scripts/verify_connection.sh
```

### Code Structure
- **`run.sh`**: Main entrypoint, orchestrates lifecycle
- **`scripts/vpn.sh`**: PIA authentication, WireGuard config
- **`scripts/killswitch.sh`**: Firewall management (nftables/iptables)
- **`scripts/verify_connection.sh`**: DNS, IP, IPv6 leak checks
- **`cmd/monitor/`**: Go health monitor (interface, ping, HTTP checks)
- **`cmd/portforward/`**: Go port forwarding service (PIA API client)
- **`cmd/proxy/`**: Go SOCKS5/HTTP proxy server

### Contributing Guidelines
1. Fork repository
2. Create feature branch: `git checkout -b feature/my-feature`
3. Test changes locally
4. Follow existing code style (bash: 4-space indent, Go: gofmt)
5. Add tests if applicable
6. Submit pull request with description

### Reporting Issues
**Include:**
- Container logs: `docker logs pia-vpn`
- Debug logs: `LOG_LEVEL=debug`
- Environment variables (redact passwords!)
- Docker version: `docker version`
- Host OS and kernel: `uname -a`
- Expected vs actual behavior

---

## Frequently Asked Questions (FAQ)

### Q: Does this work with any VPN provider?
**A:** No, this container is specifically designed for **Private Internet Access (PIA)** only. It uses PIA's proprietary authentication API and WireGuard configuration endpoints. For multi-provider support, see [Gluetun](https://github.com/qdm12/gluetun).

### Q: Why PIA-only instead of multi-provider?
**A:** Optimizing for a single provider allows:
- Faster reconnects (PIA-specific retry logic)
- Better port forwarding (PIA's API is unique)
- Smaller image size (~50MB vs 100MB+)
- Simpler codebase (easier to maintain)

### Q: Can I use this on Raspberry Pi?
**A:** Yes! Multi-arch images support:
- Raspberry Pi 4+ (linux/arm64)
- Raspberry Pi 3 (linux/arm/v7)
- Older models may struggle with WireGuard crypto

### Q: Is WireGuard faster than OpenVPN?
**A:** Generally yes:
- **WireGuard**: Modern crypto, kernel-space, ~4000 lines of code
- **OpenVPN**: Older crypto, user-space, ~100k lines of code
- **Real-world**: WireGuard typically 15-30% faster with lower CPU usage

### Q: Does port forwarding cost extra?
**A:** No, PIA includes port forwarding for free (if your server supports it). Not all providers offer this feature.

### Q: How do I know if my torrent client is using the VPN?
**Test Methods:**
1. **Check IP in torrent client**: Should show VPN IP, not your real IP
2. **Use tracker**: Some torrents show your IP in peer list
3. **qBittorrent test**: Settings → Advanced → Network Interface → should be `pia`
4. **IP leak test**: https://ipleak.net (check torrent section)

### Q: Can I run multiple instances?
**A:** Yes, but:
- Each needs unique container name
- Each needs unique port mappings (8080, 1080, etc.)
- Each counts as separate VPN connection (PIA allows 10 simultaneous)

```yaml
services:
  pia-vpn-us:
    image: x0lie/pia-tun:latest
    container_name: pia-vpn-us
    environment:
      - PIA_LOCATION=us_california
    ports:
      - 8080:8080

  pia-vpn-eu:
    image: x0lie/pia-tun:latest
    container_name: pia-vpn-eu
    environment:
      - PIA_LOCATION=uk_london
    ports:
      - 8081:8080
```

### Q: What happens if my internet goes down?
**A:** The health monitor detects WAN outages and waits:
1. Checks bypass routes to NIST time servers
2. If WAN is down, displays "Waiting for WAN..." message
3. Uses exponential backoff (5s → 10s → 20s → 40s → 80s → 160s)
4. When WAN returns, reconnects VPN automatically

### Q: How much bandwidth does this use?
**A:** WireGuard overhead is minimal:
- **Encryption overhead**: ~60 bytes per packet (1-2% for typical usage)
- **Keepalive**: 25-second interval, ~1KB/hour
- **Health checks**: ~100KB/day (15s pings)
- **Total idle overhead**: ~1MB/day

### Q: Can I use this in production?
**A:** Yes, but consider:
- **Pros**: Stable, automatic recovery, metrics for monitoring
- **Cons**: Single point of failure, VPN provider dependency
- **Recommendation**: Use in homelab, test thoroughly before production

### Q: Is this legal?
**A:** Using a VPN is legal in most countries. However:
- **Check local laws**: Some countries restrict/ban VPNs
- **Terms of Service**: Some services prohibit VPN usage
- **Torrenting**: Legal for legal content, illegal for piracy
- **Responsibility**: You are responsible for how you use this tool

---

## Changelog & Roadmap

### Recent Changes
See [GitHub Releases](https://github.com/yourusername/pia-tun/releases) for full changelog.

### Roadmap (Future Features)
- [ ] IPv6 support (when PIA fully supports WireGuard IPv6)
- [ ] Webhook notifications for port changes
- [ ] Custom DNS over HTTPS/TLS
- [ ] Split tunneling for specific apps
- [ ] Web UI for management
- [ ] Multi-hop VPN routing
- [ ] Killswitch customization via config file

---

## License & Support

### License
This project is licensed under the **MIT License** - see [LICENSE](LICENSE) file for details.

### Support Channels
- **GitHub Issues**: Bug reports and feature requests
- **GitHub Discussions**: Questions, ideas, community support
- **Discord**: [Join our server](#) for real-time chat (if applicable)
- **Documentation**: [Wiki](https://github.com/yourusername/pia-tun/wiki) for guides

### Related Projects
- **Gluetun**: Multi-provider VPN container (supports 30+ providers)
- **qBittorrent**: Torrent client with WebUI
- **Transmission**: Lightweight torrent client
- **Prowlarr/Sonarr/Radarr**: Media automation (*arr stack)

### Disclaimer
This project is **not affiliated with Private Internet Access** or Kape Technologies. PIA and Private Internet Access are trademarks of their respective owners. This is an independent open-source project.

---

## Credits & Acknowledgments
- **Private Internet Access**: For excellent VPN service and WireGuard support
- **WireGuard**: For modern, fast, secure VPN protocol
- **Alpine Linux**: For minimal, secure base image
- **Community Contributors**: Thank you to everyone who reports issues and submits PRs!

---

**⭐ If you find this project useful, please consider starring it on GitHub!**

---

## SEO Keywords Summary

**Primary Keywords (High Priority):**
- PIA Docker container
- Private Internet Access Docker
- WireGuard Docker VPN
- qBittorrent VPN Docker
- Docker VPN port forwarding
- PIA WireGuard container
- homelab VPN Docker

**Secondary Keywords:**
- Docker torrent VPN
- PIA killswitch Docker
- SOCKS5 proxy container
- Prometheus VPN metrics
- Docker Compose VPN
- Kubernetes VPN container
- pia-tun Docker

**Long-Tail Keywords:**
- How to setup PIA in Docker
- qBittorrent Docker Compose with VPN
- WireGuard Docker container tutorial
- PIA port forwarding automation
- Docker VPN with local network access
- Homelab torrent VPN setup

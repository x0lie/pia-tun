# PIA WireGuard Container - Project Structure

## 📁 Directory Layout

```
pia-wireguard-docker/
├── cmd/                          # Go applications
│   ├── monitor/                  # Health monitoring daemon
│   │   └── main.go              # VPN health checks, metrics server
│   └── proxy/                    # SOCKS5/HTTP proxy server
│       └── main.go              # Proxy with auth support
│
├── scripts/                      # Shell scripts
│   ├── ui.sh                    # UI utilities, colors, output
│   ├── vpn.sh                   # VPN setup (auth, config, interface)
│   ├── killswitch.sh            # Firewall rules (nftables/iptables)
│   ├── verify_connection.sh     # Connection verification
│   ├── proxy_go.sh              # Proxy management
│   ├── healthcheck.sh           # Docker healthcheck
│   ├── port_forwarding.sh       # Port forwarding main logic
│   ├── port_monitor.sh          # Port change monitoring daemon
│   └── port_api_updater.sh      # Torrent client API updates
│
├── run.sh                        # Main entrypoint
├── Dockerfile                    # Container build config
└── go.mod                        # Go dependencies
```

---

## 🔧 Core Components

### Main Entry Point

#### `run.sh`
**Purpose**: Container entrypoint and lifecycle management

**Key Functions**:
- `initial_connect()` - First VPN connection
- `perform_reconnection()` - Handle VPN reconnects
- `main_loop()` - Keep container running, handle reconnect requests
- `cleanup()` - Graceful shutdown

**Workflow**:
```
Start → initial_connect → main_loop → [wait for reconnect signal] → perform_reconnection → main_loop
```

---

## 🌐 VPN Management

### `scripts/vpn.sh`
**Purpose**: Complete VPN lifecycle (authentication → server selection → configuration → interface)

**Sections**:
1. **Authentication & Server Selection**
   - `authenticate()` - PIA token generation
   - `find_server()` - Best server selection (latency-based)
   - `generate_config()` - WireGuard config creation

2. **WireGuard Interface Management**
   - `parse_wg_config()` - Parse WireGuard config file
   - `setup_dns()` - Configure DNS servers
   - `bring_up_wireguard()` - Create and configure interface
   - `teardown_wireguard()` - Clean shutdown

3. **Main Setup**
   - `setup_vpn()` - Orchestrates full VPN setup

**Called By**: `run.sh` (initial_connect, perform_reconnection)

---

### `scripts/killswitch.sh`
**Purpose**: Network firewall management to prevent leaks

**Features**:
- Auto-detects nftables (modern, fast) or iptables (fallback)
- Pre-tunnel rules (allow only DNS/HTTPS/auth before VPN up)
- Post-tunnel rules (force all traffic through VPN)
- Local network exceptions
- IPv6 leak prevention

**Key Functions**:
- `setup_pre_tunnel_killswitch()` - Initial restrictive rules
- `finalize_killswitch()` - Lock down to VPN-only traffic
- `cleanup_killswitch()` - Remove all rules

**Called By**: `run.sh` (setup, teardown)

---

### `scripts/verify_connection.sh`
**Purpose**: Verify VPN is working correctly

**Checks**:
1. External IP retrieval
2. IP change verification (vs pre-VPN IP)
3. DNS validation (no leaks)
4. IPv6 leak detection

**Key Functions**:
- `capture_real_ip()` - Save IP before VPN
- `validate_dns()` - Combined DNS leak/validation check
- `verify_connection()` - Run all checks, report status

**Called By**: `run.sh` (after VPN connection)

---

## 🔌 Proxy Management

### `scripts/proxy_go.sh`
**Purpose**: Manage SOCKS5/HTTP proxy servers

**Features**:
- Single Go binary handles both protocols
- Optional authentication (no setuid required)
- Automatic restart on VPN reconnection

**Key Functions**:
- `start_proxies()` - Start proxy daemon
- `stop_proxies()` - Clean shutdown
- `restart_proxies()` - Restart after reconnect
- `is_proxy_running()` - Status check

**Called By**: `run.sh` (if PROXY_ENABLED=true)

---

## 📡 Port Forwarding

### `scripts/port_forwarding.sh`
**Purpose**: Manage PIA port forwarding (signature acquisition, binding, keep-alive)

**Workflow**:
```
Get signature → Parse port → Bind port → Write to file → Update API → Keep-alive loop
                                                                    ↓
                                            [Every 10min: Bind refresh]
                                            [Every 7 days: New signature]
```

**Key Functions**:
- `get_signature()` - Request port from PIA
- `bind_port()` - Bind port keep-alive
- `parse_pf_response()` - Extract port, expiry
- Main loop - Dual refresh (bind + signature)

**Called By**: `run.sh` (as background daemon)

---

### `scripts/port_monitor.sh`
**Purpose**: Watch port file, update torrent clients when port changes

**Workflow**:
```
Watch /etc/wireguard/port → Detect change → Update API → Retry if failed
```

**Key Functions**:
- `monitor_port_changes()` - Main watch loop
- Uses `port_api_updater.sh` functions

**Called By**: `run.sh` (if PORT_API_ENABLED=true)

---

### `scripts/port_api_updater.sh`
**Purpose**: Update torrent client APIs with forwarded port

**Supported Clients**:
- qBittorrent (WebUI API)
- Transmission (RPC)
- Deluge (JSON-RPC)
- rTorrent (XML-RPC)

**Key Function**:
- `update_port_api()` - Router to client-specific updater
- `update_qbittorrent()`, `update_transmission()`, etc.

**Used By**: `port_forwarding.sh`, `port_monitor.sh`

---

## 🩺 Health Monitoring

### `cmd/monitor/main.go`
**Purpose**: Continuous VPN health monitoring

**Features**:
- Interface checks
- Connectivity tests (ping, HTTP)
- Parallel or serial checks (configurable)
- Transfer byte monitoring (detect stale connections)
- Metrics HTTP endpoint (Prometheus-compatible)

**Endpoints** (if METRICS=true):
- `/metrics` - Prometheus format or JSON
- `/health` - Healthcheck endpoint

**Triggers**: Reconnection via `/tmp/vpn_reconnect_requested`

---

### `scripts/healthcheck.sh`
**Purpose**: Docker HEALTHCHECK script

**Strategy**:
1. Try metrics endpoint (if enabled) - most reliable
2. Fallback to basic checks (interface + ping)

**Return Codes**:
- 0 = Healthy
- 1 = Unhealthy

---

## 🎨 Utilities

### `scripts/ui.sh`
**Purpose**: Consistent terminal output and formatting

**Provides**:
- Color codes (red, green, blue, yellow, cyan)
- Status indicators (`show_step`, `show_success`, `show_warning`, `show_error`)
- Status boxes (VPN Connected, Reconnecting)
- Helper functions (`trim`, `get_external_ip`, `restart_services`)

**Used By**: All shell scripts

---

## 🔄 Process Relationships

```
┌─────────────────────────────────────────────────────────────┐
│                          run.sh                             │
│                    (Main Process - PID 1)                   │
└─────────────────────────────────────────────────────────────┘
         │
         ├──→ vpn.sh (setup)
         │    └──→ killswitch.sh (firewall rules)
         │
         ├──→ monitor (Go binary - daemon)
         │    └──→ Checks health every CHECK_INTERVAL
         │
         ├──→ proxy (Go binary - daemon, optional)
         │    └──→ SOCKS5 + HTTP proxy servers
         │
         ├──→ port_forwarding.sh (daemon, optional)
         │    └──→ Keep-alive loop every 10 minutes
         │
         └──→ port_monitor.sh (daemon, optional)
              └──→ Watch port file every 30 seconds
```

---

## 📊 Data Flow

### VPN Connection Flow
```
PIA_USER/PASS → authenticate() → token
                                   ↓
PIA_LOCATION → find_server() → best server IP/CN
                                   ↓
token + server → generate_config() → /etc/wireguard/pia.conf
                                   ↓
pia.conf → bring_up_wireguard() → pia interface UP
                                   ↓
                           finalize_killswitch() → traffic locked to VPN
```

### Port Forwarding Flow
```
token → getSignature → payload + signature + port
           ↓
     bindPort (keep-alive)
           ↓
     /etc/wireguard/port ───→ port_monitor.sh → Torrent Client API
                          └──→ Applications can read directly
```

### Reconnection Flow
```
monitor detects failure → /tmp/vpn_reconnect_requested created
                                   ↓
run.sh detects file → perform_reconnection()
                          ↓
        teardown → setup → restart services → restart daemons
```

---

## 🔐 Security Model

### Network Isolation
1. **Pre-tunnel**: Only DNS (53), HTTPS (443), auth (1337) allowed
2. **Post-tunnel**: Only VPN interface + local network (if configured)
3. **IPv6**: Completely blocked or routed through VPN

### Privilege Requirements
- **NET_ADMIN**: Required for interface/routing management
- **NET_RAW**: Required for ICMP (ping) in health checks
- **No setuid/setgid**: Proxy works without elevated privileges

### Firewall Priority
```
Priority 100: Local network exceptions (if LOCAL_NETWORK set)
Priority 200: VPN routing rules
Priority 300: Suppress default routes
```

---

## 📝 Configuration Files

### Generated at Runtime
- `/etc/wireguard/pia.conf` - WireGuard configuration
- `/etc/wireguard/port` - Current forwarded port
- `/tmp/pia_token` - PIA authentication token
- `/tmp/server_endpoint` - Selected server IP
- `/tmp/meta_cn` - Server CN (common name)
- `/tmp/client_ip` - VPN tunnel IP
- `/tmp/pf_gateway` - Port forwarding gateway
- `/tmp/real_ip` - Pre-VPN IP address
- `/tmp/vpn_reconnect_requested` - Reconnect trigger

### State Files
- `/tmp/reconnecting` - Reconnection in progress
- `/tmp/port_forwarding_complete` - PF initialization done
- `/tmp/proxy.pid` - Proxy process ID
- `/tmp/proxy.log` - Proxy logs

---

## 🎯 Design Principles

### 1. Fail-Safe Defaults
- VPN must be up before allowing internet traffic
- Killswitch active at all times
- IPv6 disabled by default

### 2. Modularity
- Each script has a single, clear purpose
- Functions are reusable
- Easy to test components individually

### 3. Observability
- Consistent logging format
- Color-coded output
- Optional metrics endpoint
- Docker healthcheck

### 4. Resilience
- Automatic reconnection on failure
- Retry logic for transient errors
- Graceful degradation (e.g., port forwarding optional)

### 5. Performance
- Early exit optimization
- Parallel checks when beneficial
- Efficient firewall rules (nftables when available)

---

## 🚀 Adding New Features

### To add a new script:
1. Place in `scripts/`
2. Add header comment explaining purpose
3. Source from `run.sh` if needed
4. Use `ui.sh` for consistent output
5. Add `set -euo pipefail` for error handling
6. Document here in this file

### To add environment variables:
1. Add to Dockerfile with default
2. Handle in relevant script
3. Export in `run.sh` if needed by child processes
4. Document in Dockerfile comments

### To modify VPN flow:
1. Edit `scripts/vpn.sh` (all VPN logic in one place)
2. Test with various PIA_LOCATION settings
3. Verify killswitch remains active

---

## 🧪 Testing

### Manual Testing
```bash
# Full container test
docker-compose up

# Individual script test
docker exec <container> /app/scripts/verify_connection.sh

# Check firewall rules
docker exec <container> nft list ruleset
# or
docker exec <container> iptables -L -n -v
```

### Key Test Scenarios
1. Initial connection
2. Reconnection after failure
3. Port forwarding (if enabled)
4. Proxy functionality (if enabled)
5. Service restart (if configured)
6. IPv6 leak prevention
7. DNS leak prevention

---

## 📚 Further Reading

- WireGuard: https://www.wireguard.com/
- PIA API: https://github.com/pia-foss/manual-connections
- nftables: https://wiki.nftables.org/
- Docker healthcheck: https://docs.docker.com/engine/reference/builder/#healthcheck

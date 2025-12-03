# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**IMPORTANT**: These instructions override default behavior and must be followed exactly.

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
   - `port_monitor.sh`: Monitors forwarded port and when necessary calls port_api_updater.sh
   - `port_api_updater.sh`: Updates the port of dependent via API
   - `proxy_go.sh`: Manages go proxy binary process
   - `verify_connection.sh`: Verifies the state of the VPN after connecting
   - `ui.sh`: Logging and UI utilities

2. **Go Applications** (`cmd/`): Performance-critical monitoring and proxy
   - `cmd/monitor/`: VPN health monitor with metrics collection
   - `cmd/proxy/`: SOCKS5/HTTP proxy implementation
   - `cmd/portfoward/`: PF signature acquisition and binding

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
curl http://localhost:9090/metrics?format=json

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
- `LOCAL_PORTS`: Ports accessible from LAN (e.g., `8080,9091` for qBittorrent, Transmission)
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

## Working with this Codebase

### Codebase Navigation
- **ALWAYS** consult `PROJECT_STRUCTURE.md` first to understand where functionality lives
- Use the structure document as your map - it contains high-level explanations of every file
- When referencing code in explanations, use the format `file_path:line_number` (e.g., `scripts/killswitch.sh:120`)

### Automatic Documentation Updates
**CRITICAL**: When you modify code that changes how a component works, you MUST update the relevant sections in `PROJECT_STRUCTURE.md`:

1. **After modifying a file**: Check if the changes affect the high-level description in PROJECT_STRUCTURE.md
2. **Significant changes**: Update the corresponding "What it Does" section to reflect new behavior
3. **New functions/features**: Add descriptions if they represent new high-level operations
4. **Removed features**: Delete outdated information from the structure document
5. **Changed workflows**: Update flow descriptions (e.g., reconnection process, port forwarding)

**Examples of when to update PROJECT_STRUCTURE.md:**
- Adding a new health check type → Update `cmd/monitor/health.go` section
- Changing killswitch architecture → Update `scripts/killswitch.sh` section
- Adding new API client support → Update `scripts/port_api_updater.sh` section
- Modifying reconnection flow → Update `run.sh` section

**When NOT to update:**
- Bug fixes that don't change behavior
- Performance optimizations that maintain the same logic
- Code refactoring without functional changes
- Adding debug logging

### Code Reference Format
When discussing code with the user:
- Reference specific locations: `scripts/vpn.sh:245` (server selection logic)
- Link multiple related locations when explaining flows
- Use this format so the user can easily navigate to the code

## Code Conventions

### Shell Scripts
- All scripts use `bash` with `set -e` (exit on error)
- Debug logging via `show_debug` (respects `LOG_LEVEL`)
- UI functions in `ui.sh`: `show_step`, `show_success`, `show_error`, `show_warning`
- Firewall functions prefixed with `nft_` (nftables) or `ipt_` (iptables)
- Temporary files in `/tmp/`: `pia_token`, `client_ip`, `meta_cn`, `pf_gateway`, etc.
- Always use surgical exemptions for firewall holes (never persistent rules during setup)
- Clean up temporary exemptions immediately after use

### Go Code
- Standard Go project layout: `cmd/` for binaries
- No external dependencies (stdlib only) - NEVER add external packages
- Optimized builds: `-ldflags="-w -s"` (strip debug info)
- Configuration via environment variables
- Logging to stderr with timestamps
- Context-aware operations for graceful shutdown
- Thread-safe operations with mutexes where needed

### Dockerfile
- Multi-stage build: Go builder → Alpine runtime
- Minimal dependencies (bash, curl, jq, wireguard-tools, nftables, iptables)
- Executables marked in builder stage (prevents duplication)
- Requires `NET_ADMIN` capability, all others dropped
- Keep image size minimal - verify size after changes

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
- `/tmp/vpn_reconnect_pipe`: Named pipe for event-driven reconnection signaling (run.sh blocks on read)
- `/tmp/reconnecting`: Active reconnection in progress (skip health checks)
- `/tmp/pf_signature_failed`: Port forwarding signature failure → monitor triggers reconnect
- `/tmp/port_forwarding_complete`: Port forwarding setup finished
- `/tmp/port_change_pipe`: Named pipe for port monitor notifications (port_monitor.sh blocks on read)

## Architecture Principles (DO NOT VIOLATE)

### Security-First Design
1. **Killswitch is always active**: Never have windows where traffic can leak
2. **Critical order**: Always remove VPN from killswitch BEFORE tearing down interface
3. **Surgical exemptions only**: Temporary firewall holes must be removed immediately after use
4. **No persistent API exemptions**: All PIA API access uses temporary exemptions
5. **Zero external dependencies in Go**: Keeps attack surface minimal and binaries small

### Event-Driven Architecture
1. **Named pipes for signaling**: Use blocking reads on pipes (not polling with flags)
   - `/tmp/vpn_reconnect_pipe`: Monitor signals run.sh
   - `/tmp/port_change_pipe`: Portforward signals port_monitor.sh
2. **No busy-waiting**: All loops should block on events or use appropriate intervals
3. **Context-aware shutdown**: Go programs must handle SIGTERM gracefully

### Zero-Leak Reconnection
1. **Baseline killswitch stays active**: During reconnection, only VPN rules change
2. **WAN checks via bypass routes**: No firewall manipulation for health checks
3. **Surgical exemptions for setup**: Temporary holes for API access only during setup phase

### Performance Optimization
1. **Parallel health checks**: When enabled, run connectivity tests concurrently
2. **nftables preferred**: Use O(1) set lookups when available (fallback to iptables)
3. **Minimal allocations in hot paths**: Health check loop runs every 15s
4. **Context cancellation**: All retries must respect context for fast shutdown

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

## Common Workflows

### When Making Changes

**Before suggesting changes:**
1. Read the relevant section in `PROJECT_STRUCTURE.md` to understand current behavior
2. Check if the change violates any Architecture Principles
3. Consider security implications (especially for killswitch/VPN code)
4. Think about edge cases (reconnection, shutdown, failures)

**After making changes:**
1. Update `PROJECT_STRUCTURE.md` if behavior changed (see "Automatic Documentation Updates")
2. Suggest appropriate testing commands from "Testing Recommendations"
3. If modifying Go code, remind user to rebuild the Docker image
4. If changing environment variables, note what needs updating in docker-compose.yml

**When adding features:**
1. Follow existing patterns (check similar code first)
2. Add environment variable configuration if user-facing
3. Add debug logging for troubleshooting
4. Consider adding metrics if it's monitorable state
5. Update CLAUDE.md environment variables section if needed
6. Add entry to PROJECT_STRUCTURE.md for new files

**When fixing bugs:**
1. Identify root cause before suggesting fix
2. Check if bug exists in multiple places (nftables vs iptables paths)
3. Verify fix doesn't break reconnection or shutdown
4. Add debug logging to help catch similar issues
5. Update PROJECT_STRUCTURE.md only if the bug fix changes documented behavior

### Security-Critical Code Changes

When modifying these files, be extra cautious:
- `scripts/killswitch.sh`: Any change could cause IP leaks
- `scripts/vpn.sh` (teardown_wireguard): Must remove VPN from killswitch first
- `run.sh` (perform_reconnection): Critical ordering requirements
- `cmd/monitor/health.go` (waitForWAN): Blocks reconnection, must not fail

Always suggest testing for leaks after modifying security-critical code.

### Suggesting Commits

When user asks to commit changes:
1. **Never** proactively suggest commits - wait for user to request
2. Review `git status` and `git diff` before crafting commit message
3. Commit messages should be concise and describe "why" not "what"
4. Include co-author line as specified in Bash tool instructions
5. Suggest relevant testing commands before committing
6. Remind about rebuilding Docker image if Go code or Dockerfile changed

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

---

## About This File (CLAUDE.md)

### What CLAUDE.md Does
This file provides persistent context and instructions to Claude Code every time it's launched in this repository. It:
- Shapes how Claude approaches problems in this codebase
- Enforces project-specific conventions and patterns
- Provides architectural context to inform better decisions
- Automates workflows like documentation updates
- Serves as a "memory" that persists across sessions

### Current Capabilities
✅ **Automatic documentation updates** - Claude will update PROJECT_STRUCTURE.md when code behavior changes
✅ **Code reference format** - Claude uses `file:line` format for easy navigation
✅ **Architecture enforcement** - Key principles Claude must not violate
✅ **Security awareness** - Extra caution on killswitch and VPN code
✅ **Testing guidance** - Claude suggests appropriate tests after changes
✅ **Workflow automation** - Standard processes for adding features, fixing bugs, etc.

### Other Possibilities

**Could be added to CLAUDE.md:**
- **Custom slash commands** - Define shortcuts like `/test-killswitch` or `/check-leaks` in `.claude/commands/`
- **Pre-commit checks** - Instructions to run specific tests before suggesting commits
- **Performance benchmarks** - Commands to verify changes don't regress performance
- **Deployment workflows** - Steps for building multi-arch images, pushing to registry
- **Troubleshooting guides** - Common issues and how to diagnose them
- **Code review checklists** - Specific things to verify for different types of changes
- **Environment-specific configs** - Different behaviors for dev vs production

**What CLAUDE.md cannot do:**
- ❌ Execute code automatically (you must approve all tool use)
- ❌ Self-update safely (risk of losing important instructions through drift)
- ❌ Access external resources not already available to Claude Code
- ❌ Remember state across different repository/directory contexts

**Recommendations:**
- Keep CLAUDE.md focused on high-level guidance and workflows
- Use PROJECT_STRUCTURE.md for detailed component documentation
- Create custom slash commands for frequently-used test sequences
- Add specific architectural decisions as you make them
- Document "gotchas" and edge cases as you discover them

This file is version-controlled, so changes are tracked and can be reviewed/reverted like any code.

# Project Structure

This document provides a comprehensive map of the pia-tun codebase, organized by file with high-level descriptions of each component's purpose and operation.

---

## run.sh

**Purpose:** Main entrypoint and orchestrator for the VPN container lifecycle. Coordinates initial connection, manages the health monitoring loop, and handles VPN reconnections.

### What it Does

**Initialization:**
- Sources all required script libraries (ui.sh, killswitch.sh, vpn.sh, verify_connection.sh, proxy_go.sh)
- Sets up environment variable defaults and exports them for child processes
- Auto-enables PORT_API_ENABLED and PORT_FORWARDING when PORT_API_TYPE is set
- Configures trap handlers for graceful shutdown (SIGTERM, SIGINT, SIGQUIT)

**Initial Connection Flow (`initial_connect()`):**
1. Displays banner (unless reconnecting)
2. Captures real IP address for leak detection (first run only)
3. Clears `/etc/resolv.conf` to prevent DNS leaks
4. Sets up baseline killswitch (blocks all non-VPN traffic)
5. Calls `vpn.sh` to authenticate with PIA and configure WireGuard
6. Brings up the WireGuard interface (`bring_up_wireguard`)
7. Adds VPN to killswitch allowlist (enables routing through tunnel)
8. Verifies connection (DNS, IPv6, external IP checks)

**Main Loop (`main_loop()`):**
1. Starts SOCKS5/HTTP proxies if enabled
2. Starts port forwarding service if enabled (waits up to 30s for completion)
3. Launches health monitor (`/usr/local/bin/monitor`) in background
4. Starts port monitor if both port forwarding and API updates are enabled
5. Restarts dependent services if configured
6. Creates named pipe (`/tmp/vpn_reconnect_pipe`) for monitor communication
7. Blocks on pipe waiting for reconnection requests from health monitor
8. Triggers `perform_reconnection()` when reconnection is needed

**Reconnection Flow (`perform_reconnection()`):**
1. Sets `/tmp/reconnecting` flag to pause health checks
2. Stops port forwarding and proxy processes
3. Tears down WireGuard interface (killswitch remains active)
4. Calls `initial_connect "true"` to establish new connection
5. Restarts proxies and port forwarding services
6. Waits for port forwarding completion
7. Restarts port monitor for API updates
8. Restarts dependent services if configured
9. Removes `/tmp/reconnecting` flag

**Cleanup (`cleanup()`):**
- Stops all proxy processes
- Kills monitor and port forwarding processes
- Tears down WireGuard tunnel
- Removes killswitch rules to prevent network namespace pollution (critical for Kubernetes)

---

## scripts/vpn.sh

**Purpose:** Handles PIA authentication, server selection, and WireGuard configuration generation.

### What it Does

**DNS Resolution:**
- Resolves hostnames using Cloudflare DNS over HTTPS (1.0.0.1, 1.1.1.1)
- Uses surgical firewall exemptions for DNS queries
- Falls back between multiple DNS services for reliability

**Authentication (`authenticate()`):**
- Checks for credentials in Docker secrets (`/run/secrets/pia_user`, `/run/secrets/pia_pass`) or environment variables
- Resolves PIA auth server IP address
- Sends authentication request to `https://www.privateinternetaccess.com/gtoken/generateToken`
- Adds temporary firewall exemption for auth server
- Validates response and extracts authentication token
- Saves token to `/tmp/pia_token`

**Server Selection (`find_server()`):**
- Fetches server list from `serverlist.piaservers.net/vpninfo/servers/v6`
- Supports comma-separated location list (e.g., `ca_ontario,ca_toronto`)
- Filters servers by port forwarding support if enabled
- Tests latency to all candidate servers (with surgical exemptions)
- Selects server with lowest latency
- Saves server metadata to temp files (`/tmp/server_endpoint`, `/tmp/meta_cn`, etc.)

**WireGuard Configuration (`generate_config()`):**
- Generates WireGuard keypair (private/public)
- Registers public key with PIA server via `addKey` endpoint (port 1337)
- Receives server public key, peer IP, and port forwarding gateway
- Writes WireGuard config to `/etc/wireguard/pia.conf`
- Configures DNS (PIA DNS or custom), MTU, and allowed IPs

**Interface Management:**
- `bring_up_wireguard()`: Creates WireGuard interface, configures IP/MTU/peer, sets fwmark (51820), adds routing rules
- `teardown_wireguard()`: Removes VPN from killswitch first (critical order), tears down interface, cleans up routing rules
- `setup_dns()`: Updates `/etc/resolv.conf` with configured DNS servers
- `cleanup_local_exceptions()`: Removes custom routing rules (priority 100, 200, 300)

**Local Network Routing:**
- Adds exceptions for RFC1918 ranges if `LOCAL_NETWORK=all`
- Adds specific CIDR exceptions if configured
- Routing rules ensure local traffic bypasses VPN (evaluated before VPN routing)

---

## scripts/killswitch.sh

**Purpose:** Manages firewall rules (nftables/iptables) to prevent IP leaks, with surgical exemptions for PIA API access and bypass routing for WAN health checks.

### What it Does

**Backend Detection:**
- Auto-detects nftables (modern, O(1) lookups) or falls back to iptables (legacy)
- Sets `USE_NFTABLES=true/false` flag for all subsequent operations
- Uses nftables when kernel support is available

**Bypass Routing Table (setup_bypass_routes()):**
- Creates routing table 100 for WAN health checks
- Routes NIST time servers (129.6.15.28/29, 132.163.96.1, etc.) via eth0
- Allows health monitor to distinguish internet outages from VPN failures
- No firewall manipulation needed - uses routing policy

**Baseline Killswitch (setup_baseline_killswitch()):**
- Cleans up any orphaned rules from previous runs (critical for Kubernetes)
- Sets up bypass routes first
- Creates firewall chains with DROP policy (blocks everything by default)
- Allows: loopback, bypass routes, established connections, local networks
- IPv6: Completely blocked if `DISABLE_IPV6=true`, otherwise routed through VPN only
- Creates `/tmp/killswitch_up` flag after verification passes

**VPN Interface Management:**
- `add_vpn_to_killswitch()`: Inserts rules to allow traffic through pia interface and fwmark 51820
- `remove_vpn_from_killswitch()`: Removes VPN rules (called before teardown to prevent leaks)
- `verify_vpn_in_killswitch()`: Verifies VPN rules are present

**Temporary Exemptions (Surgical Holes):**
- `add_temporary_exemption()`: Creates firewall rule tagged with comment (e.g., "temp_dns_resolve")
- `remove_temporary_exemption()`: Deletes rule by comment tag
- Used during VPN setup for: DNS resolution, authentication, server list fetch, latency tests
- Exemptions are removed immediately after use

**Port Forwarding:**
- `add_forwarded_port_to_input()`: Allows incoming traffic on forwarded port (TCP + UDP)
- `remove_forwarded_port_from_input()`: Removes port forwarding rules
- Adds rules to INPUT chain for VPN interface only

**Local Network Access:**
- Populates nftables sets or iptables rules for configured local networks
- Allows access to proxy/metrics ports from LAN (`LOCAL_PORTS`)
- Supports both TCP and UDP for services like DNS, Plex, HTTP/3

**Implementation Variants:**
- nftables: Uses sets for O(1) lookups, handle-based rule management, optimal rule ordering
- iptables: Chain-based rules, comment-based tracking, manual rule insertion/deletion

**Verification and Stats:**
- `verify_baseline_killswitch()`: Checks that DROP policy is active and bypass routes exist
- `show_ruleset_stats()`: Displays firewall statistics (rule count, sets, backend type)

---

## scripts/verify_connection.sh

**Purpose:** Verifies VPN connection integrity after establishment by checking DNS resolution, IPv6 blocking, and external IP address.

### What it Does

**Real IP Capture (`capture_real_ip()`):**
- Fetches pre-VPN IP from api.ipify.org (5-second timeout)
- Saves to `/tmp/real_ip` for later comparison
- Used to verify IP actually changed after VPN connection

**DNS Validation (`validate_dns()`):**
- Checks each nameserver in `/etc/resolv.conf`
- Detects PIA DNS servers (10.0.0.243/242, 209.222.18.222/218)
- Allows private/local DNS (10.x, 172.16.x, 192.168.x, 127.x)
- Warns on unexpected public DNS (potential leak)
- Returns: 0 (PIA DNS working), 1 (DNS not verified), 2 (DNS leak detected)

**IPv6 Leak Check (`check_ipv6_leak()`):**
- Early exit if IPv6 is allowed (`DISABLE_IPV6=false`)
- Attempts IPv6 connection to api6.ipify.org via pia interface
- Returns error if IPv6 responds (leak detected)
- Used to ensure IPv6 is fully blocked when disabled

**Connection Verification (`verify_connection()`):**
1. Fetches external IP through VPN (timeout causes early exit with warning)
2. Compares VPN IP to real IP (if available)
3. Validates DNS configuration
4. Checks for IPv6 leaks
5. Scores checks (4 total) and displays results
6. Returns success if ≥50% of checks passed

**Display Logic:**
- Perfect score (4/4): Green success message
- Nearly perfect (3/4): Green success message
- Partial (2/4): Yellow warning (VPN active but some checks failed)
- Failure (0-1/4): Red error message

---

## scripts/proxy_go.sh

**Purpose:** Manages the lifecycle of the Go-based SOCKS5/HTTP proxy server.

### What it Does

**Configuration:**
- Loads credentials from Docker secrets (`/run/secrets/proxy_user`, `/run/secrets/proxy_pass`) or environment variables
- Configures SOCKS5 port (default: 1080) and HTTP proxy port (default: 8888)
- Exports configuration for Go proxy binary

**Start Proxies (`start_proxies()`):**
- Cleans up any existing proxy processes
- Launches `/usr/local/bin/proxy` in background
- Captures PID to `/tmp/proxy.pid`
- Verifies process is running after 3-second delay
- Displays connection URLs (with masked password if authenticated)
- Shows debug output if startup fails

**Stop Proxies (`stop_proxies()`, `stop_proxies_silent()`):**
- Kills process by PID from `/tmp/proxy.pid`
- Falls back to `pkill -f proxy` if PID file missing
- Silent version suppresses output (used internally)

**Status Checks:**
- `is_proxy_running()`: Checks if PID from file is alive
- `get_proxy_status()`: Returns "running" or "stopped"

**CLI Interface:**
- Supports `start`, `stop`, `restart`, `status` commands
- Returns appropriate exit codes for status checks

---

## scripts/port_monitor.sh

**Purpose:** Monitors the forwarded port file for changes and triggers API updates to dependent services (qBittorrent, Transmission, etc.).

### What it Does

**Initialization:**
- Sources ui.sh, killswitch.sh, and port_api_updater.sh
- Configures port file path (default: `/etc/wireguard/port`)
- Sets retry interval (60 seconds for failed updates)
- Creates named pipe (`/tmp/port_change_pipe`) for event-driven notifications

**Initial Port Handling:**
- On startup, immediately checks if port file exists
- If port is already present, performs initial API update
- Adds port to firewall INPUT rules
- Initializes state tracking (last port, last update success)

**Main Monitoring Loop (`monitor_port_changes()`):**
- Blocks on named pipe waiting for port change notifications (with 60s timeout)
- On notification: reads new port from pipe
- On timeout: re-reads port file (handles missed notifications)
- Determines if update is needed: port changed OR previous update failed
- Updates firewall rules if port changed
- Calls `update_port_api()` to update torrent client

**State Management:**
- Tracks `LAST_PORT` and `LAST_UPDATE_SUCCESS`
- Retries failed updates every 60 seconds until successful
- Only logs "not reachable" message on first failure (reduces spam)
- Displays success message when retry succeeds

**Event-Driven Architecture:**
- Port forwarding service writes to named pipe when port changes
- Monitor blocks on pipe (efficient, instant notification)
- Timeout serves dual purpose: catch missed notifications + retry failed updates

---

## scripts/port_api_updater.sh

**Purpose:** Updates the listening port on torrent clients and other dependent services via their REST APIs.

### What it Does

**Supported Clients:**
1. **qBittorrent** (`update_qbittorrent()`): Login with cookie, set preferences via `/api/v2/app/setPreferences`, verify
2. **Transmission** (`update_transmission()`): Get session ID, set peer-port via JSON-RPC
3. **Deluge** (`update_deluge()`): Login, connect to daemon, set listen_ports via JSON-RPC
4. **rTorrent** (`update_rtorrent()`): Set port range via XML-RPC (`network.port_range.set`)
5. **Custom** (`PORT_API_CMD`): Execute custom command with `{PORT}` placeholder

**Retry Logic:**
- Each client function performs a single attempt
- `update_port_api()`: Retries 3 times with 2-second delays
- Short retries handle transient network failures
- Returns 0 on success, 1 on failure (caller handles long-term retry)

**Configuration:**
- `PORT_API_TYPE`: Client type (qbittorrent, transmission, deluge, rtorrent, custom)
- `PORT_API_URL`: API endpoint URL
- `PORT_API_USER`, `PORT_API_PASS`: Authentication credentials
- Curl timeout: 5s connect, 10s total

**Error Handling:**
- Validates PORT_API_ENABLED before proceeding
- Checks required variables (TYPE, URL) are set
- Returns detailed debug logging for troubleshooting
- Caller (port_monitor.sh) handles indefinite retries

---

## scripts/ui.sh

**Purpose:** Provides logging and UI utilities for consistent, colorized output across all scripts.

### What it Does

**Color Definitions:**
- Defines ANSI color codes: red, green, blue, cyan, yellow, reset, bold
- Used for consistent terminal output across all scripts

**Log Level Configuration:**
- Supports 3 levels: 0 (error), 1 (info, default), 2 (debug)
- Reads from `LOG_LEVEL` environment variable (error/info/debug or 0/1/2)
- Exports `_LOG_LEVEL` numeric value for fast comparisons

**Core Logging Functions:**
- `log_error()`: Always shown (stderr)
- `log_info()`: Shown at info and debug levels
- `log_debug()`: Only shown at debug level (stderr)

**Status Indicators:**
- `show_step()`: Blue arrow for operation start
- `show_success()`: Green checkmark for success
- `show_warning()`: Yellow warning symbol
- `show_error()`: Red X for errors
- `show_debug()`: Blue [DEBUG] prefix (debug level only)
- `show_info()`: Empty line for spacing

**Visual Elements:**
- `print_banner()`: Startup banner with version number
- `show_vpn_connected()`: Green success box
- `show_vpn_connected_warning()`: Yellow warning box
- `show_reconnecting()`: Yellow reconnection box

**Service Management:**
- `restart_services()`: Restarts Docker containers via `docker restart` command
- Parses comma-separated service list
- Logs success/failure for each container

**Network Utilities:**
- `get_external_ip()`: Fetches public IP with fallback to multiple services (ifconfig.me, icanhazip.com, api.ipify.org)
- 8-second timeout per service
- Returns first successful result

---

## cmd/monitor/main.go

**Purpose:** Main entrypoint for the health monitoring service that detects VPN failures and triggers reconnections.

### What it Does

**Configuration Loading (`loadConfig()`):**
- Reads environment variables: `CHECK_INTERVAL` (default 15s), `MAX_FAILURES` (default 3)
- Loads debug mode from `_LOG_LEVEL=2`
- Configures parallel checks (`MONITOR_PARALLEL_CHECKS`)
- Enables metrics if `METRICS=true`

**Monitor Loop (`monitorLoop()`):**
- Ticks every `CHECK_INTERVAL` seconds
- Checks for port forwarding signature failure flag (`/tmp/pf_signature_failed`)
- Skips checks during active reconnection (`/tmp/reconnecting` flag exists)
- Calls `checkVPNHealth()` for interface and connectivity tests
- Updates metrics (if enabled): connection status, killswitch status, transfer bytes, port forwarding status
- Tracks failure count and consecutive successes

**Failure Handling:**
- Increments failure count on each failed check
- Shows debug info on first failure (WireGuard status)
- Triggers reconnection after reaching `MAX_FAILURES` threshold
- Kills port forwarding process before reconnect
- Resets failure count after successful reconnection

**Reconnection Trigger (`triggerReconnect()`):**
- Waits for WAN connectivity before proceeding (blocks on `waitForWAN()`)
- Writes to `/tmp/vpn_reconnect_pipe` to signal run.sh
- Records reconnection in metrics

**Main Routine:**
- Sets up signal handling (SIGTERM, SIGINT, SIGQUIT)
- Tests DNS resolution on startup
- Starts metrics server in goroutine (if enabled)
- Runs monitor loop until shutdown signal received

---

## cmd/monitor/health.go

**Purpose:** Implements health check logic for VPN connectivity (interface status, ping tests, HTTP requests).

### What it Does

**Interface Status Check (`isInterfaceUp()`):**
- Verifies `pia` interface exists (`ip link show pia`)
- Checks interface has IP address (`ip addr show pia`)
- Verifies WireGuard peers are configured (`wg show pia peers`)
- Confirms interface is not in DOWN state
- Returns true only if all checks pass

**Connectivity Tests:**
- **Serial Mode** (`checkExternalConnectivitySerial()`): Tests sequentially: ping 1.1.1.1 → ping 8.8.8.8 → HTTP 1.1.1.1
- **Parallel Mode** (`checkExternalConnectivityParallel()`): Tests concurrently with 8s timeout, returns on first success
- Returns true if any test succeeds

**WAN Connectivity Check (`checkWANConnectivity()`):**
- Uses bypass routing table 100 (no firewall changes needed)
- Connects to NIST time servers on TCP port 13 (DAYTIME protocol)
- Tests 5 different NIST servers (129.6.15.28/29, 132.163.96.x, 128.138.140.44)
- Allows distinguishing internet outages from VPN failures

**WAN Wait with Backoff (`waitForWAN()`):**
- Displays "Testing WAN before reconnect" message
- Initial quick check (5s timeout)
- Exponential backoff if down: 5s → 10s → 20s → 40s → 80s → 160s (repeats)
- Blocks indefinitely until WAN returns
- Displays downtime duration when restored
- Records WAN check results in metrics

**Health Check (`checkVPNHealth()`):**
1. Checks interface is up (fast check)
2. Tests external connectivity
3. Retries once after 3-second delay if first connectivity test fails
4. Returns result with timing information
5. Updates metrics with check duration

**Helper Functions:**
- `getCurrentServer()`: Reads server name from `/tmp/meta_cn`
- `getCurrentIP()`: Fetches public IP (tries api.ipify.org, ifconfig.me, icanhazip.com) or falls back to tunnel IP
- `getServerLatency()`: Reads initial connection latency from `/tmp/server_latency`
- `getTransferBytes()`: Parses `wg show pia transfer` output for RX/TX byte counts
- `getLastHandshake()`: Reads WireGuard handshake timestamp
- `isKillswitchActive()`: Checks for nftables/iptables rules
- `getPortForwardingPort()`: Reads port from `/etc/wireguard/port`
- `isPortForwardingActive()`: Returns true if port file exists and contains valid port

---

## cmd/monitor/metrics.go

**Purpose:** Provides Prometheus-compatible metrics endpoint for monitoring VPN status, latency, and failure counts.

### What it Does

**Metrics Structure:**
- Internal state tracking for JSON endpoint
- Prometheus counters, gauges, histograms
- Thread-safe operations with mutex

**Prometheus Metrics:**
- `vpn_health_checks_total`: Total health checks performed
- `vpn_health_checks_successful_total`: Successful checks
- `vpn_health_checks_failed_total`: Failed checks
- `vpn_reconnects_total`: Reconnection count
- `vpn_check_duration_seconds`: Check duration histogram (10ms to 10s)
- `vpn_success_rate`: Success rate (0-1)
- `vpn_bytes_received_total`: Total RX bytes
- `vpn_bytes_transmitted_total`: Total TX bytes
- `vpn_server_latency_milliseconds`: Initial server latency
- `vpn_server_uptime_seconds`: Time connected to current server
- `vpn_wan_checks_total`: WAN connectivity checks
- `vpn_wan_checks_failed_total`: Failed WAN checks
- `vpn_info`: VPN info with server and IP labels
- `vpn_connection_up`: Connection status (1=up, 0=down)
- `vpn_killswitch_active`: Killswitch status
- `vpn_last_handshake_seconds`: Last WireGuard handshake timestamp
- `vpn_port_forwarding_status`: Port forwarding active (1/0)
- `vpn_port_forwarding_port`: Currently forwarded port
- `vpn_uptime_seconds`: Total uptime (gauge function)

**Update Methods:**
- `RecordCheck()`: Records check result, updates success rate
- `RecordWANCheck()`: Tracks WAN connectivity tests
- `UpdateVPNInfo()`: Updates server, IP, transfer bytes, server uptime
- `SetServerLatency()`: Sets initial latency metric
- `RecordReconnect()`: Increments reconnection counter
- `UpdateConnectionStatus()`: Sets connection up/down
- `UpdateKillswitchStatus()`: Sets killswitch active/inactive
- `UpdateLastHandshake()`: Updates WireGuard handshake time
- `UpdatePortForwarding()`: Updates port forwarding status and port number

**Metrics Server (`startMetricsServer()`):**
- Listens on port specified by `METRICS_PORT` (default: 9090)
- Default `/metrics` endpoint: Prometheus format
- Query parameter `?format=json`: Returns JSON statistics
- JSON format includes: uptime, checks, success rate, server info, transfer bytes, latency

**JSON Stats (`GetStats()`):**
- Returns map with human-readable statistics
- Formatted uptime and durations
- Success rates as percentages
- Current server, IP, transfer totals

---

## cmd/proxy/main.go

**Purpose:** Implements SOCKS5 and HTTP proxy server that routes traffic through the VPN tunnel.

### What it Does

**Configuration:**
- Reads credentials from Docker secrets (`/run/secrets/proxy_user`, `/run/secrets/proxy_pass`) or environment variables
- Configures HTTP proxy port (default: 8888) and SOCKS5 port (default: 1080)
- Initializes at runtime (not package init) for proper secret/env variable handling

**HTTP Proxy (`HTTPProxyHandler`):**
- Handles HTTP requests via standard RoundTrip
- Handles HTTPS via CONNECT method (establishes tunnel)
- Optional authentication: Basic auth with constant-time comparison (prevents timing attacks)
- Removes proxy-specific headers before forwarding
- Bidirectional copy for tunneled connections

**SOCKS5 Proxy (`handleSOCKS5()`):**
- SOCKS5 handshake: version check (0x05)
- Authentication: Username/password (0x02) if configured, or no auth (0x00)
- Supports CONNECT command (0x01) only
- Address types: IPv4 (0x01), domain (0x03), rejects IPv6 (0x04)
- Connects to destination with 10-second timeout
- Bidirectional traffic copy between client and destination

**Main Server:**
- Starts SOCKS5 listener in goroutine
- Starts HTTP server on main thread
- Both servers log authentication status on startup
- HTTP server timeouts: 30s read/write, 120s idle
- Concurrent connection handling via goroutines

---

## cmd/portforward/main.go

**Purpose:** Main entrypoint for the port forwarding service that manages PIA port forwarding signatures and bindings.

### What it Does

**Configuration Loading (`loadConfig()`):**
- Reads from temp files: `/tmp/pia_token`, `/tmp/client_ip`, `/tmp/meta_cn`, `/tmp/pf_gateway`
- Validates PF gateway is available (not empty or "null")
- Configures intervals: bind interval (default 900s/15min), signature refresh (default 31 days)
- Sets signature safety margin (default 24 hours before expiry)
- Reads `PORT_FILE` path (default: `/etc/wireguard/port`)
- Loads webhook URL and debug mode

**Main Routine:**
- Creates PIA client with interface binding
- Creates keepalive manager
- Sets up signal handling (SIGTERM, SIGINT, SIGQUIT)
- Runs port forwarding service via `manager.Run()`
- On error during initial setup: Shows warning box, creates completion flag, blocks forever

**Display Functions:**
- `showStep()`, `showSuccess()`, `showError()`, `showWarning()`: Colorized output
- `showVPNConnected()`: Green success box
- `showVPNConnectedWarning()`: Yellow warning box (for partial success)
- `debugLog()`: Conditional debug output with timestamps

---

## cmd/portforward/client.go

**Purpose:** Handles HTTP communication with PIA's port forwarding API (signature requests, port binding).

### What it Does

**Client Initialization (`NewPIAClient()`):**
- Creates HTTP client with custom transport
- Binds outgoing connections to `pia` interface IP address
- Disables TLS verification (`InsecureSkipVerify: true`, equivalent to curl `-k`)
- Sets timeouts: 10s connection, 30s keepalive

**Get Signature (`GetSignature()`):**
- Sends request to `https://<PF_GATEWAY>:19999/getSignature?token=<TOKEN>`
- 2-second initial delay (matches bash behavior)
- Parses JSON response: `{ "status": "OK", "payload": "...", "signature": "..." }`
- Returns `APIError` if status != "OK" (triggers reconnection)
- Returns signature response on success

**Get Signature with Retry (`GetSignatureWithRetry()`):**
- Time-based retry: 5 minutes for initial setup, 2 minutes for refresh
- Exponential backoff: 2s → 4s → 8s → 16s → 30s (capped)
- Immediately stops on API errors (status != OK) and logs error - triggers reconnection
- Logs network errors only when retry duration is exceeded
- Context-aware: cancels on shutdown signal
- Returns error after retry duration exceeded

**Bind Port (`BindPort()`):**
- Sends request to `https://<PF_GATEWAY>:19999/bindPort?payload=<PAYLOAD>&signature=<SIGNATURE>`
- Parses JSON response: `{ "status": "OK", "message": "..." }`
- Returns `APIError` if status != "OK"
- Returns nil on success

**Bind Port with Retry (`BindPortWithRetry()`):**
- Time-based retry: 3 minutes for all bind operations
- Exponential backoff with 30s cap
- Immediately stops on API errors (status != OK) and logs error - triggers signature refresh
- Logs network errors only when retry duration is exceeded
- Context-aware cancellation
- Returns error after retry duration exceeded

**Parse Payload (`ParsePayload()`):**
- Base64-decodes payload string
- Parses JSON: `{ "port": 12345, "expires_at": "2024-01-15T12:34:56.789Z" }`
- Strips milliseconds from ISO8601 timestamp for parsing
- Returns port number and expiry time

**Error Handling:**
- `APIError` type: Distinguishes API failures (bad signature) from network failures
- Network errors: Retry with backoff
- API errors: Stop retrying, trigger reconnection (caller responsibility)

---

## cmd/portforward/keepalive.go

**Purpose:** Manages port forwarding signature refresh and binding keepalive with optimized failure escalation.

### What it Does

**Initial Setup (`initialSetup()`):**
1. Acquires initial signature (5-minute retry window)
2. Parses payload to extract port and expiry
3. Performs initial port binding (3-minute retry window)
4. Writes port to file (`/etc/wireguard/port`)
5. Notifies port monitor via named pipe (`/tmp/port_change_pipe`)
6. Sends webhook notification (async)
7. Displays success message with port, expiry, and keepalive settings
8. Shows VPN connected box
9. Creates `/tmp/port_forwarding_complete` flag
10. On failure: Displays warning, blocks forever (no exit, allows VPN to stay up)

**Keepalive Loop (`keepaliveLoop()`):**
- Ticks every `BIND_INTERVAL` (default 900s/15min)
- Context-aware: exits gracefully on shutdown signal
- Checks if signature needs refresh:
  - **Reason 1**: Scheduled refresh (31-day interval)
  - **Reason 2**: Signature expiring soon (within 24-hour safety margin)
- Performs regular bind if signature still valid
- **Bind failure escalation (maintains port within 24-minute death window):**
  - **API error (status != OK)**: Immediately escalates to signature refresh
  - **Network error after 3 minutes**: Escalates to signature refresh
- **Port death timing safety:** 15min bind + 3min bind retry + 2min signature retry = 20min max before reconnect (4-minute safety margin)

**Signature Refresh (`refreshSignature()`):**
1. Requests new signature (2-minute retry window)
2. Parses new payload
3. Compares port: displays change notification if different
4. Updates port file and notifies monitor if changed
5. Sends webhook notification (async)
6. Updates internal state (port, payload, signature, expiry, timestamp)
7. Binds with new signature (3-minute retry window)
8. Displays success message
9. **Signature failure escalation:**
   - **API error (status != OK)**: Immediately triggers reconnection
   - **Network error after 2 minutes**: Triggers reconnection

**Refresh Failure Handling (`handleRefreshFailure()`):**
- Displays error message
- Creates `/tmp/pf_signature_failed` flag
- Exits with code 1 (triggers reconnection in run.sh)

**Port Change Notification (`notifyPortChange()`):**
- Non-blocking write to `/tmp/port_change_pipe`
- Runs in goroutine to avoid blocking keepalive loop
- Gracefully handles case where pipe doesn't exist yet

**Webhook Notification (`sendWebhook()`):**
- Fetches public VPN IP (5-second timeout)
- Builds JSON payload: `{ "port": 12345, "ip": "x.x.x.x", "timestamp": "..." }`
- Sends POST to configured webhook URL (not implemented in current version)

**State Management:**
- Tracks: current port, payload, signature, expiry time, last signature time
- Counts: bind operations, signature refreshes
- Thread-safe with mutex

---

## Dockerfile

**Purpose:** Multi-stage build configuration that creates a minimal Alpine-based image with WireGuard, firewall tools, and Go binaries.

### What it Does

**Stage 1: Go Builder (golang:1.23-alpine):**
- Copies Go module files and source code
- Builds three binaries with maximum optimization:
  - `proxy`: SOCKS5/HTTP proxy server
  - `monitor`: VPN health monitor
  - `portforward`: Port forwarding manager
- Build flags: `CGO_ENABLED=0`, `-ldflags="-w -s"` (strips symbols), `-trimpath`
- Marks binaries executable in builder stage (prevents duplication in final image)

**Stage 2: Runtime (alpine:3.19):**
- Installs minimal dependencies:
  - `bash`: Shell for scripts
  - `curl`: HTTP requests for API calls
  - `jq`: JSON parsing
  - `ca-certificates`: TLS certificate validation
  - `wireguard-tools-wg`: WireGuard interface management
  - `nftables`, `iptables`: Firewall backends
  - `iproute2-minimal`: Network routing commands
- Cleanup: Removes APK cache, docs, man pages, Python, temporary files
- Strips executables to reduce size
- Copies Go binaries from builder (already executable)
- Copies PIA certificate and shell scripts
- Makes scripts executable (not binaries, prevents duplication)

**Configuration:**
- Creates `/etc/wireguard` directory
- Sets environment variable defaults (TZ, LOG_LEVEL, ports, intervals)
- Exposes ports: 1080 (SOCKS5), 8888 (HTTP proxy), 9090 (metrics)
- Entrypoint: `/app/run.sh`

**Optimization Techniques:**
- Multi-stage build reduces final image size
- Executables marked in builder stage only
- Stripped symbols reduce binary size
- Minimal runtime dependencies
- Aggressive cleanup of unnecessary files

---

## Additional Files

### docker-compose.yml
**Purpose:** Example Docker Compose configuration demonstrating pia-tun deployment with dependent services.

### CLAUDE.md
**Purpose:** Project documentation and guidance for AI assistants working with this codebase.

### README.md
**Purpose:** User-facing documentation with setup instructions, environment variables, and usage examples.

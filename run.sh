#!/bin/bash

set -euo pipefail

source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/vpn.sh
source /app/scripts/verify_connection.sh
source /app/scripts/proxy_go.sh

# OPTIMIZED: Set defaults inline, export only what's needed by child processes
PORT_FORWARDING=${PORT_FORWARDING:-false}
IPV6_ENABLED=${IPV6_ENABLED:-false}
DNS=${DNS:-pia}
LOCAL_NETWORKS=${LOCAL_NETWORKS:-""}
LOCAL_PORTS=${LOCAL_PORTS:-""}
HANDSHAKE_TIMEOUT=${HANDSHAKE_TIMEOUT:-180}
HC_INTERVAL=${HC_INTERVAL:-15}
HC_FAILURE_WINDOW=${HC_FAILURE_WINDOW:-30}
PROXY_ENABLED=${PROXY_ENABLED:-false}
SOCKS5_PORT=${SOCKS5_PORT:-1080}
HTTP_PROXY_PORT=${HTTP_PROXY_PORT:-8888}
PORT_SYNC_ENABLED=${PORT_SYNC_ENABLED:-false}

# Auto-enable PORT_SYNC_ENABLED and PORT_FORWARDING if PORT_SYNC_CLIENT or PORT_SYNC_CMD is set
if [ -n "${PORT_SYNC_CLIENT:-}" ] || [ -n "${PORT_SYNC_CMD:-}" ]; then
    PORT_SYNC_ENABLED=true
    PORT_FORWARDING=true
fi

# Export only what child processes actually need
export PORT_FORWARDING
export IPV6_ENABLED DNS LOCAL_NETWORKS LOCAL_PORTS
export HC_INTERVAL HC_FAILURE_WINDOW HANDSHAKE_TIMEOUT
export PROXY_ENABLED SOCKS5_PORT HTTP_PROXY_PORT
export PORT_SYNC_ENABLED PORT_SYNC_CLIENT PORT_SYNC_URL PORT_SYNC_USER PORT_SYNC_PASS PORT_SYNC_CMD
export METRICS METRICS_PORT
export IPT_CMD IP6T_CMD

# Boolean flags (set once, check many times)
PF_ENABLED=false
[ "$PORT_FORWARDING" = "true" ] && PF_ENABLED=true

PROXY_ENABLED_FLAG=false
[ "$PROXY_ENABLED" = "true" ] && PROXY_ENABLED_FLAG=true

trap cleanup SIGTERM SIGINT SIGQUIT

cleanup() {
    show_info
    show_step "Shutting down..."

    show_debug "Cleanup: Stopping proxies (enabled=$PROXY_ENABLED_FLAG)"
    $PROXY_ENABLED_FLAG && stop_proxies

    show_debug "Cleanup: Killing monitor processes"
    pkill -f "monitor" 2>/dev/null || true

    show_debug "Cleanup: Killing port forwarding processes"
    pkill -f "portforward" 2>/dev/null || true
    pkill -f "port_monitor.sh" 2>/dev/null || true

    show_debug "Cleanup: Tearing down VPN tunnel"
    teardown_wireguard

    # CRITICAL: Clean up killswitch to prevent network namespace pollution
    # This prevents orphaned DROP rules from affecting subsequent containers
    # in Kubernetes environments where network namespaces may be reused
    show_debug "Cleanup: Removing killswitch rules"
    cleanup_killswitch

    show_success "Cleanup complete"
    exit 0
}

initial_connect() {
    local restart="${1:-false}"
    
    show_debug "initial_connect called"

    [ ! -f /tmp/reconnecting ] && print_banner

    show_debug "Clearing /etc/resolv.conf"
    echo > /etc/resolv.conf

    # Setup baseline killswitch first (includes bypass route allowances)
    if [ "$restart" != "true" ]; then
        setup_baseline_killswitch || {
            show_error "CRITICAL: Killswitch setup failed - cannot safely connect to VPN"
            show_error "This is a security issue. Exiting to prevent potential IP leaks."
            exit 1
        }
        show_info

        # Debug: Show killswitch rules
        if [ $_LOG_LEVEL -ge 2 ]; then
            show_info
            show_debug "Baseline killswitch rules:"
            iptables -L VPN_OUT -n -v 2>/dev/null | head -20 | while read -r line; do
                show_debug "  $line"
            done
            show_info
        fi
    else
        show_debug "Skipping baseline killswitch setup (restart=true)"
    fi

    # Only capture real IP if not already captured
    if [ ! -f /tmp/real_ip ]; then
        capture_real_ip
        show_info
    else
        show_debug "Real IP already captured: $(cat /tmp/real_ip 2>/dev/null || echo 'unknown')"
    fi

    # VPN setup (uses surgical exemptions internally)
    /app/scripts/vpn.sh "$restart" || {
        show_error "vpn.sh failed with exit code $?"
        return 1
    }

    # Save server latency for metrics
    if [ -f /tmp/server_latency_temp ]; then
        show_debug "Saving server latency: $(cat /tmp/server_latency_temp)"
        mv /tmp/server_latency_temp /tmp/server_latency
    fi

    show_step "Establishing VPN connection..."
    if bring_up_wireguard /etc/wireguard/pia0.conf; then
        show_success "VPN tunnel established"
        show_debug "WireGuard interface brought up successfully"
    else
        show_error "Failed to bring up tunnel, error code $?"
        return 1
    fi

    # Add VPN to killswitch (now it's safe to route through VPN)
    if [ "$restart" != "true" ]; then
        add_vpn_to_killswitch || {
            show_error "CRITICAL: Failed to add VPN to killswitch"
            show_error "VPN traffic may not be properly protected. Tearing down VPN."
            teardown_wireguard
            exit 1
        }

        # Clean up any leftover temporary exemptions from setup phase
        show_debug "Cleaning up temporary exemptions from VPN setup"
        remove_all_temporary_exemptions

        show_ruleset_stats
    else
        show_debug "Adding VPN to killswitch (reconnection, suppressing output)"
        add_vpn_to_killswitch >/dev/null 2>&1 || {
            show_error "CRITICAL: Failed to add VPN to killswitch during reconnection"
            return 1
        }

        # Clean up any leftover temporary exemptions from reconnection
        show_debug "Cleaning up temporary exemptions from reconnection"
        remove_all_temporary_exemptions >/dev/null 2>&1
    fi

    show_info
    show_step "Verifying connection..."
    if verify_connection; then
        show_debug "Connection verification passed"
        # Signal connection ready to monitor (provides verified server/IP data)
    else
        show_warning "Connection verification found issues"
        show_debug "verify_connection returned non-zero exit code"
        # Still try to signal connection (VPN may be working despite verification issues)
        show_info
    fi
    if $METRICS; then
        signal_connection_ready
    fi
}

perform_reconnection() {
    show_reconnecting
    touch /tmp/reconnecting

    if $PF_ENABLED; then
        show_debug "Stopping port forwarding processes"
        pkill -9 -f "portforward" 2>/dev/null || true
        pkill -9 -f "port_monitor.sh" 2>/dev/null || true
    fi
    
    if $PROXY_ENABLED_FLAG; then
        show_debug "Stopping proxies"
        stop_proxies
    fi

    # Teardown VPN (removes from killswitch first, then tears down interface)
    teardown_wireguard

    local attempt=0
    local delay=5
    local max_delay=160

    while true; do
        if initial_connect "true"; then
            show_debug "Reconnection: VPN established successfully"
            
            # Start proxies after VPN is up
            if $PROXY_ENABLED_FLAG; then
                show_debug "Restarting proxies"
                start_proxies >/dev/null 2>&1
            fi

            if $PF_ENABLED; then
                show_debug "Starting port forwarding service"
                /usr/local/bin/portforward & disown

                # Wait for port forwarding to complete (max 30s)
                local waited=0
                show_debug "Waiting for port forwarding completion (max 30s)"
                while [ $waited -lt 30 ]; do
                    if [ -f /tmp/port_forwarding_complete ]; then
                        show_debug "Port forwarding completed after ${waited}s"
                        rm -f /tmp/port_forwarding_complete
                        break
                    fi
                    sleep 1
                    waited=$((waited + 1))
                done
                [ $waited -ge 30 ] && show_debug "Port forwarding wait timed out after 30s"
            else
                show_vpn_connected
            fi

            show_step "Health monitor still running..."
            show_success "Check interval: ${HC_INTERVAL}s, Failure window: ${HC_FAILURE_WINDOW}s"

            # Restart port monitor if both PF and API are enabled (now after health status)
            if $PF_ENABLED && [ "$PORT_SYNC_ENABLED" = "true" ]; then
                show_info
                show_step "Restarting port monitor..."
                show_debug "Launching port_monitor.sh"
                /app/scripts/port_monitor.sh & disown
            fi
            
            sleep 2
            
            show_debug "Removing reconnecting flag"
            rm -f /tmp/reconnecting
            return 0
        else
            show_error "Reconnection failed"
            show_warning "Will retry in $delay seconds"
            show_debug "initial_connect returned error during reconnection"
        fi

        attempt=$((attempt+1))
        sleep "$delay"

        delay=$((delay * 2))
        [ "$delay" -gt "$max_delay" ] && delay="$max_delay"
    done
}

main_loop() {
    
    # Start proxies first if enabled
    if $PROXY_ENABLED_FLAG; then
        start_proxies
    fi

    if $PF_ENABLED; then
        show_debug "Starting port forwarding service"
        /usr/local/bin/portforward & disown

        # Wait for port forwarding to complete (max 30s)
        local waited=0
        show_debug "Waiting for port forwarding completion (max 30s)"
        while [ $waited -lt 30 ]; do
            if [ -f /tmp/port_forwarding_complete ]; then
                show_debug "Port forwarding completed after ${waited}s"
                rm -f /tmp/port_forwarding_complete
                break
            fi
            sleep 1
            waited=$((waited + 1))
        done
        [ $waited -ge 30 ] && show_debug "Port forwarding wait timed out after 30s"
    else
        show_vpn_connected
    fi

    show_step "Starting health monitor..."
    /usr/local/bin/monitor &
    show_success "Health monitor active"
    show_success "Check interval: ${HC_INTERVAL}s, Failure window: ${HC_FAILURE_WINDOW}s"

    if [ "$METRICS" = "true" ]; then
        show_success "Metrics available on port ${METRICS_PORT:-9090}"
        show_info "      Prometheus:  http://<container-ip>:${METRICS_PORT:-9090}/metrics"
        show_info "      JSON:        http://<container-ip>:${METRICS_PORT:-9090}/metrics?format=json"
    fi

    # Start port monitor if PF and API updater are enabled
    if $PF_ENABLED && [ "$PORT_SYNC_ENABLED" = "true" ]; then
        show_debug "Starting port monitor (PF + API enabled)"
        /app/scripts/port_monitor.sh & disown
    fi

    sleep 1

  # Create named pipes for monitor communication
  RECONNECT_PIPE="/tmp/vpn_reconnect_pipe"
  [ -p "$RECONNECT_PIPE" ] || mkfifo "$RECONNECT_PIPE"

  CONNECTION_PIPE="/tmp/vpn_connection_pipe"
  [ -p "$CONNECTION_PIPE" ] || mkfifo "$CONNECTION_PIPE"

  show_debug "Entering reconnection monitor loop (blocking on pipe)"
  while true; do
      # This blocks until monitor writes to the pipe
      if read -r reason < "$RECONNECT_PIPE"; then
          show_debug "Reconnection requested: ${reason:-no reason given}"

          if perform_reconnection; then
              show_debug "Reconnection successful"
          else
              show_warning "Retrying..."
              sleep 5
          fi
      fi
  done
}

# Signal connection ready to the monitor via named pipe
# This provides the verified connection data directly, avoiding race conditions
signal_connection_ready() {
    local pipe="/tmp/vpn_connection_pipe"

    # Only signal if pipe exists (monitor is running)
    if [ ! -p "$pipe" ]; then
        show_debug "Connection pipe not found, skipping signal"
        return 0
    fi

    local server=$(cat /tmp/meta_cn 2>/dev/null || echo "")
    if [ -z "$server" ]; then
        show_debug "No server info available, skipping connection signal"
        return 1
    fi

    # Get the verified external IP
    local vpn_ip=$(cat /tmp/server_endpoint 2>/dev/null || echo "")
    if [ -z "$vpn_ip" ]; then
        show_debug "Could not get external IP, skipping connection signal"
        return 1
    fi

    local timestamp=$(date +%s)
    local data="${server}|${vpn_ip}|${timestamp}"

    show_debug "Signaling connection ready: $data"

    # Non-blocking write to pipe (timeout prevents hanging if no reader)
    if timeout 2 bash -c "echo '$data' > '$pipe'" 2>/dev/null; then
        show_debug "Connection signal sent successfully"
        return 0
    else
        show_debug "Failed to send connection signal (no reader or timeout)"
        return 1
    fi
}

# Debug: Show environment configuration
show_debug "Environment configuration:"
show_debug "  PORT_FORWARDING=$PORT_FORWARDING"
show_debug "  IPV6_ENABLED=$IPV6_ENABLED"
show_debug "  DNS=$DNS"
show_debug "  LOCAL_NETWORKS=$LOCAL_NETWORKS"
show_debug "  LOCAL_PORTS=$LOCAL_PORTS"
show_debug "  HC_INTERVAL=$HC_INTERVAL"
show_debug "  HC_FAILURE_WINDOW=$HC_FAILURE_WINDOW"
show_debug "  HANDSHAKE_TIMEOUT=$HANDSHAKE_TIMEOUT"
show_debug "  PROXY_ENABLED=$PROXY_ENABLED"
show_debug "  PORT_SYNC_ENABLED=$PORT_SYNC_ENABLED"
show_debug "  METRICS=$METRICS"
show_debug "  LOG_LEVEL=${LOG_LEVEL} (numeric: $_LOG_LEVEL)"

# CAP_NET_ADMIN check – add after env debug logs (end of section 23-37)
CAP_NET_ADMIN=0x1000  # CAP_NET_ADMIN is capability 12 (1 << 12 = 0x1000)
cap_eff=$(grep -i '^CapEff:' /proc/self/status | awk '{print $2}')
# Convert hex string to decimal for arithmetic comparison
cap_eff_decimal=$((0x$cap_eff))
if [ $((cap_eff_decimal & CAP_NET_ADMIN)) -eq 0 ]; then
    show_error "Container missing CAP_NET_ADMIN capability"
    show_error "Required for firewall management. Add '--cap-add=NET_ADMIN' to docker run"
    exit 1
fi
show_debug "CAP_NET_ADMIN check passed (CapEff: 0x$cap_eff)"

show_debug "Starting initial connection"
if initial_connect; then
    main_loop
else
    show_error "Initial connection failed"
    show_debug "Exiting with error code 1"
    exit 1
fi

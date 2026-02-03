#!/bin/bash

set -euo pipefail

source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/vpn.sh
source /app/scripts/verify_connection.sh
source /app/scripts/proxy_go.sh

# OPTIMIZED: Set defaults inline, export only what's needed by child processes
PF_ENABLED=${PF_ENABLED:-false}
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
PS_ENABLED=${PS_ENABLED:-false}
PIA_CN=${PIA_CN:-""}
PIA_IP=${PIA_IP:-""}

# Auto-enable PS_ENABLED and PF_ENABLED if PS_CLIENT or PS_CMD is set
if [ -n "${PS_CLIENT:-}" ] || [ -n "${PS_CMD:-}" ]; then
    PS_ENABLED=true
    PF_ENABLED=true
fi

# Export only what child processes actually need
export PF_ENABLED
export IPV6_ENABLED DNS LOCAL_NETWORKS LOCAL_PORTS
export HC_INTERVAL HC_FAILURE_WINDOW HANDSHAKE_TIMEOUT
export PROXY_ENABLED SOCKS5_PORT HTTP_PROXY_PORT
export PS_ENABLED PS_CLIENT PS_URL PS_USER PS_PASS PS_CMD
export METRICS METRICS_PORT
export IPT_CMD IP6T_CMD
export PIA_CN PIA_IP

trap cleanup SIGTERM SIGINT SIGQUIT

cleanup() {
    show_step "Shutting down..."

    show_debug "Cleanup: Stopping proxies (enabled=$PROXY_ENABLED)"
    stop_proxies

    show_debug "Cleanup: Killing monitor processes"
    pkill -f "monitor" 2>/dev/null || true

    show_debug "Cleanup: Killing port forwarding processes"
    pkill -f "portforward" 2>/dev/null || true
    pkill -f "port_monitor.sh" 2>/dev/null || true

    show_debug "Cleanup: flag files"
    rm -f /tmp/port_forwarding_complete
    rm -f /tmp/reconnecting
    rm -f /tmp/monitor_up
    rm -f /tmp/killswitch_up

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

reconnect() {
    show_reconnecting
    touch /tmp/reconnecting

    if $PF_ENABLED; then
        show_debug "Stopping port forwarding processes"
        pkill -9 -f "portforward" 2>/dev/null || true
        pkill -9 -f "port_monitor.sh" 2>/dev/null || true
    fi

    pkill -9 -f "cacher" 2>/dev/null || true

    # Teardown VPN (removes from killswitch first, then tears down interface)
    teardown_wireguard

    connect_loop
}

connect() {
    echo > /etc/resolv.conf

    /app/scripts/vpn.sh || {
        show_error "vpn.sh failed with exit code $?"
        return 1
    }

    show_step "Establishing VPN connection..."
    if bring_up_wireguard /etc/wireguard/pia0.conf; then
        show_success "VPN tunnel established"
        show_debug "WireGuard interface brought up successfully"
    else
        show_error "Failed to bring up tunnel, error code $?"
        return 1
    fi

    if [ ! -f /tmp/reconnecting ]; then    
        add_vpn_to_killswitch || {
            show_error "CRITICAL: Failed to add VPN to killswitch"
            show_error "VPN traffic may not be properly protected. Tearing down VPN."
            teardown_wireguard
            exit 1
        }

        show_debug "Cleaning up temporary exemptions from VPN setup"
        remove_all_temporary_exemptions
        show_ruleset_stats
    else
        silent add_vpn_to_killswitch || {
            show_error "CRITICAL: Failed to add VPN to killswitch"
            show_error "VPN traffic may not be properly protected. Tearing down VPN."
            teardown_wireguard
            exit 1
        }

        show_debug "Cleaning up temporary exemptions from VPN setup"
        silent remove_all_temporary_exemptions
    fi

    show_step "Verifying connection..."
    if verify_connection; then
        show_debug "Connection verification passed"
        show_vpn_connected
    else
        show_warning "Connection verification found issues"
        show_debug "verify_connection returned non-zero exit code"
        show_vpn_connected_warning
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
    fi

    /usr/local/bin/cacher & disown

    if $PROXY_ENABLED; then
        if [ ! -f /tmp/proxy.pid ]; then
            start_proxies
        else
            show_step "Proxy servers still running"
            show_success "Proxy servers ready"
        fi
    fi


    if [ ! -f /tmp/monitor_up ]; then
        show_step "Starting health monitor..."
        /usr/local/bin/monitor &
        touch /tmp/monitor_up
    else
        show_step "Health monitor resuming..."
    fi
    show_success "Check interval: ${HC_INTERVAL}s, Failure window: ${HC_FAILURE_WINDOW}s"

    if [ "$METRICS" = "true" ]; then
        if [ ! -f /tmp/reconnecting ]; then
            show_success "Metrics available on port ${METRICS_PORT:-9090}"
            show_info "      Prometheus:  http://<container-ip>:${METRICS_PORT:-9090}/metrics"
            show_info "      JSON:        http://<container-ip>:${METRICS_PORT:-9090}/metrics?format=json"
            show_info "      Health:      http://<container-ip>:${METRICS_PORT:-9090}/health"
        fi
    else
        show_info "      Health:      http://<container-ip>:${METRICS_PORT:-9090}/health"
    fi

    # Start port monitor if PF and API updater are enabled
    if $PS_ENABLED; then
        if [ ! -f /tmp/reconnecting ]; then
            show_step "Port monitor starting (sync: $PS_CLIENT)"
            show_debug "Starting port monitor (PF + API enabled)"
            /app/scripts/port_monitor.sh & disown
        else
            show_step "Restarting port monitor..."
            show_debug "Launching port_monitor.sh"
            /app/scripts/port_monitor.sh & disown
        fi
    fi

    if $METRICS; then
        signal_connection_ready
    fi
}

initialize() {
    print_banner

    show_debug "Removing stale flag files"
    rm -f /tmp/port_forwarding_complete
    rm -f /tmp/reconnecting
    rm -f /tmp/monitor_up
    rm -f /tmp/killswitch_up
    ip link delete pia0 2>/dev/null || true

    show_debug "Clearing /etc/resolv.conf"
    echo > /etc/resolv.conf

    setup_baseline_killswitch || {
        show_error "CRITICAL: Killswitch setup failed - cannot safely connect to VPN"
        show_error "This is a security issue. Exiting to prevent potential IP leaks."
        exit 1
    }

    if [ $_LOG_LEVEL -ge 2 ]; then
        show_debug "Baseline killswitch rules:"
        iptables -L VPN_OUT -n -v 2>/dev/null | head -20 | while read -r line; do
            show_debug "  $line"
        done
    fi

    capture_real_ip || true
}

connect_loop() {
    local attempt=0
    local delay=5
    local max_delay=120
    touch /tmp/monitor_wait

    while true; do
        if connect "true"; then
            show_debug "Connection successful"
            rm -f /tmp/reconnecting
            rm -f /tmp/monitor_wait
            return 0
        else
            show_error "Connection failed"
            show_warning "Will retry in $delay seconds"
            show_info
        fi

        attempt=$((attempt+1))
        sleep "$delay"

        delay=$((delay * 2))
        [ "$delay" -gt "$max_delay" ] && delay="$max_delay"
    done
}

main_loop() {
  show_debug "Entering reconnection monitor loop (blocking on pipe)"
  while true; do
      if read -r reason < "$RECONNECT_PIPE"; then
          show_debug "Reconnection requested: ${reason:-no reason given}"

          if reconnect; then
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

    local server=$(cat /tmp/pia_cn 2>/dev/null || echo "")
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
show_debug "  PF_ENABLED=$PF_ENABLED"
show_debug "  IPV6_ENABLED=$IPV6_ENABLED"
show_debug "  DNS=$DNS"
show_debug "  LOCAL_NETWORKS=$LOCAL_NETWORKS"
show_debug "  LOCAL_PORTS=$LOCAL_PORTS"
show_debug "  HC_INTERVAL=$HC_INTERVAL"
show_debug "  HC_FAILURE_WINDOW=$HC_FAILURE_WINDOW"
show_debug "  HANDSHAKE_TIMEOUT=$HANDSHAKE_TIMEOUT"
show_debug "  PROXY_ENABLED=$PROXY_ENABLED"
show_debug "  PS_ENABLED=$PS_ENABLED"
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

# Create named pipes for monitor communication
RECONNECT_PIPE="/tmp/vpn_reconnect_pipe"
[ -p "$RECONNECT_PIPE" ] || mkfifo "$RECONNECT_PIPE"

CONNECTION_PIPE="/tmp/vpn_connection_pipe"
[ -p "$CONNECTION_PIPE" ] || mkfifo "$CONNECTION_PIPE"

initialize
connect_loop
main_loop

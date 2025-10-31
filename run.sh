#!/bin/bash

set -e

source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/vpn.sh
source /app/scripts/verify_connection.sh
source /app/scripts/proxy_go.sh

# OPTIMIZED: Set defaults inline, export only what's needed by child processes
DISABLE_IPV6=${DISABLE_IPV6:-true}
DNS=${DNS:-pia}
LOCAL_NETWORK=${LOCAL_NETWORK:-""}
KILLSWITCH_EXEMPT_PORTS=${KILLSWITCH_EXEMPT_PORTS:-""}
HANDSHAKE_TIMEOUT=${HANDSHAKE_TIMEOUT:-180}
CHECK_INTERVAL=${CHECK_INTERVAL:-15}
MAX_FAILURES=${MAX_FAILURES:-2}
RESTART_SERVICES=${RESTART_SERVICES:-""}
PROXY_ENABLED=${PROXY_ENABLED:-false}
SOCKS5_PORT=${SOCKS5_PORT:-1080}
HTTP_PROXY_PORT=${HTTP_PROXY_PORT:-8888}
PORT_API_ENABLED=${PORT_API_ENABLED:-false}

# Auto-enable PORT_API if PORT_API_TYPE is set
[ -n "${PORT_API_TYPE:-}" ] && PORT_API_ENABLED=true

# Export only what child processes actually need
export DISABLE_IPV6 DNS LOCAL_NETWORK KILLSWITCH_EXEMPT_PORTS
export CHECK_INTERVAL MAX_FAILURES HANDSHAKE_TIMEOUT
export PROXY_ENABLED SOCKS5_PORT HTTP_PROXY_PORT
export PORT_API_ENABLED PORT_API_TYPE PORT_API_URL PORT_API_USER PORT_API_PASS PORT_API_CMD
export RESTART_SERVICES MONITOR_DEBUG METRICS METRICS_PORT
export MONITOR_PARALLEL_CHECKS MONITOR_FAST_FAIL MONITOR_WATCH_HANDSHAKE
export WEBHOOK_URL

# Boolean flags (set once, check many times)
PF_ENABLED=false
[ "$PORT_FORWARDING" = "true" ] && PF_ENABLED=true

DEBUG_MODE=false
[ "$MONITOR_DEBUG" = "true" ] && DEBUG_MODE=true

PROXY_ENABLED_FLAG=false
[ "$PROXY_ENABLED" = "true" ] && PROXY_ENABLED_FLAG=true

cleanup() {
    echo ""
    show_step "Shutting down..."
    $PROXY_ENABLED_FLAG && stop_proxies
    pkill -f "monitor" 2>/dev/null || true
    pkill -f "port_forwarding.sh" 2>/dev/null || true
    pkill -f "port_monitor.sh" 2>/dev/null || true
    teardown_wireguard
    show_success "Cleanup complete"
    exit 0
}

trap cleanup SIGTERM SIGINT SIGQUIT

initial_connect() {
    local restart="${1:-false}"

    [ ! -f /tmp/reconnecting ] && print_banner

    # Only capture real IP if not already captured
    if [ ! -f /tmp/real_ip ]; then
        capture_real_ip
        echo ""
    fi

    # Setup baseline killswitch first (includes bypass route allowances)
    if [ "$restart" != "true" ]; then
        setup_baseline_killswitch
        echo ""

        # Debug info
        if $DEBUG_MODE; then
            echo ""
            echo "  ${blu}[DEBUG]${nc} Baseline killswitch rules:"
            if command -v nft >/dev/null 2>&1; then
                nft list chain inet vpn_filter output 2>/dev/null | head -20 | sed 's/^/    /'
            else
                iptables -L VPN_OUT -n -v 2>/dev/null | head -20 | sed 's/^/    /'
            fi
            echo ""
        fi
    fi

    # VPN setup (uses surgical exemptions internally)
    /app/scripts/vpn.sh "$restart" || return 1

    # Save server latency for metrics
    [ -f /tmp/server_latency_temp ] && mv /tmp/server_latency_temp /tmp/server_latency

    show_step "Establishing VPN connection..."
    bring_up_wireguard /etc/wireguard/pia.conf && show_success "VPN tunnel established" || \
        { show_error "Failed to bring up tunnel"; return 1; }

    # Add VPN to killswitch (now it's safe to route through VPN)
    if [ "$restart" != "true" ]; then
        add_vpn_to_killswitch
        show_ruleset_stats
    else
        add_vpn_to_killswitch >/dev/null 2>&1
    fi

    echo ""
    show_step "Verifying connection..."
    verify_connection && echo "" || { show_warning "Connection verification found issues"; echo ""; }
}

perform_reconnection() {
    show_reconnecting
    touch /tmp/reconnecting

    $PF_ENABLED && {
        pkill -f "port_forwarding.sh" 2>/dev/null || true
        pkill -f "port_monitor.sh" 2>/dev/null || true
    }
    $PROXY_ENABLED_FLAG && stop_proxies

    # Teardown VPN (removes from killswitch first, then tears down interface)
    teardown_wireguard

    if initial_connect "true"; then
        [ -n "$RESTART_SERVICES" ] && { restart_services "$RESTART_SERVICES"; echo ""; }

        # Start proxies after VPN is up
        $PROXY_ENABLED_FLAG && start_proxies >/dev/null 2>&1

        if $PF_ENABLED; then
            /app/scripts/port_forwarding.sh &

            # Wait for port forwarding to complete (max 30s)
            local waited=0
            while [ $waited -lt 30 ]; do
                [ -f /tmp/port_forwarding_complete ] && { rm -f /tmp/port_forwarding_complete; break; }
                sleep 1
                waited=$((waited + 1))
            done
        else
            show_vpn_connected
        fi

        show_step "Health monitor still running..."
        show_success "Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"

        # Restart port monitor if both PF and API are enabled (now after health status)
        $PF_ENABLED && [ "$PORT_API_ENABLED" = "true" ] && {
            echo ""
            show_step "Restarting port monitor..."
            /app/scripts/port_monitor.sh &
        }

        return 0
    else
        show_error "Reconnection failed"
        return 1
    fi
}

main_loop() {
    # Start proxies first if enabled
    $PROXY_ENABLED_FLAG && start_proxies

    if $PF_ENABLED; then
        /app/scripts/port_forwarding.sh &

        # Wait for port forwarding to complete (max 30s)
        local waited=0
        while [ $waited -lt 30 ]; do
            [ -f /tmp/port_forwarding_complete ] && { rm -f /tmp/port_forwarding_complete; break; }
            sleep 1
            waited=$((waited + 1))
        done
    else
        show_vpn_connected
    fi

    # IMPROVED: Add stabilization delay before starting monitor
    # This gives the VPN time to establish handshakes and settle
    sleep 1

    show_step "Starting health monitor..."
    /usr/local/bin/monitor &
    show_success "Health monitor active (PID: $!)"
    show_success "Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"

    # Show active detection modes
    [ "$MONITOR_WATCH_HANDSHAKE" = "true" ] && show_success "Handshake monitoring: enabled (timeout: ${HANDSHAKE_TIMEOUT}s)"

    if [ "$METRICS" = "true" ]; then
        echo "  ${grn}✓${nc} Metrics available on port ${METRICS_PORT:-9090}"
        echo "      Prometheus:  http://<container-ip>:${METRICS_PORT:-9090}/metrics?format=prometheus"
        echo "      JSON:        http://<container-ip>:${METRICS_PORT:-9090}/metrics"
        echo "      Health:      http://<container-ip>:${METRICS_PORT:-9090}/health"
    fi

    # Start port monitor if PF and API updater are enabled
    $PF_ENABLED && [ "$PORT_API_ENABLED" = "true" ] && /app/scripts/port_monitor.sh &

    while true; do
        if [ -f /tmp/vpn_reconnect_requested ]; then
            rm -f /tmp/vpn_reconnect_requested
            perform_reconnection || {
                show_warning "Retrying..."
                sleep 5
                touch /tmp/vpn_reconnect_requested
            }
        fi
        sleep 5
    done
}

initial_connect || { show_error "Initial connection failed"; exit 1; }
main_loop

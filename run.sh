#!/bin/bash

set -e

source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/vpn.sh
source /app/scripts/verify_connection.sh
source /app/scripts/proxy_go.sh

# OPTIMIZED: Set defaults inline, export only what's needed by child processes
PORT_FORWARDING=${PORT_FORWARDING:-false}
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
export PORT_FORWARDING
export DISABLE_IPV6 DNS LOCAL_NETWORK KILLSWITCH_EXEMPT_PORTS
export CHECK_INTERVAL MAX_FAILURES HANDSHAKE_TIMEOUT
export PROXY_ENABLED SOCKS5_PORT HTTP_PROXY_PORT
export PORT_API_ENABLED PORT_API_TYPE PORT_API_URL PORT_API_USER PORT_API_PASS PORT_API_CMD
export RESTART_SERVICES METRICS METRICS_PORT
export MONITOR_PARALLEL_CHECKS
export WEBHOOK_URL

# Boolean flags (set once, check many times)
PF_ENABLED=false
[ "$PORT_FORWARDING" = "true" ] && PF_ENABLED=true

PROXY_ENABLED_FLAG=false
[ "$PROXY_ENABLED" = "true" ] && PROXY_ENABLED_FLAG=true

# Debug: Show environment configuration
show_debug "Environment configuration:"
show_debug "  PORT_FORWARDING=$PORT_FORWARDING"
show_debug "  DISABLE_IPV6=$DISABLE_IPV6"
show_debug "  DNS=$DNS"
show_debug "  LOCAL_NETWORK=$LOCAL_NETWORK"
show_debug "  KILLSWITCH_EXEMPT_PORTS=$KILLSWITCH_EXEMPT_PORTS"
show_debug "  CHECK_INTERVAL=$CHECK_INTERVAL"
show_debug "  MAX_FAILURES=$MAX_FAILURES"
show_debug "  HANDSHAKE_TIMEOUT=$HANDSHAKE_TIMEOUT"
show_debug "  PROXY_ENABLED=$PROXY_ENABLED"
show_debug "  PORT_API_ENABLED=$PORT_API_ENABLED"
show_debug "  RESTART_SERVICES=$RESTART_SERVICES"
show_debug "  METRICS=$METRICS"
show_debug "  LOG_LEVEL=${LOG_LEVEL} (numeric: $_LOG_LEVEL)"

cleanup() {
    echo ""
    show_step "Shutting down..."
    
    show_debug "Cleanup: Stopping proxies (enabled=$PROXY_ENABLED_FLAG)"
    $PROXY_ENABLED_FLAG && stop_proxies
    
    show_debug "Cleanup: Killing monitor processes"
    pkill -f "monitor" 2>/dev/null || true
    
    show_debug "Cleanup: Killing port forwarding processes"
    pkill -f "port_forwarding.sh" 2>/dev/null || true
    pkill -f "port_monitor.sh" 2>/dev/null || true
    
    show_debug "Cleanup: Tearing down WireGuard"
    teardown_wireguard
    
    show_success "Cleanup complete"
    exit 0
}

trap cleanup SIGTERM SIGINT SIGQUIT

initial_connect() {
    local restart="${1:-false}"
    
    show_debug "initial_connect called with restart=$restart"

    [ ! -f /tmp/reconnecting ] && print_banner

    # Only capture real IP if not already captured
    if [ ! -f /tmp/real_ip ]; then
        capture_real_ip
        echo ""
    else
        show_debug "Real IP already captured: $(cat /tmp/real_ip 2>/dev/null || echo 'unknown')"
    fi

    show_debug "Clearing /etc/resolv.conf"
    echo > /etc/resolv.conf

    # Setup baseline killswitch first (includes bypass route allowances)
    if [ "$restart" != "true" ]; then
        setup_baseline_killswitch
        echo ""

        # Debug: Show killswitch rules
        if [ $_LOG_LEVEL -ge 2 ]; then
            echo ""
            show_debug "Baseline killswitch rules:"
            if command -v nft >/dev/null 2>&1; then
                nft list chain inet vpn_filter output 2>/dev/null | head -20 | while read -r line; do
                    show_debug "  $line"
                done
            else
                iptables -L VPN_OUT -n -v 2>/dev/null | head -20 | while read -r line; do
                    show_debug "  $line"
                done
            fi
            echo ""
        fi
    else
        show_debug "Skipping baseline killswitch setup (restart=true)"
    fi

    # VPN setup (uses surgical exemptions internally)
    show_debug "Calling vpn.sh with restart=$restart"
    /app/scripts/vpn.sh "$restart" || {
        show_debug "vpn.sh failed with exit code $?"
        return 1
    }

    # Save server latency for metrics
    if [ -f /tmp/server_latency_temp ]; then
        show_debug "Saving server latency: $(cat /tmp/server_latency_temp)"
        mv /tmp/server_latency_temp /tmp/server_latency
    fi

    show_step "Establishing VPN connection..."
    if bring_up_wireguard /etc/wireguard/pia.conf; then
        show_success "VPN tunnel established"
        show_debug "WireGuard interface brought up successfully"
    else
        show_error "Failed to bring up tunnel"
        show_debug "bring_up_wireguard returned error code $?"
        return 1
    fi

    # Add VPN to killswitch (now it's safe to route through VPN)
    if [ "$restart" != "true" ]; then
        show_debug "Adding VPN to killswitch (first connection)"
        add_vpn_to_killswitch
        show_ruleset_stats
    else
        show_debug "Adding VPN to killswitch (reconnection, suppressing output)"
        add_vpn_to_killswitch >/dev/null 2>&1
    fi

    echo ""
    show_step "Verifying connection..."
    if verify_connection; then
        show_debug "Connection verification passed"
    else
        show_warning "Connection verification found issues"
        show_debug "verify_connection returned non-zero exit code"
        echo ""
    fi
}

perform_reconnection() {
    show_debug "Reconnection requested"
    show_reconnecting
    touch /tmp/reconnecting

    if $PF_ENABLED; then
        show_debug "Stopping port forwarding processes"
        pkill -f "port_forwarding.sh" 2>/dev/null || true
        pkill -f "port_monitor.sh" 2>/dev/null || true
    fi
    
    if $PROXY_ENABLED_FLAG; then
        show_debug "Stopping proxies"
        stop_proxies
    fi

    # Teardown VPN (removes from killswitch first, then tears down interface)
    show_debug "Tearing down WireGuard tunnel"
    teardown_wireguard

    if initial_connect "true"; then
        show_debug "Reconnection: VPN established successfully"
        
        # Start proxies after VPN is up
        if $PROXY_ENABLED_FLAG; then
            show_debug "Restarting proxies"
            start_proxies >/dev/null 2>&1
        fi

        if $PF_ENABLED; then
            show_debug "Starting port forwarding script"
            /app/scripts/port_forwarding.sh &

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
        show_success "Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"

        # Restart port monitor if both PF and API are enabled (now after health status)
        if $PF_ENABLED && [ "$PORT_API_ENABLED" = "true" ]; then
            echo ""
            show_step "Restarting port monitor..."
            show_debug "Launching port_monitor.sh"
            /app/scripts/port_monitor.sh &
        fi
        
        sleep 2
        
        if [ -n "$RESTART_SERVICES" ]; then
            echo ""
            show_debug "Restarting dependent services: $RESTART_SERVICES"
            restart_services "$RESTART_SERVICES"
            echo ""
        fi
        
        show_debug "Removing reconnecting flag"
        rm -f /tmp/reconnecting
        return 0
    else
        show_error "Reconnection failed"
        show_debug "initial_connect returned error during reconnection"
        return 1
    fi
}

main_loop() {
    show_debug "Entering main loop"
    
    # Start proxies first if enabled
    if $PROXY_ENABLED_FLAG; then
        show_debug "Starting proxies"
        start_proxies
    fi

    if $PF_ENABLED; then
        show_debug "Starting port forwarding script"
        /app/scripts/port_forwarding.sh &

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

    # Stabilization delay before starting monitor
    show_debug "Waiting 1s for stabilization before starting monitor"
    sleep 1

    show_step "Starting health monitor..."
    /usr/local/bin/monitor &
    local monitor_pid=$!
    show_success "Health monitor active (PID: $monitor_pid)"
    show_success "Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"
    show_debug "Monitor PID: $monitor_pid"

    if [ "$METRICS" = "true" ]; then
        echo "  ${grn}✓${nc} Metrics available on port ${METRICS_PORT:-9090}"
        echo "      Prometheus:  http://<container-ip>:${METRICS_PORT:-9090}/metrics?format=prometheus"
        echo "      JSON:        http://<container-ip>:${METRICS_PORT:-9090}/metrics"
        echo "      Health:      http://<container-ip>:${METRICS_PORT:-9090}/health"
        show_debug "Metrics endpoint configured on port ${METRICS_PORT:-9090}"
    fi

    # Start port monitor if PF and API updater are enabled
    if $PF_ENABLED && [ "$PORT_API_ENABLED" = "true" ]; then
        show_debug "Starting port monitor (PF + API enabled)"
        /app/scripts/port_monitor.sh &
    fi

    sleep 2
    
    if [ -n "$RESTART_SERVICES" ]; then
        echo ""
        show_debug "Restarting dependent services: $RESTART_SERVICES"
        restart_services "$RESTART_SERVICES"
        echo ""
    fi

    show_debug "Entering reconnection monitor loop (check every 5s)"
    local loop_count=0
    while true; do
        if [ -f /tmp/vpn_reconnect_requested ]; then
            show_debug "Reconnection flag detected (loop iteration: $loop_count)"
            rm -f /tmp/vpn_reconnect_requested
            
            if perform_reconnection; then
                show_debug "Reconnection successful"
            else
                show_warning "Retrying..."
                show_debug "Reconnection failed, waiting 5s before retry"
                sleep 5
                touch /tmp/vpn_reconnect_requested
            fi
        fi
        sleep 5
        loop_count=$((loop_count + 1))
        [ $_LOG_LEVEL -ge 2 ] && [ $((loop_count % 12)) -eq 0 ] && show_debug "Main loop heartbeat (iteration: $loop_count, uptime: $((loop_count * 5))s)"
    done
}

show_debug "Starting initial connection"
if initial_connect; then
    show_debug "Initial connection successful, entering main loop"
    main_loop
else
    show_error "Initial connection failed"
    show_debug "Exiting with error code 1"
    exit 1
fi
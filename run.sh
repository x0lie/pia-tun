#!/bin/bash

set -e

source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/wireguard.sh
source /app/scripts/verify_connection.sh
source /app/scripts/proxy_go.sh

# Bulk export
export DISABLE_IPV6=${DISABLE_IPV6:-true} \
       DNS=${DNS:-pia} \
       LOCAL_NETWORK=${LOCAL_NETWORK:-""} \
       QUIET_MODE=true \
       KILLSWITCH_EXEMPT_PORTS=${KILLSWITCH_EXEMPT_PORTS:-""} \
       HANDSHAKE_TIMEOUT=${HANDSHAKE_TIMEOUT:-180} \
       CHECK_INTERVAL=${CHECK_INTERVAL:-15} \
       MAX_FAILURES=${MAX_FAILURES:-2} \
       RESTART_SERVICES=${RESTART_SERVICES:-""} \
       PROXY_ENABLED=${PROXY_ENABLED:-false} \
       SOCKS5_PORT=${SOCKS5_PORT:-1080} \
       HTTP_PROXY_PORT=${HTTP_PROXY_PORT:-8888}

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
    teardown_wireguard
    cleanup_killswitch
    show_success "Cleanup complete"
    exit 0
}

trap cleanup SIGTERM SIGINT SIGQUIT

initial_connect() {
    [ ! -f /tmp/reconnecting ] && print_banner
    [ ! -f /tmp/real_ip ] && { capture_real_ip; echo ""; }
    
    setup_pre_tunnel_killswitch
    
    # Debug info
    if $DEBUG_MODE; then
        echo ""
        echo "  ${blu}[DEBUG]${nc} Pre-tunnel firewall rules:"
        iptables -L VPN_OUT -n -v | head -20 | sed 's/^/    /'
        echo ""
        echo "  ${blu}[DEBUG]${nc} Testing connectivity:"
        printf "    DNS: "; nslookup www.privateinternetaccess.com >/dev/null 2>&1 && echo "${grn}OK${nc}" || echo "${red}FAIL${nc}"
        printf "    HTTPS: "; curl -s --max-time 5 https://www.privateinternetaccess.com >/dev/null 2>&1 && echo "${grn}OK${nc}" || echo "${red}FAIL${nc}"
    fi
    echo ""
    
    /app/scripts/setup_vpn.sh || return 1
    
    # Save server latency for metrics
    if [ -f /tmp/server_latency_temp ]; then
        mv /tmp/server_latency_temp /tmp/server_latency
    fi
    
    echo ""
    
    show_step "Establishing VPN connection..."
    bring_up_wireguard /etc/wireguard/pia.conf && show_success "VPN tunnel established" || \
        { show_error "Failed to bring up tunnel"; return 1; }
    sleep 3
    echo ""
    
    finalize_killswitch
    echo ""
    
    show_step "Verifying connection..."
    verify_connection && echo "" || { show_warning "Connection verification found issues"; echo ""; }
}

perform_reconnection() {
    show_reconnecting
    touch /tmp/reconnecting
    
    $PF_ENABLED && pkill -f "port_forwarding.sh" 2>/dev/null || true
    $PROXY_ENABLED_FLAG && stop_proxies
    
    show_step "Tearing down existing tunnel..."
    teardown_wireguard
    show_success "Tunnel torn down"
    echo ""
    
    if initial_connect; then
        [ -n "$RESTART_SERVICES" ] && { restart_services "$RESTART_SERVICES"; echo ""; }
        
        # Start proxies after VPN is up
        if $PROXY_ENABLED_FLAG; then
            start_proxies
        fi
        
        if $PF_ENABLED; then
            show_step "Restarting port forwarding..."
            /app/scripts/port_forwarding.sh &
            # Inline wait loop (no function overhead)
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
        echo ""
        return 0
    else
        show_error "Reconnection failed"
        return 1
    fi
}

main_loop() {
    # Start proxies first if enabled
    if $PROXY_ENABLED_FLAG; then
        start_proxies
    fi
    
    if $PF_ENABLED; then
        show_step "Initializing port forwarding..."
        /app/scripts/port_forwarding.sh &
        # Inline wait
        local waited=0
        while [ $waited -lt 30 ]; do
            [ -f /tmp/port_forwarding_complete ] && { rm -f /tmp/port_forwarding_complete; break; }
            sleep 1
            waited=$((waited + 1))
        done
    else
        show_vpn_connected
    fi
    
    show_step "Starting health monitor..."
    sleep 5
    /usr/local/bin/monitor &
    show_success "Health monitor active (PID: $!)"
    show_success "Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"
    
    # Show active detection modes
    [ "$MONITOR_FAST_FAIL" = "true" ] && show_success "Fast-fail mode: enabled"
    [ "$MONITOR_PARALLEL_CHECKS" = "true" ] && show_success "Parallel checks: enabled"
    [ "$MONITOR_WATCH_HANDSHAKE" = "true" ] && show_success "Handshake monitoring: enabled (timeout: ${HANDSHAKE_TIMEOUT}s)"
    
    if [ "$METRICS" = "true" ]; then
        echo "  ${grn}✓${nc} Metrics available on port ${METRICS_PORT:-9090}"
        echo "      JSON:        http://localhost:${METRICS_PORT:-9090}/metrics"
        echo "      Prometheus:  http://localhost:${METRICS_PORT:-9090}/metrics?format=prometheus"
        echo "      Health:      http://localhost:${METRICS_PORT:-9090}/health"
    fi
    
    echo ""
    
    while true; do
        [ -f /tmp/vpn_reconnect_requested ] && {
            rm -f /tmp/vpn_reconnect_requested
            perform_reconnection || { show_warning "Retrying..."; sleep 5; touch /tmp/vpn_reconnect_requested; }
        }
        sleep 5
    done
}

initial_connect || { show_error "Initial connection failed"; exit 1; }
main_loop

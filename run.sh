#!/bin/bash

set -e

source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/wireguard.sh
source /app/scripts/verify_connection.sh

# Export all environment variables at once
export DISABLE_IPV6=${DISABLE_IPV6:-true} \
       DNS=${DNS:-pia} \
       LOCAL_NETWORK=${LOCAL_NETWORK:-""} \
       QUIET_MODE=true \
       KILLSWITCH_EXEMPT_PORTS=${KILLSWITCH_EXEMPT_PORTS:-""} \
       HANDSHAKE_TIMEOUT=${HANDSHAKE_TIMEOUT:-180} \
       CHECK_INTERVAL=${CHECK_INTERVAL:-30} \
       MAX_FAILURES=${MAX_FAILURES:-3} \
       RESTART_SERVICES=${RESTART_SERVICES:-""}

# Cleanup handler
cleanup() {
    echo ""
    show_step "Shutting down..."
    pkill -f "monitor.sh" 2>/dev/null || true
    pkill -f "port_forwarding.sh" 2>/dev/null || true
    teardown_wireguard
    cleanup_killswitch
    show_success "Cleanup complete"
    exit 0
}

trap cleanup SIGTERM SIGINT SIGQUIT

# Wait for port forwarding completion
wait_for_pf() {
    for i in {1..30}; do
        [ -f /tmp/port_forwarding_complete ] && { rm -f /tmp/port_forwarding_complete; return 0; }
        sleep 1
    done
}

# Show debug info if enabled
show_debug_info() {
    [ "${MONITOR_DEBUG}" != "true" ] && return
    
    echo ""
    echo "  ${blu}[DEBUG]${nc} Pre-tunnel firewall rules:"
    iptables -L VPN_OUT -n -v | head -20 | sed 's/^/    /'
    echo ""
    echo "  ${blu}[DEBUG]${nc} Testing connectivity:"
    printf "    DNS: "; nslookup www.privateinternetaccess.com >/dev/null 2>&1 && echo "${grn}OK${nc}" || echo "${red}FAIL${nc}"
    printf "    HTTPS: "; curl -s --max-time 5 https://www.privateinternetaccess.com >/dev/null 2>&1 && echo "${grn}OK${nc}" || echo "${red}FAIL${nc}"
}

# Initial VPN connection
initial_connect() {
    [ ! -f /tmp/reconnecting ] && print_banner
    
    # Capture real IP only once
    [ ! -f /tmp/real_ip ] && { capture_real_ip; echo ""; }
    
    # Setup firewall
    setup_pre_tunnel_killswitch
    show_debug_info
    echo ""
    
    # Setup VPN (merged script)
    /app/scripts/setup_vpn.sh || return 1
    echo ""
    
    # Bring up tunnel
    show_step "Establishing VPN connection..."
    bring_up_wireguard /etc/wireguard/pia.conf && show_success "VPN tunnel established" || { show_error "Failed to bring up tunnel"; return 1; }
    sleep 3
    echo ""
    
    # Finalize
    finalize_killswitch
    echo ""
    
    show_step "Verifying connection..."
    verify_connection && echo "" || { show_warning "Connection verification found issues"; echo ""; }
    
    return 0
}

# Reconnection handler
perform_reconnection() {
    show_reconnecting
    touch /tmp/reconnecting
    
    [ "${PORT_FORWARDING}" = "true" ] && pkill -f "port_forwarding.sh" 2>/dev/null || true
    
    show_step "Tearing down existing tunnel..."
    teardown_wireguard
    show_success "Tunnel torn down"
    echo ""
    
    if initial_connect; then
        [ -n "$RESTART_SERVICES" ] && { restart_services "$RESTART_SERVICES"; echo ""; }
        
        if [ "${PORT_FORWARDING}" = "true" ]; then
            show_step "Restarting port forwarding..."
            /app/scripts/port_forwarding.sh &
            wait_for_pf
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

# Main monitoring loop
main_loop() {
    if [ "${PORT_FORWARDING}" = "true" ]; then
        show_step "Initializing port forwarding..."
        /app/scripts/port_forwarding.sh &
        wait_for_pf
    else
        show_vpn_connected
    fi
    
    show_step "Starting health monitor..."
    sleep 5
    /app/scripts/monitor.sh &
    show_success "Health monitor active (PID: $!)"
    show_success "Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"
    echo ""
    
    while true; do
        if [ -f /tmp/vpn_reconnect_requested ]; then
            rm -f /tmp/vpn_reconnect_requested
            perform_reconnection || { show_warning "Retrying..."; sleep 5; touch /tmp/vpn_reconnect_requested; }
        fi
        sleep 5
    done
}

# Main execution
initial_connect || { show_error "Initial connection failed"; exit 1; }
main_loop

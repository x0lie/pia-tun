#!/bin/bash

set -e

# Source helper scripts
source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/wireguard.sh
source /app/scripts/verify_connection.sh

# Export environment variables
export_vars() {
    export DISABLE_IPV6=${DISABLE_IPV6:-true}
    export DNS=${DNS:-pia}
    export LOCAL_NETWORK=${LOCAL_NETWORK:-""}
    export PIA_USER PIA_PASS PIA_LOCATION PORT_FORWARDING MTU
    export QUIET_MODE=true
    export KILLSWITCH_EXEMPT_PORTS=${KILLSWITCH_EXEMPT_PORTS:-""}
    export HANDSHAKE_TIMEOUT=${HANDSHAKE_TIMEOUT:-180}
    export CHECK_INTERVAL=${CHECK_INTERVAL:-30}
    export MAX_FAILURES=${MAX_FAILURES:-3}
    export RESTART_SERVICES=${RESTART_SERVICES:-""}
}

# Cleanup function
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

# Wait for port forwarding to complete (up to 30 seconds)
wait_for_port_forwarding() {
    local max_wait=30
    local waited=0
    
    while [ $waited -lt $max_wait ]; do
        if [ -f /tmp/port_forwarding_complete ]; then
            rm -f /tmp/port_forwarding_complete
            return 0
        fi
        sleep 1
        waited=$((waited + 1))
    done
    
    return 0
}

# Initial connection function
initial_connect() {
    # Only print banner on first connect
    [ ! -f /tmp/reconnecting ] && print_banner

    export_vars

    # Only capture real IP on first connect
    if [ ! -f /tmp/real_ip ]; then
        capture_real_ip
        echo ""
    fi

    # Apply pre-tunnel killswitch
    setup_pre_tunnel_killswitch

    # Debug output if enabled
    if [ "${MONITOR_DEBUG}" = "true" ]; then
        echo ""
        echo "  ${blu}[DEBUG]${nc} Pre-tunnel firewall rules:"
        iptables -L VPN_OUT -n -v | head -20 | sed 's/^/    /'
        echo ""
        echo "  ${blu}[DEBUG]${nc} Testing connectivity:"
        echo -n "    DNS resolution: "
        nslookup www.privateinternetaccess.com >/dev/null 2>&1 && echo "${grn}OK${nc}" || echo "${red}FAILED${nc}"
        echo -n "    HTTPS to PIA: "
        curl -s --max-time 5 https://www.privateinternetaccess.com >/dev/null 2>&1 && echo "${grn}OK${nc}" || echo "${red}FAILED${nc}"
    fi
    echo ""

    # Authenticate
    show_step "Authenticating with PIA..."
    if /app/scripts/get_token.sh > /dev/null 2>&1; then
        show_success "Authentication successful"
    else
        show_error "Authentication failed"
        return 1
    fi
    echo ""

    # Get server info
    show_step "Finding optimal server for ${bold}${PIA_LOCATION}${nc}..."
    if SERVER_OUTPUT=$(/app/scripts/get_server_info.sh 2>&1); then
        SERVER_NAME=$(echo "$SERVER_OUTPUT" | grep "Server selected:" | cut -d: -f2- | xargs)
        [ -n "$SERVER_NAME" ] && show_success "Connected to: ${bold}${SERVER_NAME}${nc}" || show_warning "Server selected (no latency data)"
    else
        echo "$SERVER_OUTPUT"
        return 1
    fi
    echo ""

    # Generate config
    show_step "Configuring WireGuard tunnel..."
    if /app/scripts/generate_config.sh >/tmp/wg_config_output.log 2>&1; then
        show_success "Tunnel configured"
    else
        show_error "Configuration failed"
        cat /tmp/wg_config_output.log
        return 1
    fi
    echo ""

    # Bring up tunnel
    show_step "Establishing VPN connection..."
    if bring_up_wireguard /etc/wireguard/pia.conf; then
        show_success "VPN tunnel established"
    else
        show_error "Failed to bring up tunnel"
        return 1
    fi

    sleep 3
    echo ""

    # Finalize killswitch
    finalize_killswitch
    echo ""

    # Verify connection
    show_step "Verifying connection..."
    verify_connection && echo "" || { show_warning "Connection verification found potential issues"; echo ""; }

    return 0
}

# Perform reconnection
perform_reconnection() {
    show_reconnecting
    touch /tmp/reconnecting

    # Stop port forwarding if running
    [ "${PORT_FORWARDING}" = "true" ] && pkill -f "port_forwarding.sh" 2>/dev/null || true

    # Tear down tunnel
    show_step "Tearing down existing tunnel..."
    teardown_wireguard
    show_success "Tunnel torn down"
    echo ""

    # Reconnect
    if initial_connect; then
        # Restart services if configured
        [ -n "$RESTART_SERVICES" ] && restart_services "$RESTART_SERVICES" && echo ""

        # Restart port forwarding if enabled
        if [ "${PORT_FORWARDING}" = "true" ]; then
            show_step "Restarting port forwarding..."
            /app/scripts/port_forwarding.sh &
            wait_for_port_forwarding
        else
            show_vpn_connected
        fi

        # Announce monitor status
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
    # Start port forwarding if enabled
    if [ "${PORT_FORWARDING}" = "true" ]; then
        show_step "Initializing port forwarding..."
        /app/scripts/port_forwarding.sh &
        wait_for_port_forwarding
    else
        show_vpn_connected
    fi

    # Start health monitor
    show_step "Starting health monitor..."
    sleep 5  # Grace period
    /app/scripts/monitor.sh &
    MONITOR_PID=$!
    show_success "Health monitor active (PID: $MONITOR_PID)"
    show_success "Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"
    echo ""

    # Main loop - check for reconnect requests
    while true; do
        if [ -f /tmp/vpn_reconnect_requested ]; then
            rm -f /tmp/vpn_reconnect_requested

            if ! perform_reconnection; then
                show_warning "Retrying reconnection..."
                sleep 5
                touch /tmp/vpn_reconnect_requested
            fi
        fi

        sleep 5
    done
}

# Main execution
main() {
    initial_connect || { show_error "Initial connection failed"; exit 1; }
    main_loop
}

main "$@"

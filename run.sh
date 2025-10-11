#!/bin/bash

# Exit on error
set -e

# Source helper scripts
source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/wireguard.sh
source /app/scripts/verify_connection.sh

# Helper to export vars to child scripts
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

    # Stop monitor if running
    pkill -f "monitor.sh" 2>/dev/null || true

    # Stop port forwarding if running
    pkill -f "port_forwarding.sh" 2>/dev/null || true

    # Bring down tunnel
    teardown_wireguard

    # Clean up iptables
    cleanup_killswitch

    show_success "Cleanup complete"
    exit 0
}

# Trap signals for graceful shutdown
trap cleanup SIGTERM SIGINT SIGQUIT

# Initial connection function
initial_connect() {
    # Only print banner on first connect, not reconnects
    if [ ! -f /tmp/reconnecting ]; then
        print_banner
    fi

    export_vars

    # Only capture real IP on first connect
    if [ ! -f /tmp/real_ip ]; then
        capture_real_ip
        echo ""
    fi

    # Apply pre-tunnel killswitch first
    setup_pre_tunnel_killswitch

    # Debug: Show what's actually allowed during pre-tunnel phase
    if [ "${MONITOR_DEBUG}" = "true" ]; then
        echo ""
        echo "  ${blu}[DEBUG]${nc} Pre-tunnel firewall rules:"
        iptables -L VPN_OUT -n -v | head -20 | sed 's/^/    /'
        echo ""
        echo "  ${blu}[DEBUG]${nc} Testing connectivity:"
        echo -n "    DNS resolution: "
        if nslookup www.privateinternetaccess.com >/dev/null 2>&1; then
            echo "${grn}OK${nc}"
        else
            echo "${red}FAILED${nc}"
        fi
        echo -n "    HTTPS to PIA: "
        if curl -s --max-time 5 https://www.privateinternetaccess.com >/dev/null 2>&1; then
            echo "${grn}OK${nc}"
        else
            echo "${red}FAILED${nc}"
        fi
    fi
    echo ""

    # Authenticate and get token
    show_step "Authenticating with PIA..."
    if /app/scripts/get_token.sh > /dev/null 2>&1; then
        show_success "Authentication successful"
    else
        show_error "Authentication failed"
        return 1
    fi
    echo ""

    # Get Server info
    show_step "Finding optimal server for ${bold}${PIA_LOCATION}${nc}..."
    if SERVER_OUTPUT=$(/app/scripts/get_server_info.sh 2>&1); then
        SERVER_NAME=$(echo "$SERVER_OUTPUT" | grep "Server selected:" | cut -d: -f2- | xargs)
        if [ -n "$SERVER_NAME" ]; then
            show_success "Connected to: ${bold}${SERVER_NAME}${nc}"
        else
            show_warning "Server selected (no latency data)"
        fi
    else
        echo "$SERVER_OUTPUT"
        return 1
    fi
    echo ""

    # Generate WireGuard config
    show_step "Configuring WireGuard tunnel..."
    if /app/scripts/generate_config.sh >/tmp/wg_config_output.log 2>&1; then
        show_success "Tunnel configured"
    else
        show_error "Configuration failed"
        cat /tmp/wg_config_output.log
        return 1
    fi
    echo ""

    # Bring up the tunnel manually
    show_step "Establishing VPN connection..."
    if bring_up_wireguard /etc/wireguard/pia.conf; then
        show_success "VPN tunnel established"
    else
        show_error "Failed to bring up tunnel"
        return 1
    fi

    # Wait for tunnel to stabilize
    sleep 3
    echo ""

    # Finalize killswitch with fwmark
    finalize_killswitch
    echo ""

    # Verify connectivity
    show_step "Verifying connection..."
    if verify_connection; then
        echo ""
    else
        show_warning "Connection verification found potential issues"
        echo ""
    fi

    return 0
}

# Perform reconnection by calling initial_connect
perform_reconnection() {
    echo ""
    echo "${ylw}╔════════════════════════════════════════════════╗${nc}"
    echo "${ylw}║${nc}               ${ylw}↻${nc} ${bold}Reconnecting VPN${nc}               ${ylw}║${nc}"
    echo "${ylw}╚════════════════════════════════════════════════╝${nc}"
    echo ""

    # Mark that we're reconnecting
    touch /tmp/reconnecting

    # Stop port forwarding if running
    if [ "${PORT_FORWARDING}" = "true" ]; then
        pkill -f "port_forwarding.sh" 2>/dev/null || true
    fi

    # Tear down existing tunnel
    show_step "Tearing down existing tunnel..."
    teardown_wireguard
    show_success "Tunnel torn down"
    echo ""

    # Call the same initial_connect function
    if initial_connect; then
        # Restart dependent services if configured
        if [ -n "$RESTART_SERVICES" ]; then
            show_step "Restarting dependent services..."

            IFS=',' read -ra SERVICES <<< "$RESTART_SERVICES"
            for service in "${SERVICES[@]}"; do
                service=$(echo "$service" | xargs)
                if [ -n "$service" ]; then
                    echo "  ${blu}↻${nc} Restarting: $service"
                    docker restart "$service" 2>/dev/null || echo "  ${ylw}⚠${nc} Could not restart $service"
                fi
            done

            show_success "Services restarted"
            echo ""
        fi

        # Restart port forwarding if enabled
        if [ "${PORT_FORWARDING}" = "true" ]; then
            show_step "Restarting port forwarding..."
            /app/scripts/port_forwarding.sh &
            
            # Wait for port forwarding to complete and show VPN Connected box
            # (port_forwarding.sh will display the box when it gets the port)
            wait_for_port_forwarding
        else
            # Show VPN Connected box when not using port forwarding
            echo "${grn}╔════════════════════════════════════════════════╗${nc}"
            echo "${grn}║${nc}                ${grn}✓${nc} ${bold}VPN Connected${nc}                 ${grn}║${nc}"
            echo "${grn}╚════════════════════════════════════════════════╝${nc}"
            echo ""
        fi

        # Announce health monitor is still running
        show_step "Health monitor still running..."
        echo "  ${grn}✓${nc} Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"
        echo ""

        return 0
    else
        show_error "Reconnection failed"
        return 1
    fi
}

# Wait for port forwarding to complete (up to 30 seconds)
wait_for_port_forwarding() {
    local max_wait=30
    local waited=0
    
    # Wait for port forwarding to create the completion flag
    while [ $waited -lt $max_wait ]; do
        if [ -f /tmp/port_forwarding_complete ]; then
            rm -f /tmp/port_forwarding_complete
            return 0
        fi
        sleep 1
        waited=$((waited + 1))
    done
    
    # Timeout - just continue anyway
    return 0
}

# Main monitoring loop
main_loop() {
    # Start port forwarding if enabled
    if [ "${PORT_FORWARDING}" = "true" ]; then
        show_step "Initializing port forwarding..."
        /app/scripts/port_forwarding.sh &
        PF_PID=$!
        
        # Wait for port forwarding to complete and show VPN Connected box
        wait_for_port_forwarding
    else
        echo "${grn}╔════════════════════════════════════════════════╗${nc}"
        echo "${grn}║${nc}                ${grn}✓${nc} ${bold}VPN Connected${nc}                 ${grn}║${nc}"
        echo "${grn}╚════════════════════════════════════════════════╝${nc}"
        echo ""
    fi

    # Start health monitor in background AFTER port forwarding completes
    show_step "Starting health monitor..."

    # Give VPN a grace period to stabilize before monitoring
    # This prevents false positives right after connection
    sleep 5

    /app/scripts/monitor.sh &
    MONITOR_PID=$!
    show_success "Health monitor active (PID: $MONITOR_PID)"
    echo "  ${grn}✓${nc} Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"
    echo ""

    # Main loop - check for reconnect requests
    while true; do
        # Check if reconnect was requested by monitor
        if [ -f /tmp/vpn_reconnect_requested ]; then
            rm -f /tmp/vpn_reconnect_requested

            # Perform reconnection
            if perform_reconnection; then
                # Success - continue monitoring
                :
            else
                # Failed - request another reconnect attempt
                show_warning "Retrying reconnection..."
                sleep 5
                touch /tmp/vpn_reconnect_requested
            fi
        fi

        # Check every 5 seconds for reconnect requests
        sleep 5
    done
}

# Main flow
main() {
    # Perform initial connection
    if ! initial_connect; then
        show_error "Initial connection failed"
        exit 1
    fi

    # Enter main monitoring loop
    main_loop
}

# Run main
main "$@"

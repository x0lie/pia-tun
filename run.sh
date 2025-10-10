#!/bin/bash

# Exit on error
set -e

# Source helper scripts
source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/wireguard.sh

# Helper to export vars to child scripts
export_vars() {
    export DISABLE_IPV6=${DISABLE_IPV6:-true}
    export PIA_USER PIA_PASS PIA_LOCATION PORT_FORWARDING LOCAL_NETWORK PIA_DNS MTU
    export QUIET_MODE=true
    export KILLSWITCH_EXEMPT_PORTS=${KILLSWITCH_EXEMPT_PORTS:-""}
}

# Cleanup function
cleanup() {
    echo ""
    show_step "Shutting down..."
    
    # Bring down tunnel
    teardown_wireguard
    
    # Clean up iptables
    cleanup_killswitch
    
    show_success "Cleanup complete"
    exit 0
}

# Trap signals for graceful shutdown
trap cleanup SIGTERM SIGINT SIGQUIT

# Main flow
main() {
    print_banner
    export_vars

    # Apply pre-tunnel killswitch first
    setup_pre_tunnel_killswitch
    echo ""

    # Authenticate and get token
    show_step "Authenticating with PIA..."
    if /app/scripts/get_token.sh > /dev/null 2>&1; then
        show_success "Authentication successful"
    else
        show_error "Authentication failed"
        exit 1
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
        exit 1
    fi
    echo ""

    # Generate WireGuard config
    show_step "Configuring WireGuard tunnel..."
    if /app/scripts/generate_config.sh > /tmp/wg_config_output.log 2>&1; then
        show_success "Tunnel configured"
    else
        show_error "Configuration failed"
        cat /tmp/wg_config_output.log
        exit 1
    fi
    echo ""

    # Bring up the tunnel manually
    show_step "Establishing VPN connection..."
    if bring_up_wireguard /etc/wireguard/pia.conf; then
        show_success "VPN tunnel established"
    else
        show_error "Failed to bring up tunnel"
        exit 1
    fi

    # Wait for tunnel to stabilize
    sleep 3
    echo ""

    # Finalize killswitch with fwmark
    finalize_killswitch
    echo ""

    # Verify connectivity
    show_step "Verifying connection..."
    EXTERNAL_IP=$(timeout 10 curl -s --interface pia ifconfig.me 2>/dev/null || echo "")
    if [ -n "$EXTERNAL_IP" ]; then
        show_success "External IP: ${grn}${bold}${EXTERNAL_IP}${nc}"
    else
        show_warning "Could not verify external IP"
    fi
    echo ""

    # Port Forward if enabled
    if [ "${PORT_FORWARDING}" = "true" ]; then
        show_step "Initializing port forwarding..."
        /app/scripts/port_forwarding.sh
    else
        echo "${grn}╔════════════════════════════════════════════════╗${nc}"
        echo "${grn}║${nc}                ${grn}✓${nc} ${bold}VPN Connected${nc}                 ${grn}║${nc}"
        echo "${grn}╚════════════════════════════════════════════════╝${nc}"
        echo ""
        tail -f /dev/null & wait
    fi
}

# Run main
main "$@"

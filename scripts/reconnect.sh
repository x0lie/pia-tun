#!/bin/bash

# VPN Reconnection Handler
# This script performs a full reconnection cycle

set -e

# Source helper scripts
source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/wireguard.sh
source /app/scripts/verify_connection.sh

# Configuration
RESTART_SERVICES=${RESTART_SERVICES:-""}

# Perform reconnection
reconnect_vpn() {
    show_step "Starting VPN reconnection..."
    echo ""
    
    # Verify we still have credentials (they're in environment)
    if [ -z "$PIA_USER" ] || [ -z "$PIA_PASS" ]; then
        show_error "PIA credentials not found in environment"
        return 1
    fi
    
    # Step 1: Tear down existing tunnel
    show_step "Tearing down existing tunnel..."
    teardown_wireguard
    show_success "Tunnel torn down"
    
    # Brief pause to ensure cleanup
    sleep 2
    echo ""
    
    # Step 1.5: IMPORTANT - Re-apply pre-tunnel killswitch rules
    # This allows auth traffic while blocking everything else
    show_step "Adjusting firewall for reconnection..."
    setup_pre_tunnel_killswitch
    show_success "Firewall configured for authentication"
    echo ""
    
    # Step 2: Re-authenticate and get fresh token
    show_step "Re-authenticating with PIA..."
    # Clear old token first
    rm -f /tmp/pia_token
    
    if /app/scripts/get_token.sh > /dev/null 2>&1; then
        show_success "Authentication successful"
    else
        show_error "Authentication failed"
        # Check if it's a credentials issue or network issue
        if ! curl -s --max-time 5 https://www.privateinternetaccess.com >/dev/null 2>&1; then
            show_warning "Cannot reach PIA servers - network may be down"
        else
            show_warning "Check your PIA credentials"
        fi
        return 1
    fi
    echo ""
    
    # Step 3: Get fresh server info
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
    
    # Step 4: Generate new WireGuard config
    show_step "Configuring WireGuard tunnel..."
    if /app/scripts/generate_config.sh >/tmp/wg_config_output.log 2>&1; then
        show_success "Tunnel configured"
    else
        show_error "Configuration failed"
        cat /tmp/wg_config_output.log
        return 1
    fi
    echo ""
    
    # Step 5: Bring up tunnel
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
    
    # Step 6: Finalize killswitch with the new tunnel
    show_step "Finalizing killswitch..."
    finalize_killswitch
    echo ""
    
    # Step 7: Verify connection
    show_step "Verifying connection..."
    if verify_connection; then
        show_success "Connection verified"
    else
        show_warning "Connection verification found potential issues"
    fi
    echo ""
    
    # Step 8: Restart dependent services if configured
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
    
    # Step 9: Restart port forwarding if enabled
    if [ "${PORT_FORWARDING}" = "true" ]; then
        show_step "Restarting port forwarding..."
        
        # Kill existing port forwarding process
        pkill -f "port_forwarding.sh" 2>/dev/null || true
        
        # Start new port forwarding in background
        /app/scripts/port_forwarding.sh &
        
        show_success "Port forwarding restarted"
        echo ""
    fi
    
    show_success "VPN reconnection complete"
    echo ""
    
    return 0
}

# Run if executed directly
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    reconnect_vpn
fi

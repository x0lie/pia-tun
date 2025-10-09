#!/bin/bash

# Define colors
red=$'\033[0;31m'
grn=$'\033[0;32m'
blu=$'\033[0;34m'
cyn=$'\033[0;36m'
ylw=$'\033[0;33m'
nc=$'\033[0m'
bold=$'\033[1m'

# Exit on error
set -e

# Print startup banner
print_banner() {
    clear
    echo "${cyn}╔════════════════════════════════════════════════╗${nc}"
    echo "${cyn}║                                                ║${nc}"
    echo "${cyn}║${nc}    ${bold}PIA WireGuard VPN Container${nc}                 ${cyn}║${nc}"
    echo "${cyn}║${nc}    ${grn}olsonalexw${nc}                                  ${cyn}║${nc}"
    echo "${cyn}║                                                ║${nc}"
    echo "${cyn}╚════════════════════════════════════════════════╝${nc}"
    echo ""
}

# Helper to export vars to child scripts
export_vars() {
    export DISABLE_IPV6=${DISABLE_IPV6:-true}
    export PIA_USER PIA_PASS PIA_LOCATION PORT_FORWARDING LOCAL_NETWORK PIA_DNS MTU
    export QUIET_MODE=true  # Signal to child scripts to minimize output
}

# Progress indicator
show_step() {
    echo "${blu}▶${nc} $1"
}

show_success() {
    echo "  ${grn}✓${nc} $1"
}

show_warning() {
    echo "  ${ylw}⚠${nc} $1"
}

show_error() {
    echo "  ${red}✗${nc} $1"
}

# Main flow
main() {
    print_banner
    export_vars

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
    SERVER_OUTPUT=$(/app/scripts/get_server_info.sh 2>&1)
    SERVER_NAME=$(echo "$SERVER_OUTPUT" | grep "Server selected:" | cut -d: -f2- | xargs)
    if [ -n "$SERVER_NAME" ]; then
        show_success "Connected to: ${bold}${SERVER_NAME}${nc}"
    else
        show_warning "Server selected (no latency data)"
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

    # DNS fallback: Set DNS before bringing up tunnel
    DNS_LINE=$(grep '^DNS =' /etc/wireguard/pia.conf | cut -d= -f2- | tr -d ' ')
    if [ -n "$DNS_LINE" ]; then
        cp /etc/resolv.conf /etc/resolv.conf.bak 2>/dev/null || true
        echo "# Set by PIA WireGuard" > /etc/resolv.conf
        IFS=',' read -ra DNS_SERVERS <<< "$DNS_LINE"
        for server in "${DNS_SERVERS[@]}"; do
            server=$(echo "$server" | xargs)
            echo "nameserver $server" >> /etc/resolv.conf
        done
    fi

    # Bring up the tunnel (suppress verbose output)
    show_step "Establishing VPN connection..."
    if wg-quick up /etc/wireguard/pia.conf > /tmp/wg_up.log 2>&1; then
        show_success "VPN tunnel established"
    else
        show_error "Failed to bring up tunnel"
        cat /tmp/wg_up.log
        exit 1
    fi

    # Wait for tunnel to stabilize
    sleep 2
    echo ""

    # Verify connectivity
    show_step "Verifying connection..."
    EXTERNAL_IP=$(timeout 10 curl -s ifconfig.me 2>/dev/null || echo "")
    if [ -n "$EXTERNAL_IP" ]; then
        show_success "External IP: ${grn}${bold}${EXTERNAL_IP}${nc}"
    else
        show_warning "Could not verify external IP"
    fi
    echo ""

    # Port Forward if enabled
    if [ "${PORT_FORWARDING}" = "true" ]; then
        show_step "Initializing port forwarding..."
        echo ""
        /app/scripts/port_forwarding.sh
    else
        echo "${cyn}╔════════════════════════════════════════════════╗${nc}"
        echo "${cyn}║${nc}  ${grn}✓${nc} ${bold}VPN Connected & Ready${nc}                    ${cyn}║${nc}"
        echo "${cyn}╚════════════════════════════════════════════════╝${nc}"
        echo ""
        tail -f /dev/null
    fi
}

# Run main
main "$@"
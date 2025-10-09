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
    export QUIET_MODE=true
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

    # Generate WireGuard config (without killswitch)
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

    # Verify connectivity BEFORE applying killswitch
    show_step "Verifying connection..."
    EXTERNAL_IP=$(timeout 10 curl -s ifconfig.me 2>/dev/null || echo "")
    if [ -n "$EXTERNAL_IP" ]; then
        show_success "External IP: ${grn}${bold}${EXTERNAL_IP}${nc}"
    else
        show_warning "Could not verify external IP"
    fi
    echo ""

    # NOW apply the killswitch after we know it's working
    # Use fwmark instead of interface to work with WireGuard's policy routing
    show_step "Activating killswitch..."
    
    # Get the fwmark that WireGuard is using
    FWMARK=$(wg show pia fwmark)
    
    # Allow traffic marked by WireGuard (will go through tunnel)
    iptables -I OUTPUT 1 -m mark --mark $FWMARK -j ACCEPT 2>/dev/null || true
    # Allow established/related connections (helps with K8s services)
    iptables -I OUTPUT 2 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
    # Allow loopback
    iptables -I OUTPUT 3 -o lo -j ACCEPT 2>/dev/null || true
    # Allow local networks (Docker/K8s)
    iptables -I OUTPUT 4 -d 10.0.0.0/8 -j ACCEPT 2>/dev/null || true
    iptables -I OUTPUT 5 -d 172.16.0.0/12 -j ACCEPT 2>/dev/null || true
    iptables -I OUTPUT 6 -d 192.168.0.0/16 -j ACCEPT 2>/dev/null || true
    iptables -I OUTPUT 7 -d 169.254.0.0/16 -j ACCEPT 2>/dev/null || true
    # Allow traffic on the tunnel interface itself
    iptables -I OUTPUT 8 -o pia -j ACCEPT 2>/dev/null || true
    # Block everything else (prevents leaks)
    iptables -A OUTPUT -j REJECT --reject-with icmp-net-unreachable 2>/dev/null || true
    
    show_success "Killswitch active (fwmark: $FWMARK)"
    echo ""

    # Port Forward if enabled
    if [ "${PORT_FORWARDING}" = "true" ]; then
        show_step "Initializing port forwarding..."
        echo ""
        /app/scripts/port_forwarding.sh
    else
        echo "${grn}╔════════════════════════════════════════════════╗${nc}"
        echo "${grn}║${nc}                ${grn}✓${nc} ${bold}VPN Connected${nc}                 ${grn}║${nc}"
        echo "${grn}╚════════════════════════════════════════════════╝${nc}"
        echo ""
        tail -f /dev/null
    fi
}

# Run main
main "$@"

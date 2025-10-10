#!/bin/bash

# WireGuard tunnel management functions

# Bring up WireGuard manually (avoiding wg-quick's sysctl attempts)
bring_up_wireguard() {
    local config="$1"
    
    # Parse config file
    local private_key=$(grep '^PrivateKey' "$config" | cut -d= -f2- | xargs)
    local address=$(grep '^Address' "$config" | cut -d= -f2- | xargs)
    local dns=$(grep '^DNS' "$config" | cut -d= -f2- | xargs)
    local mtu=$(grep '^MTU' "$config" | cut -d= -f2- | xargs)
    
    local peer_pubkey=$(grep '^PublicKey' "$config" | cut -d= -f2- | xargs)
    local endpoint=$(grep '^Endpoint' "$config" | cut -d= -f2- | xargs)
    local allowed_ips=$(grep '^AllowedIPs' "$config" | cut -d= -f2- | xargs)
    local keepalive=$(grep '^PersistentKeepalive' "$config" | cut -d= -f2- | xargs)
    
    # Create interface
    ip link add pia type wireguard || return 1
    
    # Set private key
    wg set pia private-key <(echo "$private_key") || return 1
    
    # Add address
    ip address add "$address" dev pia || return 1
    
    # OPTIMIZATION: Set MTU based on path MTU discovery
    # WireGuard overhead is 60 bytes, so for 1500 byte physical MTU:
    # Optimal WireGuard MTU = 1440, but use 1392 for problematic paths
    if [ -n "$mtu" ]; then
        ip link set mtu "$mtu" dev pia
    else
        # Use 1392 as safe default (works through most paths without fragmentation)
        # Can try 1412 or 1420 if your path supports it
        ip link set mtu 1392 dev pia
    fi
    
    # Configure peer
    wg set pia peer "$peer_pubkey" endpoint "$endpoint" allowed-ips "$allowed_ips" persistent-keepalive "$keepalive" || return 1
    
    # Set fwmark for policy routing
    wg set pia fwmark 51820 || return 1
    
    # Bring up interface
    ip link set pia up || return 1
    
    # Add routes
    ip route add 0.0.0.0/0 dev pia table 51820 || return 1
    ip rule add not fwmark 51820 table 51820 || return 1
    ip rule add table main suppress_prefixlength 0 || return 1
    
    # Set DNS if specified
    if [ -n "$dns" ]; then
        cp /etc/resolv.conf /etc/resolv.conf.bak 2>/dev/null || true
        echo "# Set by PIA WireGuard" > /etc/resolv.conf
        IFS=',' read -ra DNS_SERVERS <<< "$dns"
        for server in "${DNS_SERVERS[@]}"; do
            server=$(echo "$server" | xargs)
            echo "nameserver $server" >> /etc/resolv.conf
        done
    fi
    
    return 0
}

# Teardown WireGuard tunnel
teardown_wireguard() {
    if wg show pia >/dev/null 2>&1; then
        ip link set pia down 2>/dev/null || true
        ip link del pia 2>/dev/null || true
    fi
    
    # Restore DNS
    if [ -f /etc/resolv.conf.bak ]; then
        mv /etc/resolv.conf.bak /etc/resolv.conf 2>/dev/null || true
    fi
}

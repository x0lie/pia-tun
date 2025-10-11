#!/bin/bash

# WireGuard tunnel management functions

# Setup DNS configuration
setup_dns() {
    local dns="$1"
    
    if [ -z "$dns" ]; then
        return 0
    fi
    
    # Backup original resolv.conf
    [ ! -f /etc/resolv.conf.bak ] && cp /etc/resolv.conf /etc/resolv.conf.bak 2>/dev/null || true
    
    # Write new resolv.conf
    echo "# Set by PIA WireGuard" > /etc/resolv.conf
    IFS=',' read -ra DNS_SERVERS <<< "$dns"
    for server in "${DNS_SERVERS[@]}"; do
        server=$(echo "$server" | xargs)
        [ -n "$server" ] && echo "nameserver $server" >> /etc/resolv.conf
    done
}

# Restore working DNS (for reconnection)
restore_dns() {
    echo "# DNS for reconnection" > /etc/resolv.conf
    echo "nameserver 1.1.1.1" >> /etc/resolv.conf
    echo "nameserver 8.8.8.8" >> /etc/resolv.conf
}

# Add local network routing exceptions
add_local_network_exceptions() {
    if [ "$LOCAL_NETWORK" = "all" ]; then
        # Exempt all RFC1918 private networks
        ip rule add to 10.0.0.0/8 table main priority 100 2>/dev/null || true
        ip rule add to 172.16.0.0/12 table main priority 100 2>/dev/null || true
        ip rule add to 192.168.0.0/16 table main priority 100 2>/dev/null || true
        ip rule add to 169.254.0.0/16 table main priority 100 2>/dev/null || true
    elif [ -n "$LOCAL_NETWORK" ]; then
        # User specified custom local networks
        IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORK"
        for network in "${NETWORKS[@]}"; do
            network=$(echo "$network" | xargs)
            # Only handle IPv4 for routing exceptions
            [[ "$network" != *":"* ]] && ip rule add to "$network" table main priority 100 2>/dev/null || true
        done
    fi
}

# Bring up WireGuard manually
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
    
    # Set MTU (default 1392 for optimal performance)
    ip link set mtu "${mtu:-1392}" dev pia
    
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
    
    # Add local network exceptions
    add_local_network_exceptions
    
    # Setup DNS
    setup_dns "$dns"
    
    return 0
}

# Teardown WireGuard tunnel
teardown_wireguard() {
    if wg show pia >/dev/null 2>&1; then
        ip link set pia down 2>/dev/null || true
        ip link del pia 2>/dev/null || true
    fi

    # Restore working DNS for reconnection
    restore_dns
}

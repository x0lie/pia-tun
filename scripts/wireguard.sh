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
    
    # Add routing exceptions for local networks (so they don't go through VPN)
    # Only add exceptions if LOCAL_NETWORK is explicitly set
    if [ "$LOCAL_NETWORK" = "all" ]; then
        # Exempt all RFC1918 private networks from VPN routing
        ip rule add to 10.0.0.0/8 table main priority 100 || true
        ip rule add to 172.16.0.0/12 table main priority 100 || true
        ip rule add to 192.168.0.0/16 table main priority 100 || true
        ip rule add to 169.254.0.0/16 table main priority 100 || true
    elif [ -n "$LOCAL_NETWORK" ]; then
        # User specified custom local networks - only exempt those
        IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORK"
        for network in "${NETWORKS[@]}"; do
            network=$(echo "$network" | xargs)
            # Only handle IPv4 for routing exceptions
            if [[ "$network" != *":"* ]]; then
                # Suppress the /0 default route for this specific destination
                ip rule add to "$network" table main priority 100 || true
            fi
        done
    fi
    # If LOCAL_NETWORK is empty/unset: no exceptions, all traffic through VPN
    
    # Set DNS if specified
    if [ -n "$dns" ]; then
        # Backup original resolv.conf
        if [ ! -f /etc/resolv.conf.bak ]; then
            cp /etc/resolv.conf /etc/resolv.conf.bak 2>/dev/null || true
        fi
        
        # Write new resolv.conf
        echo "# Set by PIA WireGuard" > /etc/resolv.conf
        IFS=',' read -ra DNS_SERVERS <<< "$dns"
        for server in "${DNS_SERVERS[@]}"; do
            server=$(echo "$server" | xargs)  # Trim whitespace
            if [ -n "$server" ]; then
                echo "nameserver $server" >> /etc/resolv.conf
            fi
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

    # CRITICAL: Restore working DNS for reconnection
    # The backed up resolv.conf might have PIA DNS (10.0.0.x) which won't work without the tunnel
    # Always use public DNS during reconnection to be safe
    echo "# DNS for reconnection" > /etc/resolv.conf
    echo "nameserver 1.1.1.1" >> /etc/resolv.conf
    echo "nameserver 8.8.8.8" >> /etc/resolv.conf
}

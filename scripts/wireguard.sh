#!/bin/bash

# WireGuard tunnel management functions

# Parse WireGuard config into associative array
parse_wg_config() {
    local config="$1"
    declare -gA WG_CONFIG
    
    while IFS= read -r line; do
        # Skip empty lines, comments, and section headers
        [[ -z "$line" || "$line" =~ ^[[:space:]]*# || "$line" =~ ^\[.*\]$ ]] && continue
        
        # Split on first '=' only (preserves base64 padding)
        if [[ "$line" =~ ^([^=]+)=(.*)$ ]]; then
            local key="${BASH_REMATCH[1]}"
            local value="${BASH_REMATCH[2]}"
            
            # Trim whitespace from key and value
            key=$(echo "$key" | xargs)
            value=$(echo "$value" | xargs)
            
            [ -n "$key" ] && WG_CONFIG[$key]="$value"
        fi
    done < "$config"
}

# Setup DNS configuration
setup_dns() {
    local dns="$1"
    [ -z "$dns" ] && return 0
    
    [ ! -f /etc/resolv.conf.bak ] && cp /etc/resolv.conf /etc/resolv.conf.bak 2>/dev/null || true
    
    {
        echo "# Set by PIA WireGuard"
        IFS=',' read -ra DNS_SERVERS <<< "$dns"
        for server in "${DNS_SERVERS[@]}"; do
            echo "nameserver $(echo "$server" | xargs)"
        done
    } > /etc/resolv.conf
}

# Add local network routing exceptions
add_local_exceptions() {
    local networks=("$@")
    for network in "${networks[@]}"; do
        # Only process IPv4 networks
        if [[ "$network" != *":"* ]]; then
            # Add rule to use main routing table for this network
            # Priority 100 ensures these take precedence over VPN routing
            ip rule add to "$network" table main priority 100 2>/dev/null || true
        fi
    done
}

# Clean up local network rules
cleanup_local_exceptions() {
    # Remove all our custom rules (priorities 100, 200, 300)
    while ip rule del priority 100 2>/dev/null; do :; done
    while ip rule del priority 200 2>/dev/null; do :; done
    while ip rule del priority 300 2>/dev/null; do :; done
}

# Bring up WireGuard tunnel
bring_up_wireguard() {
    local config="$1"
    parse_wg_config "$config"
    
    # Create and configure interface
    ip link add pia type wireguard || return 1
    wg set pia private-key <(echo "${WG_CONFIG[PrivateKey]}") || return 1
    ip address add "${WG_CONFIG[Address]}" dev pia || return 1
    ip link set mtu "${WG_CONFIG[MTU]:-1392}" dev pia
    
    # Configure peer
    wg set pia \
        peer "${WG_CONFIG[PublicKey]}" \
        endpoint "${WG_CONFIG[Endpoint]}" \
        allowed-ips "${WG_CONFIG[AllowedIPs]}" \
        persistent-keepalive "${WG_CONFIG[PersistentKeepalive]}" || return 1
    
    # Setup routing with fwmark
    wg set pia fwmark 51820 || return 1
    ip link set pia up || return 1
    
    # Add VPN routes to separate table
    ip route add 0.0.0.0/0 dev pia table 51820 || return 1
    
    # CRITICAL: Add local network exceptions BEFORE VPN routing rules
    # This ensures local traffic is evaluated first
    if [ "$LOCAL_NETWORK" = "all" ]; then
        ip rule add to 10.0.0.0/8 table main priority 100 2>/dev/null || true
        ip rule add to 172.16.0.0/12 table main priority 100 2>/dev/null || true
        ip rule add to 192.168.0.0/16 table main priority 100 2>/dev/null || true
        ip rule add to 169.254.0.0/16 table main priority 100 2>/dev/null || true
    elif [ -n "$LOCAL_NETWORK" ]; then
        IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORK"
        for network in "${NETWORKS[@]}"; do
            network=$(echo "$network" | xargs)
            # Only IPv4 networks
            [[ "$network" != *":"* ]] && ip rule add to "$network" table main priority 100 2>/dev/null || true
        done
    fi
    
    # Now add VPN routing rules (priority 200, after local networks)
    ip rule add not fwmark 51820 table 51820 priority 200 || return 1
    ip rule add table main suppress_prefixlength 0 priority 300 || return 1
    
    # Setup DNS
    setup_dns "${WG_CONFIG[DNS]}"
    
    return 0
}

# Teardown WireGuard tunnel
teardown_wireguard() {
    if wg show pia >/dev/null 2>&1; then
        ip link set pia down 2>/dev/null || true
        ip link del pia 2>/dev/null || true
    fi
    
    # Clean up local network exceptions
    cleanup_local_exceptions
    
    # Restore working DNS
    {
        echo "# DNS for reconnection"
        echo "nameserver 1.1.1.1"
        echo "nameserver 8.8.8.8"
    } > /etc/resolv.conf
}

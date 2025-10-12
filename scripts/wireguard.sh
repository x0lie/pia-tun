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
        [[ "$network" != *":"* ]] && ip rule add to "$network" table main priority 100 2>/dev/null || true
    done
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
    
    # Setup routing
    wg set pia fwmark 51820 || return 1
    ip link set pia up || return 1
    ip route add 0.0.0.0/0 dev pia table 51820 || return 1
    ip rule add not fwmark 51820 table 51820 || return 1
    ip rule add table main suppress_prefixlength 0 || return 1
    
    # Handle local network exceptions
    if [ "$LOCAL_NETWORK" = "all" ]; then
        add_local_exceptions "10.0.0.0/8" "172.16.0.0/12" "192.168.0.0/16" "169.254.0.0/16"
    elif [ -n "$LOCAL_NETWORK" ]; then
        IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORK"
        add_local_exceptions "${NETWORKS[@]}"
    fi
    
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
    
    # Restore working DNS
    {
        echo "# DNS for reconnection"
        echo "nameserver 1.1.1.1"
        echo "nameserver 8.8.8.8"
    } > /etc/resolv.conf
}

#!/bin/bash

# Merged VPN Setup Script
# Combines: get_token.sh, get_server_info.sh, generate_config.sh

set -e
source /app/scripts/ui.sh

# Step 1: Authenticate and get token
authenticate() {
    local response=$(curl -s --insecure -u "$PIA_USER:$PIA_PASS" \
        "https://www.privateinternetaccess.com/gtoken/generateToken")
    
    local token=$(echo "$response" | jq -r '.token // empty')
    
    if [ -z "$token" ]; then
        show_error "Authentication failed - check credentials"
        return 1
    fi
    
    echo "$token" > /tmp/pia_token
    echo "$token"
}

# Step 2: Find best server
find_server() {
    local max_latency="${MAX_LATENCY:-1}"
    local serverlist_url='https://serverlist.piaservers.net/vpninfo/servers/v6'
    
    # Get server list
    local all_regions=$(curl -s "$serverlist_url" | head -1)
    
    if [ ${#all_regions} -lt 1000 ]; then
        show_error "Could not fetch server list"
        return 1
    fi
    
    # Get region data
    local region_data=$(echo "$all_regions" | jq --arg REGION "$PIA_LOCATION" -r '.regions[] | select(.id==$REGION)')
    
    if [ -z "$region_data" ]; then
        show_error "Invalid location: $PIA_LOCATION"
        return 1
    fi
    
    # Check port forwarding support if needed
    if [ "$PORT_FORWARDING" = "true" ]; then
        local supports_pf=$(echo "$region_data" | jq -r '.port_forward')
        if [ "$supports_pf" != "true" ]; then
            show_error "Port forwarding not supported in $PIA_LOCATION"
            return 1
        fi
    fi
    
    # Get WireGuard servers
    local wg_servers=$(echo "$region_data" | jq -r '.servers.wg[] | .ip + " " + .cn')
    
    if [ -z "$wg_servers" ]; then
        show_error "No WireGuard servers found"
        return 1
    fi
    
    # Find fastest server
    local latencies=$(mktemp)
    echo "$wg_servers" | while read -r ip cn; do
        local time_sec=$(curl -k -o /dev/null -s --connect-timeout "$max_latency" \
            --write-out "%{time_connect}" "https://$ip:443" 2>/dev/null || echo "999")
        
        if [[ "$time_sec" != "999" && "$time_sec" != "0.000"* ]]; then
            local time_ms=$(awk "BEGIN {printf \"%.0f\", $time_sec * 1000}")
            echo "$time_ms $ip $cn" >> "$latencies"
        fi
    done
    
    # Select best server
    if [ -s "$latencies" ]; then
        local best=$(sort -n "$latencies" | head -1)
        BEST_IP=$(echo "$best" | awk '{print $2}')
        BEST_CN=$(echo "$best" | awk '{print $3}')
        BEST_TIME=$(echo "$best" | awk '{print $1}')
    else
        BEST_IP=$(echo "$region_data" | jq -r '.servers.wg[0].ip')
        BEST_CN=$(echo "$region_data" | jq -r '.servers.wg[0].cn')
        BEST_TIME="timeout"
    fi
    
    rm -f "$latencies"
    
    # Save for later use
    echo "$BEST_IP" > /tmp/server_endpoint
    echo "$BEST_CN" > /tmp/meta_cn
    
    echo "$BEST_CN|$BEST_TIME"
}

# Step 3: Generate WireGuard config
generate_config() {
    local token="$1"
    local endpoint_ip=$(cat /tmp/server_endpoint)
    local meta_cn=$(cat /tmp/meta_cn)
    
    # Generate keys
    local private_key=$(wg genkey)
    local public_key=$(echo "$private_key" | wg pubkey)
    
    # Call addKey API
    local response=$(curl -s -k -G \
        --connect-to "$meta_cn::$endpoint_ip:" \
        --data-urlencode "pt=$token" \
        --data-urlencode "pubkey=$public_key" \
        "https://$meta_cn:1337/addKey")
    
    local status=$(echo "$response" | jq -r '.status // empty')
    if [ "$status" != "OK" ]; then
        show_error "Server registration failed"
        return 1
    fi
    
    # Extract config details
    local server_key=$(echo "$response" | jq -r '.server_key')
    local peer_ip=$(echo "$response" | jq -r '.peer_ip')
    local server_port=$(echo "$response" | jq -r '.server_port // 1337')
    local pf_gateway=$(echo "$response" | jq -r '.server_vip // empty')
    
    # Save for port forwarding
    echo "$peer_ip" > /tmp/client_ip
    echo "$pf_gateway" > /tmp/pf_gateway
    
    # Determine DNS
    local dns_config=""
    if [ -n "$DNS" ] && [ "$DNS" != "pia" ] && [ "$DNS" != "none" ]; then
        dns_config="DNS = $DNS"
    elif [ "$DNS" != "none" ]; then
        dns_config="DNS = 10.0.0.243, 10.0.0.242"
    fi
    
    # Generate config
    local allowed_ips="0.0.0.0/0"
    [ "${DISABLE_IPV6}" != "true" ] && allowed_ips="0.0.0.0/0, ::/0"
    
    cat > /etc/wireguard/pia.conf << EOF
[Interface]
PrivateKey = $private_key
Address = $peer_ip/32
${dns_config}
MTU = ${MTU:-1392}

[Peer]
PublicKey = $server_key
Endpoint = $endpoint_ip:$server_port
AllowedIPs = $allowed_ips
PersistentKeepalive = 25
EOF
    
    return 0
}

# Main execution
main() {
    # Authenticate
    show_step "Authenticating with PIA..."
    if token=$(authenticate); then
        show_success "Authentication successful"
    else
        return 1
    fi
    echo ""
    
    # Find server
    show_step "Finding optimal server for ${bold}${PIA_LOCATION}${nc}..."
    if server_info=$(find_server); then
        IFS='|' read -r server_name latency <<< "$server_info"
        if [ "$latency" = "timeout" ]; then
            show_warning "Server selected: $server_name (no latency data)"
        else
            show_success "Connected to: ${bold}${server_name}${nc} (${latency}ms)"
        fi
    else
        return 1
    fi
    echo ""
    
    # Generate config
    show_step "Configuring WireGuard tunnel..."
    if generate_config "$token"; then
        show_success "Tunnel configured"
    else
        return 1
    fi
    
    return 0
}

# Run if executed directly
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    main
fi

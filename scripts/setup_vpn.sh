#!/bin/bash

set -e
source /app/scripts/ui.sh

authenticate() {
    local response=$(curl -s --insecure -u "$PIA_USER:$PIA_PASS" \
        "https://www.privateinternetaccess.com/gtoken/generateToken")
    
    local token=$(echo "$response" | jq -r '.token // empty')
    [ -z "$token" ] && { show_error "Authentication failed"; return 1; }
    
    echo "$token" > /tmp/pia_token
    echo "$token"
}

find_server() {
    local max_latency="${MAX_LATENCY:-1}"
    local all_regions=$(curl -s 'https://serverlist.piaservers.net/vpninfo/servers/v6' | head -1)
    
    [ ${#all_regions} -lt 1000 ] && { show_error "Could not fetch server list"; return 1; }
    
    local region_data=$(echo "$all_regions" | jq --arg REGION "$PIA_LOCATION" -r '.regions[] | select(.id==$REGION)')
    [ -z "$region_data" ] && { show_error "Invalid location: $PIA_LOCATION"; return 1; }
    
    # Check port forwarding if needed
    if [ "$PORT_FORWARDING" = "true" ]; then
        local supports_pf=$(echo "$region_data" | jq -r '.port_forward')
        [ "$supports_pf" != "true" ] && { show_error "Port forwarding not supported in $PIA_LOCATION"; return 1; }
    fi
    
    local wg_servers=$(echo "$region_data" | jq -r '.servers.wg[] | .ip + " " + .cn')
    [ -z "$wg_servers" ] && { show_error "No WireGuard servers found"; return 1; }
    
    # Find fastest server
    local latencies=$(mktemp)
    while read -r ip cn; do
        local time_sec=$(curl -k -o /dev/null -s --connect-timeout "$max_latency" \
            --write-out "%{time_connect}" "https://$ip:443" 2>/dev/null || echo "999")
        
        [[ "$time_sec" != "999" && "$time_sec" != "0.000"* ]] && {
            local time_ms=$(awk "BEGIN {printf \"%.0f\", $time_sec * 1000}")
            echo "$time_ms $ip $cn" >> "$latencies"
        }
    done <<< "$wg_servers"
    
    # Select best
    if [ -s "$latencies" ]; then
        read -r BEST_TIME BEST_IP BEST_CN <<< "$(sort -n "$latencies" | head -1)"
    else
        BEST_IP=$(echo "$region_data" | jq -r '.servers.wg[0].ip')
        BEST_CN=$(echo "$region_data" | jq -r '.servers.wg[0].cn')
        BEST_TIME="timeout"
    fi
    rm -f "$latencies"
    
    echo "$BEST_IP" > /tmp/server_endpoint
    echo "$BEST_CN" > /tmp/meta_cn
    echo "$BEST_CN|$BEST_TIME"
}

generate_config() {
    local token="$1"
    local endpoint_ip=$(cat /tmp/server_endpoint)
    local meta_cn=$(cat /tmp/meta_cn)
    
    local private_key=$(wg genkey)
    local public_key=$(echo "$private_key" | wg pubkey)
    
    local response=$(curl -s -k -G \
        --connect-to "$meta_cn::$endpoint_ip:" \
        --data-urlencode "pt=$token" \
        --data-urlencode "pubkey=$public_key" \
        "https://$meta_cn:1337/addKey")
    
    # Parse all JSON fields at once (single jq call)
    local json_data=$(echo "$response" | jq -r '[.status, .server_key, .peer_ip, (.server_port // 1337), (.server_vip // "")] | @tsv')
    local status server_key peer_ip server_port pf_gateway
    read -r status server_key peer_ip server_port pf_gateway <<< "$json_data"
    
    [ "$status" != "OK" ] && { show_error "Server registration failed"; return 1; }
    
    echo "$peer_ip" > /tmp/client_ip
    echo "$pf_gateway" > /tmp/pf_gateway
    
    # Determine DNS
    local dns_config=""
    if [ -n "$DNS" ] && [ "$DNS" != "pia" ] && [ "$DNS" != "none" ]; then
        dns_config="DNS = $DNS"
    elif [ "$DNS" != "none" ]; then
        dns_config="DNS = 10.0.0.243, 10.0.0.242"
    fi
    
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
}

main() {
    show_step "Authenticating with PIA..."
    token=$(authenticate) && show_success "Authentication successful" || return 1
    echo ""
    
    show_step "Finding optimal server for ${bold}${PIA_LOCATION}${nc}..."
    server_info=$(find_server) || return 1
    IFS='|' read -r server_name latency <<< "$server_info"
    [ "$latency" = "timeout" ] && show_warning "Server selected: $server_name (no latency data)" || \
        show_success "Connected to: ${bold}${server_name}${nc} (${latency}ms)"
    echo ""
    
    show_step "Configuring WireGuard tunnel..."
    generate_config "$token" && show_success "Tunnel configured" || return 1
}

# Only run if executed directly (not sourced)
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    main
fi

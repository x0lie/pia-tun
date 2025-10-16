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
    
    # Parse locations (comma or space separated)
    local locations="${PIA_LOCATION}"
    locations=$(echo "$locations" | tr ',' ' ')
    
    # Check if multiple locations provided
    local location_count=$(echo "$locations" | wc -w)
    
    local all_candidates=$(mktemp)
    local pf_required=false
    [ "$PORT_FORWARDING" = "true" ] && pf_required=true
    
    # Collect all WireGuard servers from all specified locations
    for location in $locations; do
        local region_data=$(echo "$all_regions" | jq --arg REGION "$location" -r '.regions[] | select(.id==$REGION)')
        
        if [ -z "$region_data" ]; then
            echo "  ${ylw}⚠${nc} Location not found: $location (skipping)" >&2
            continue
        fi
        
        # Check port forwarding if needed
        if $pf_required; then
            local supports_pf=$(echo "$region_data" | jq -r '.port_forward')
            if [ "$supports_pf" != "true" ]; then
                echo "  ${ylw}⚠${nc} Port forwarding not supported in $location (skipping)" >&2
                continue
            fi
        fi
        
        local region_name=$(echo "$region_data" | jq -r '.name')
        
        # Extract WireGuard servers with location info
        echo "$region_data" | jq -r --arg LOC "$location" --arg NAME "$region_name" \
            '.servers.wg[] | "\(.ip) \(.cn) \($LOC) \($NAME)"' >> "$all_candidates"
    done
    
    # Check if we found any candidates
    if [ ! -s "$all_candidates" ]; then
        rm -f "$all_candidates"
        echo "error|No valid servers found"
        return 1
    fi
    
    # Test latency for all candidates
    local latencies=$(mktemp)
    local tested=0
    local successful=0
    
    while read -r ip cn location_id location_name; do
        tested=$((tested + 1))
        local time_sec=$(curl -k -o /dev/null -s --connect-timeout "$max_latency" \
            --write-out "%{time_connect}" "https://$ip:443" 2>/dev/null || echo "999")
        
        if [[ "$time_sec" != "999" && "$time_sec" != "0.000"* ]]; then
            local time_ms=$(awk "BEGIN {printf \"%.0f\", $time_sec * 1000}")
            echo "$time_ms $ip $cn $location_id $location_name" >> "$latencies"
            successful=$((successful + 1))
        fi
    done < "$all_candidates"
    
    rm -f "$all_candidates"
    
    # Select best server
    if [ -s "$latencies" ]; then
        read -r BEST_TIME BEST_IP BEST_CN BEST_LOCATION_ID BEST_LOCATION_NAME <<< "$(sort -n "$latencies" | head -1)"
        
        # Save stats for later display
        echo "$tested" > /tmp/servers_tested
        echo "$successful" > /tmp/servers_responded
        echo "$location_count" > /tmp/location_count
    else
        # Fallback to first server if all timeout
        read -r ip cn location_id location_name <<< "$(head -1 < "$all_candidates")"
        BEST_IP="$ip"
        BEST_CN="$cn"
        BEST_LOCATION_ID="$location_id"
        BEST_LOCATION_NAME="$location_name"
        BEST_TIME="timeout"
        
        echo "$tested" > /tmp/servers_tested
        echo "0" > /tmp/servers_responded
        echo "$location_count" > /tmp/location_count
    fi

    # Save latency for metrics
    [[ "$BEST_TIME" != "timeout" && "$BEST_TIME" =~ ^[0-9]+$ ]] && echo "$BEST_TIME" > /tmp/server_latency_temp

    rm -f "$latencies"

    echo "$BEST_IP" > /tmp/server_endpoint
    echo "$BEST_CN" > /tmp/meta_cn
    echo "$BEST_LOCATION_ID" > /tmp/server_location
    echo "$BEST_LOCATION_NAME" > /tmp/server_location_name
    
    # Return simple info for display (just server name and latency)
    echo "$BEST_CN|$BEST_TIME"
}

generate_config() {
    local token="$1"
    local endpoint_ip=$(cat /tmp/server_endpoint)
    local meta_cn=$(cat /tmp/meta_cn)
    
    # Allow manual META_CN override
    if [ -n "$META_CN" ]; then
        show_warning "Using manual META_CN override: $META_CN"
        meta_cn="$META_CN"
        echo "$META_CN" > /tmp/meta_cn
    fi
    
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
    
    # Parse location display
    local locations="${PIA_LOCATION}"
    locations=$(echo "$locations" | tr ',' ' ')
    local location_count=$(echo "$locations" | wc -w)
    
    if [ $location_count -gt 1 ]; then
        show_step "Finding optimal server across locations: ${bold}$(echo $locations | tr ' ' ', ')${nc}..."
    else
        show_step "Finding optimal server for ${bold}${PIA_LOCATION}${nc}..."
    fi
    
    server_info=$(find_server) || return 1
    IFS='|' read -r server_name latency <<< "$server_info"
    
    # Check if we tested multiple locations
    if [ -f /tmp/location_count ] && [ "$(cat /tmp/location_count)" -gt 1 ]; then
        local tested=$(cat /tmp/servers_tested 2>/dev/null || echo "0")
        local responded=$(cat /tmp/servers_responded 2>/dev/null || echo "0")
        local location_name=$(cat /tmp/server_location_name 2>/dev/null || echo "")
        
        [ "$responded" -gt 0 ] && show_success "Tested ${tested} servers, ${responded} responded"
        [ -n "$location_name" ] && show_success "Best server: ${location_name}"
    fi
    
    if [ "$latency" = "timeout" ]; then
        show_warning "Server selected: $server_name (no latency data)"
    else
        show_success "Connected to: ${bold}${server_name}${nc} (${latency}ms)"
    fi
    echo ""
    
    show_step "Configuring WireGuard tunnel..."
    generate_config "$token" && show_success "Tunnel configured" || return 1
}

# Only run if executed directly (not sourced)
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    main
fi

# USAGE EXAMPLES:
#
# Single location (original behavior):
#   PIA_LOCATION=ca_toronto
#
# Multiple locations (picks lowest ping):
#   PIA_LOCATION=ca_ontario,ca_toronto
#   PIA_LOCATION="ca_ontario ca_toronto"  # Space-separated also works
#
# Manual META_CN override (for advanced users):
#   META_CN=toronto401  # Overrides automatic CN detection
#   # Use this if you know the exact CN you want to connect to
#   # Find available CNs in the server list response

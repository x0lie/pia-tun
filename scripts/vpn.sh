#!/bin/bash
# Unified VPN management - Authentication, server selection, and WireGuard interface
# Merged from setup_vpn.sh + wireguard.sh for better cohesion

set -e
source /app/scripts/ui.sh

#═══════════════════════════════════════════════════════════════════════════════
# AUTHENTICATION & SERVER SELECTION
#═══════════════════════════════════════════════════════════════════════════════

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
    if [ -n "${META_CN:-}" ]; then
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
MTU = ${MTU:-1420}

[Peer]
PublicKey = $server_key
Endpoint = $endpoint_ip:$server_port
AllowedIPs = $allowed_ips
PersistentKeepalive = 25
EOF
}

#═══════════════════════════════════════════════════════════════════════════════
# WIREGUARD INTERFACE MANAGEMENT
#═══════════════════════════════════════════════════════════════════════════════

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

# Clean up local network routing rules
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

#═══════════════════════════════════════════════════════════════════════════════
# MAIN SETUP FUNCTION (Called from run.sh)
#═══════════════════════════════════════════════════════════════════════════════

setup_vpn() {
    local restart="${1:-false}"
    
    show_step "Authenticating with PIA..."
    local token=$(authenticate) && show_success "Authentication successful" || return 1
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
    
    local server_info=$(find_server) || return 1
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
	echo ""
    fi
    
    if [ "$restart" != "true" ]; then
        show_step "Configuring WireGuard tunnel..."
        generate_config "$token" && show_success "Tunnel configured" || return 1
	echo ""
    else
        # Silent configuration during reconnection
        generate_config "$token" >/dev/null 2>&1 || return 1
    fi
}

# Only run setup_vpn if executed directly (not sourced)
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    setup_vpn "$@"
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

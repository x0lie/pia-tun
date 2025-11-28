#!/bin/bash
# Unified VPN management - Authentication, server selection, and WireGuard interface
# Uses surgical firewall exemptions for secure PIA API access

set -e
source /app/scripts/ui.sh
source /app/scripts/killswitch.sh

#═══════════════════════════════════════════════════════════════════════════════
# DNS RESOLUTION (Using bypass Cloudflare DNS)
#═══════════════════════════════════════════════════════════════════════════════

# Resolve hostname using Cloudflare DNS (1.0.0.1, already in bypass routes)
resolve_hostname() {
    local hostname="$1"
    show_debug "Resolving hostname: $hostname"
    
    # Try 1.0.0.1 (Cloudflare)
    add_temporary_exemption "1.0.0.1" "443" "tcp" "dns_resolve"
    
    show_debug "Querying 1.0.0.1 for $hostname"
    local ip=$(curl -fsS --max-time 10 \
        "https://1.0.0.1/dns-query?name=$hostname&type=A" \
        -H 'accept: application/dns-json' | \
        jq -r '.Answer // empty | .[] | select(.type == 1) | .data' | head -n1)
    remove_temporary_exemption "dns_resolve"
    
    if [[ "$ip" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
        show_debug "Resolved $hostname to $ip via 1.0.0.1"
        echo "$ip"
        return 0
    fi
    
    show_debug "1.0.0.1 failed, trying fallback 1.1.1.1"

    # Fallback to 1.1.1.1
    add_temporary_exemption "1.1.1.1" "443" "tcp" "dns_resolve"
    
    show_debug "Querying 1.1.1.1 for $hostname"
    local ip=$(curl -fsS --max-time 10 \
        "https://1.1.1.1/dns-query?name=$hostname&type=A" \
        -H 'accept: application/dns-json' | \
        jq -r '.Answer // empty | .[] | select(.type == 1) | .data' | head -n1)
    remove_temporary_exemption "dns_resolve"
    
    if [[ "$ip" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
        show_debug "Resolved $hostname to $ip via 1.1.1.1"
        echo "$ip"
        return 0
    else
        show_debug "DNS resolution failed for $hostname (all services failed)"
        return 1
    fi
}

#═══════════════════════════════════════════════════════════════════════════════
# AUTHENTICATION & SERVER SELECTION
#═══════════════════════════════════════════════════════════════════════════════

authenticate() {
    local pia_user=""
    local pia_pass=""
    
    show_debug "Checking for PIA credentials..."
    
    # Check for Docker secrets first
    if [ -f "/run/secrets/pia_user" ]; then
        show_debug "Found Docker secret: /run/secrets/pia_user"
        pia_user=$(cat /run/secrets/pia_user)
    elif [ -n "${PIA_USER:-}" ]; then
        show_debug "Using PIA_USER environment variable"
        pia_user="${PIA_USER}"
    fi
    
    if [ -f "/run/secrets/pia_pass" ]; then
        show_debug "Found Docker secret: /run/secrets/pia_pass"
        pia_pass=$(cat /run/secrets/pia_pass)
    elif [ -n "${PIA_PASS:-}" ]; then
        show_debug "Using PIA_PASS environment variable"
        pia_pass="${PIA_PASS}"
    fi
    
    # Validate we have credentials from either source
    if [ -z "$pia_user" ] || [ -z "$pia_pass" ]; then
        show_error "PIA credentials not found (set PIA_USER/PIA_PASS or use Docker secrets)"
        show_debug "Credential check failed: user=${pia_user:+set} pass=${pia_pass:+set}"
        return 1
    fi
    
    show_debug "Credentials validated: username length=${#pia_user}"
    
    # Resolve PIA auth server IP
    show_debug "Resolving www.privateinternetaccess.com"
    local auth_ip=$(resolve_hostname "www.privateinternetaccess.com")
    if [ -z "$auth_ip" ]; then
        show_error "Cannot resolve privateinternetaccess.com"
        return 1
    fi
    
    show_debug "PIA auth server resolved to: $auth_ip"
    
    # Add surgical exemption for authentication
    add_temporary_exemption "$auth_ip" "443" "tcp" "pia_auth"
    
    # Perform authentication
    show_debug "Sending authentication request to https://www.privateinternetaccess.com/gtoken/generateToken"
    local response=$(curl -s --insecure -w "\n%{http_code}" -u "$pia_user:$pia_pass" \
        --connect-to "www.privateinternetaccess.com::$auth_ip:" \
        "https://www.privateinternetaccess.com/gtoken/generateToken" 2>/dev/null)
    
    # Remove exemption immediately
    remove_temporary_exemption "pia_auth"
    
    # Split response into body and HTTP code
    local http_code=$(echo "$response" | tail -1)
    local body=$(echo "$response" | sed '$d')
    
    show_debug "Authentication response: HTTP $http_code"
    [ $_LOG_LEVEL -ge 2 ] && [ -n "$body" ] && show_debug "Response body: ${body:0:200}..."
    
    # Check HTTP status
    if [ "$http_code" = "000" ]; then
        show_error "Authentication failed: Cannot reach PIA servers"
        show_debug "Connection failed (HTTP 000 - network error)"
        return 1
    elif [ "$http_code" = "401" ] || [ "$http_code" = "403" ]; then
        show_error "Authentication failed: Invalid username or password"
        show_debug "Authentication rejected (HTTP $http_code)"
        return 1
    elif [ "$http_code" != "200" ]; then
        show_error "Authentication failed: PIA server error (HTTP $http_code)"
        show_debug "Unexpected HTTP status code"
        return 1
    fi
    
    # Check if we got valid JSON
    if ! echo "$body" | jq -e . >/dev/null 2>&1; then
        show_error "Authentication failed: Invalid response from PIA"
        show_debug "Response is not valid JSON"
        return 1
    fi
    
    # Extract token
    local token=$(echo "$body" | jq -r '.token // empty')
    
    if [ -z "$token" ]; then
        local error_msg=$(echo "$body" | jq -r '.message // .error // empty')
        show_debug "No token in response, error message: $error_msg"
        
        if [ -n "$error_msg" ]; then
            case "$error_msg" in
                "authentication failed")
                    show_error "Authentication failed: Invalid username or password"
                    ;;
                *expired*)
                    show_error "Authentication failed: Account expired"
                    ;;
                *suspend*)
                    show_error "Authentication failed: Account suspended"
                    ;;
                *connection*|*limit*)
                    show_error "Authentication failed: Too many active connections"
                    ;;
                *)
                    show_error "Authentication failed: $error_msg"
                    ;;
            esac
        else
            show_error "Authentication failed: Unknown error (no token received)"
        fi
        
        return 1
    fi
    
    show_debug "Token received successfully (length: ${#token})"
    show_debug "Saving token to /tmp/pia_token"
    echo "$token" > /tmp/pia_token
    chmod 600 /tmp/pia_token
    echo "$token"
}

find_server() {
    local max_latency="${MAX_LATENCY:-1}"
    show_debug "Finding server with max_latency=${max_latency}s"
    
    # Resolve server list API
    show_debug "Resolving serverlist.piaservers.net"
    local serverlist_ip=$(resolve_hostname "serverlist.piaservers.net")
    if [ -z "$serverlist_ip" ]; then
        show_error "Cannot resolve serverlist.piaservers.net"
        return 1
    fi
    
    show_debug "Server list API resolved to: $serverlist_ip"
    
    # Add surgical exemption for server list fetch
    add_temporary_exemption "$serverlist_ip" "443" "tcp" "pia_serverlist"
    
    show_debug "Fetching server list from https://serverlist.piaservers.net/vpninfo/servers/v6"
    local all_regions=$(curl -s --connect-to "serverlist.piaservers.net::$serverlist_ip:" \
        'https://serverlist.piaservers.net/vpninfo/servers/v6' | head -1)
    
    # Remove exemption immediately
    remove_temporary_exemption "pia_serverlist"
    
    show_debug "Server list response size: ${#all_regions} bytes"
    [ ${#all_regions} -lt 1000 ] && { 
        show_error "Could not fetch server list"
        show_debug "Server list too small (< 1000 bytes), likely failed"
        return 1
    }
    
    # Parse locations (comma or space separated)
    local locations="${PIA_LOCATION}"
    locations=$(echo "$locations" | tr ',' ' ')
    
    # Check if multiple locations provided
    local location_count=$(echo "$locations" | wc -w)
    show_debug "Processing $location_count location(s): $locations"
    
    local all_candidates=$(mktemp)
    local pf_required=false
    [ "$PORT_FORWARDING" = "true" ] && pf_required=true
    show_debug "Port forwarding required: $pf_required"
    
    # Collect all WireGuard servers from all specified locations
    for location in $locations; do
        show_debug "Processing location: $location"
        local region_data=$(echo "$all_regions" | jq --arg REGION "$location" -r '.regions[] | select(.id==$REGION)')
        
        if [ -z "$region_data" ]; then
            echo "  ${ylw}⚠${nc} Location not found: $location (skipping)" >&2
            show_debug "Location '$location' not found in server list"
            continue
        fi
        
        local region_name=$(echo "$region_data" | jq -r '.name')
        show_debug "Found region: $region_name (id: $location)"
        
        # Check port forwarding if needed
        if $pf_required; then
            local supports_pf=$(echo "$region_data" | jq -r '.port_forward')
            show_debug "Port forwarding support for $location: $supports_pf"
            if [ "$supports_pf" != "true" ]; then
                echo "  ${ylw}⚠${nc} Port forwarding not supported in $location (skipping)" >&2
                continue
            fi
        fi
        
        # Extract WireGuard servers with location info
        local server_count=$(echo "$region_data" | jq -r '.servers.wg | length')
        show_debug "Found $server_count WireGuard server(s) in $location"
        
        echo "$region_data" | jq -r --arg LOC "$location" --arg NAME "$region_name" \
            '.servers.wg[] | "\(.ip) \(.cn) \($LOC) \($NAME)"' >> "$all_candidates"
    done
    
    # Check if we found any candidates
    local candidate_count=$(wc -l < "$all_candidates" 2>/dev/null || echo "0")
    show_debug "Total candidates collected: $candidate_count"
    
    if [ ! -s "$all_candidates" ]; then
        rm -f "$all_candidates"
        show_debug "No valid servers found after filtering"
        echo "error|No valid servers found"
        return 1
    fi
    
    # Test latency for all candidates (with surgical exemptions)
    show_debug "Starting latency tests for $candidate_count servers"
    local latencies=$(mktemp)
    local tested=0
    local successful=0
    
    while read -r ip cn location_id location_name; do
        tested=$((tested + 1))
        show_debug "Testing server $tested/$candidate_count: $cn ($ip) in $location_name"
        
        # Add surgical exemption for this specific server test
        add_temporary_exemption "$ip" "443" "tcp" "latency_test_$tested"
        
        local time_sec=$(curl -k -o /dev/null -s --connect-timeout "$max_latency" \
            --write-out "%{time_connect}" "https://$ip:443" 2>/dev/null || echo "999")
        
        # Remove exemption immediately
        remove_temporary_exemption "latency_test_$tested"
        
        if [[ "$time_sec" != "999" && "$time_sec" != "0.000"* ]]; then
            local time_ms=$(awk "BEGIN {printf \"%.0f\", $time_sec * 1000}")
            show_debug "Server $cn responded: ${time_ms}ms"
            echo "$time_ms $ip $cn $location_id $location_name" >> "$latencies"
            successful=$((successful + 1))
        else
            show_debug "Server $cn timed out or failed"
        fi
    done < "$all_candidates"
    
    show_debug "Latency testing complete: $successful/$tested servers responded"
    rm -f "$all_candidates"
    
    # Select best server
    if [ -s "$latencies" ]; then
        read -r BEST_TIME BEST_IP BEST_CN BEST_LOCATION_ID BEST_LOCATION_NAME <<< "$(sort -n "$latencies" | head -1)"
        show_debug "Best server selected: $BEST_CN ($BEST_IP) - ${BEST_TIME}ms"
        
        # Debug: Show top 5 servers
        if [ $_LOG_LEVEL -ge 2 ]; then
            show_debug "Top 5 servers by latency:"
            sort -n "$latencies" | head -5 | while read -r ms ip cn loc_id loc_name; do
                show_debug "  ${ms}ms - $cn ($loc_name)"
            done
        fi
        
        # Save stats for later display
        echo "$tested" > /tmp/servers_tested
        echo "$successful" > /tmp/servers_responded
        echo "$location_count" > /tmp/location_count
    else
        show_debug "All servers timed out, using fallback"
        # Fallback to first server if all timeout
        read -r ip cn location_id location_name <<< "$(head -1 < "$all_candidates" 2>/dev/null || show_info)"
        if [ -z "$ip" ]; then
            rm -f "$latencies"
            show_debug "No fallback server available"
            return 1
        fi
        BEST_IP="$ip"
        BEST_CN="$cn"
        BEST_LOCATION_ID="$location_id"
        BEST_LOCATION_NAME="$location_name"
        BEST_TIME="timeout"
        
        show_debug "Fallback server: $BEST_CN ($BEST_IP)"
        
        echo "$tested" > /tmp/servers_tested
        echo "0" > /tmp/servers_responded
        echo "$location_count" > /tmp/location_count
    fi

    # Save latency for metrics
    if [[ "$BEST_TIME" != "timeout" && "$BEST_TIME" =~ ^[0-9]+$ ]]; then
        show_debug "Saving latency metric: ${BEST_TIME}ms"
        echo "$BEST_TIME" > /tmp/server_latency_temp
    fi

    rm -f "$latencies"

    show_debug "Saving server selection to temp files"
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
    
    show_debug "Generating WireGuard config for server: $meta_cn ($endpoint_ip)"
    
    # Allow manual META_CN override
    if [ -n "${META_CN:-}" ]; then
        show_warning "Using manual META_CN override: $META_CN"
        show_debug "META_CN override: $META_CN (was: $meta_cn)"
        meta_cn="$META_CN"
        echo "$META_CN" > /tmp/meta_cn
    fi

    local private_key=$(wg genkey)
    local public_key=$(echo "$private_key" | wg pubkey)
    show_debug "Public key generated: ${public_key:0:20}..."
    
    # Add surgical exemption for addKey registration (port 1337)
    add_temporary_exemption "$endpoint_ip" "1337" "tcp" "pia_addkey"
    
    show_debug "Registering public key with PIA server: https://$meta_cn:1337/addKey"
    local response=$(curl -s -k -G \
        --connect-to "$meta_cn::$endpoint_ip:" \
        --data-urlencode "pt=$token" \
        --data-urlencode "pubkey=$public_key" \
        "https://$meta_cn:1337/addKey")
    
    # Remove exemption immediately
    remove_temporary_exemption "pia_addkey"
    
    show_debug "addKey response received (length: ${#response})"
    [ $_LOG_LEVEL -ge 2 ] && show_debug "Response: $response"
    
    # Parse all JSON fields at once (single jq call)
    local json_data=$(echo "$response" | jq -r '[.status, .server_key, .peer_ip, (.server_port // 1337), (.server_vip // "")] | @tsv')
    local status server_key peer_ip server_port pf_gateway
    read -r status server_key peer_ip server_port pf_gateway <<< "$json_data"
    
    show_debug "Parsed response: status=$status, peer_ip=$peer_ip, server_port=$server_port"
    show_debug "Server public key: ${server_key:0:20}..."
    show_debug "Port forwarding gateway: ${pf_gateway:-none}"
    
    if [ "$status" != "OK" ]; then
        show_error "Server registration failed"
        show_debug "Registration status was '$status', expected 'OK'"
        return 1
    fi
    
    show_debug "Saving client IP: $peer_ip"
    echo "$peer_ip" > /tmp/client_ip
    echo "$pf_gateway" > /tmp/pf_gateway
    
    # Determine DNS
    local dns_config=""
    if [ -n "$DNS" ] && [ "$DNS" != "pia" ] && [ "$DNS" != "none" ]; then
        show_debug "Using custom DNS: $DNS"
        dns_config="DNS = $DNS"
    elif [ "$DNS" != "none" ]; then
        show_debug "Using PIA DNS servers"
        dns_config="DNS = 10.0.0.243, 10.0.0.242"
    else
        show_debug "DNS disabled (DNS=none)"
    fi
    
    local allowed_ips="0.0.0.0/0"
    if [ "${DISABLE_IPV6}" != "true" ]; then
        show_debug "IPv6 enabled, adding ::/0 to allowed IPs"
        allowed_ips="0.0.0.0/0, ::/0"
    else
        show_debug "IPv6 disabled"
    fi
    
    local mtu="${MTU:-1420}"
    show_debug "MTU: $mtu"
    
    show_debug "Writing WireGuard config to /etc/wireguard/pia.conf"
    cat > /etc/wireguard/pia.conf << EOF
[Interface]
PrivateKey = $private_key
Address = $peer_ip/32
${dns_config}
MTU = $mtu

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
    
    show_debug "Parsing WireGuard config: $config"
    
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
    
    if [ $_LOG_LEVEL -ge 2 ]; then
        show_debug "Parsed config values:"
        for key in "${!WG_CONFIG[@]}"; do
            # Don't log private key
            if [ "$key" = "PrivateKey" ]; then
                show_debug "  $key: [REDACTED]"
            else
                show_debug "  $key: ${WG_CONFIG[$key]}"
            fi
        done
    fi
}

# Setup DNS configuration
setup_dns() {
    local dns="$1"
    [ -z "$dns" ] && return 0
    
    show_debug "Setting up DNS: $dns"
    
    if [ ! -f /etc/resolv.conf.bak ]; then
        show_debug "Backing up /etc/resolv.conf"
        cp /etc/resolv.conf /etc/resolv.conf.bak 2>/dev/null || true
    fi
    
    {
        echo "# Set by pia-tun"
        IFS=',' read -ra DNS_SERVERS <<< "$dns"
        for server in "${DNS_SERVERS[@]}"; do
            local trimmed=$(echo "$server" | xargs)
            show_debug "Adding nameserver: $trimmed"
            echo "nameserver $trimmed"
        done
    } > /etc/resolv.conf
    
    show_debug "DNS configuration updated"
}

# Clean up local network routing rules
cleanup_local_exceptions() {
    show_debug "Cleaning up local network routing rules"
    
    local count=0
    # Remove all our custom rules (priorities 100, 200, 300)
    while ip rule del priority 100 2>/dev/null; do count=$((count + 1)); done
    [ $count -gt 0 ] && show_debug "Removed $count rule(s) at priority 100"
    
    count=0
    while ip rule del priority 200 2>/dev/null; do count=$((count + 1)); done
    [ $count -gt 0 ] && show_debug "Removed $count rule(s) at priority 200"
    
    count=0
    while ip rule del priority 300 2>/dev/null; do count=$((count + 1)); done
    [ $count -gt 0 ] && show_debug "Removed $count rule(s) at priority 300"
}

# Bring up WireGuard tunnel
bring_up_wireguard() {
    local config="$1"
    show_debug "Bringing up WireGuard interface: $config"
    
    parse_wg_config "$config"
    
    # Create and configure interface
    show_debug "Creating WireGuard interface: pia"
    ip link add pia type wireguard || { show_debug "Failed to create interface"; return 1; }
    
    show_debug "Setting private key"
    wg set pia private-key <(echo "${WG_CONFIG[PrivateKey]}") || { show_debug "Failed to set private key"; return 1; }
    
    show_debug "Adding address: ${WG_CONFIG[Address]}"
    ip address add "${WG_CONFIG[Address]}" dev pia || { show_debug "Failed to add address"; return 1; }
    
    local mtu="${WG_CONFIG[MTU]:-1392}"
    show_debug "Setting MTU: $mtu"
    ip link set mtu "$mtu" dev pia
    
    # Configure peer
    show_debug "Configuring peer:"
    show_debug "  Endpoint: ${WG_CONFIG[Endpoint]}"
    show_debug "  Allowed IPs: ${WG_CONFIG[AllowedIPs]}"
    show_debug "  Keepalive: ${WG_CONFIG[PersistentKeepalive]}s"
    
    wg set pia \
        peer "${WG_CONFIG[PublicKey]}" \
        endpoint "${WG_CONFIG[Endpoint]}" \
        allowed-ips "${WG_CONFIG[AllowedIPs]}" \
        persistent-keepalive "${WG_CONFIG[PersistentKeepalive]}" || { show_debug "Failed to configure peer"; return 1; }
    
    # Setup routing with fwmark
    show_debug "Setting fwmark: 51820"
    wg set pia fwmark 51820 || { show_debug "Failed to set fwmark"; return 1; }
    
    show_debug "Bringing interface up"
    ip link set pia up || { show_debug "Failed to bring interface up"; return 1; }
    
    # Add VPN routes to separate table
    show_debug "Adding route: 0.0.0.0/0 via pia (table 51820)"
    ip route add 0.0.0.0/0 dev pia table 51820 || { show_debug "Failed to add route"; return 1; }
    
    # CRITICAL: Add local network exceptions BEFORE VPN routing rules
    # This ensures local traffic is evaluated first
    if [ "$LOCAL_NETWORK" = "all" ]; then
        show_debug "Adding local network exceptions for all RFC1918 ranges"
        ip rule add to 10.0.0.0/8 table main priority 100 2>/dev/null || true
        ip rule add to 172.16.0.0/12 table main priority 100 2>/dev/null || true
        ip rule add to 192.168.0.0/16 table main priority 100 2>/dev/null || true
        ip rule add to 169.254.0.0/16 table main priority 100 2>/dev/null || true
    elif [ -n "$LOCAL_NETWORK" ]; then
        show_debug "Adding local network exceptions: $LOCAL_NETWORK"
        IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORK"
        for network in "${NETWORKS[@]}"; do
            network=$(echo "$network" | xargs)
            # Only IPv4 networks
            if [[ "$network" != *":"* ]]; then
                ip rule add to "$network" table main priority 100 2>/dev/null || true
            else
                show_debug "  Skipping IPv6 network: $network"
            fi
        done
    else
        show_debug "No local network exceptions configured"
    fi
    
    # Now add VPN routing rules (priority 200, after local networks)
    show_debug "Adding VPN routing rules (priority 200-300)"
    ip rule add not fwmark 51820 table 51820 priority 200 || { show_debug "Failed to add VPN routing rule"; return 1; }
    ip rule add table main suppress_prefixlength 0 priority 300 || { show_debug "Failed to add suppress rule"; return 1; }
    
    # Setup DNS
    setup_dns "${WG_CONFIG[DNS]}"

    # Debug: Show interface status
    if [ $_LOG_LEVEL -ge 2 ]; then
        show_debug "Interface status:"
        wg show pia 2>/dev/null | while read -r line; do
            show_debug "  $line"
        done
        
        show_debug "Routing rules:"
        ip rule show | grep -E "(priority 100|priority 200|priority 300)" | while read -r line; do
            show_debug "  $line"
        done
    fi
    
    return 0
}

# Teardown WireGuard tunnel
teardown_wireguard() {
    show_debug "Tearing down WireGuard tunnel"
    
    # CRITICAL: Remove VPN from killswitch FIRST
    # This prevents any leak window where interface is down but firewall still references it
    remove_vpn_from_killswitch
    
    # Now safe to tear down interface
    if wg show pia >/dev/null 2>&1; then
        show_debug "Bringing down interface: pia"
        ip link set pia down 2>/dev/null || true
        show_debug "Deleting interface: pia"
        ip link del pia 2>/dev/null || true
    else
        show_debug "Interface pia not found (already down)"
    fi
    
    # Clean up local network exceptions
    cleanup_local_exceptions
    
    show_debug "Teardown complete"
}

#═══════════════════════════════════════════════════════════════════════════════
# MAIN SETUP FUNCTION (Called from run.sh)
#═══════════════════════════════════════════════════════════════════════════════

setup_vpn() {
    local restart="${1:-false}"
    show_debug "setup_vpn called with restart=$restart"
    
    show_step "Authenticating with PIA..."
    local token
    token=$(authenticate) || return 1
    show_success "Authentication successful"
    show_debug "Token length: ${#token}"
    show_info
    
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
    show_debug "Server selection result: $server_name (${latency}ms)"
    
    # Check if we tested multiple locations
    if [ -f /tmp/location_count ] && [ "$(cat /tmp/location_count)" -gt 1 ]; then
        local tested=$(cat /tmp/servers_tested 2>/dev/null || echo "0")
        local responded=$(cat /tmp/servers_responded 2>/dev/null || echo "0")
        local location_name=$(cat /tmp/server_location_name 2>/dev/null || show_info)
        
        [ "$responded" -gt 0 ] && show_success "Tested ${tested} servers, ${responded} responded"
        [ -n "$location_name" ] && show_success "Best server: ${location_name}"
    fi
    
    if [ "$latency" = "timeout" ]; then
        show_warning "Server selected: $server_name (no latency data)"
    else
        show_success "Connected to: ${bold}${server_name}${nc} (${latency}ms)"
        show_info
    fi
    
    if [ "$restart" != "true" ]; then
        show_step "Configuring WireGuard tunnel..."
        generate_config "$token" && show_success "Tunnel configured" || return 1
        show_info
    else
        # Silent configuration during reconnection
        show_debug "Generating config silently (restart=true)"
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
#!/bin/bash
# Unified VPN management - Authentication, server selection, and WireGuard interface
# Uses surgical firewall exemptions for secure PIA API access

set -e
source /app/scripts/ui.sh
source /app/scripts/killswitch.sh

#═══════════════════════════════════════════════════════════════════════════════
# DNS RESOLUTION (Using Quad9)
#═══════════════════════════════════════════════════════════════════════════════

# Resolve hostname using Cloudflare DNS (9.9.9.9, already in bypass routes)
resolve_hostname() {
    local hostname="$1"
    show_debug "Resolving hostname: $hostname"

    # Try 9.9.9.9 (Quad9)
    add_temporary_exemption "9.9.9.9" "853" "tcp" "dns_resolve"
    
    show_debug "Querying 9.9.9.9:853 for $hostname"
    local ip=$(kdig +short +tls-ca +tls-host=dns.quad9.net @9.9.9.9 +time=2 "$hostname" A 2>/dev/null | head -n1)
    remove_temporary_exemption "dns_resolve"
    
    if [[ "$ip" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
        show_success "Resolved $hostname"
        show_debug "Resolved $hostname to $ip via 9.9.9.9"
        echo "$ip"
        return 0
    fi

    show_debug "9.9.9.9 failed, trying fallback 9.9.9.11"

    # Fallback to 9.9.9.11
    add_temporary_exemption "9.9.9.11" "853" "tcp" "dns_resolve"
    
    show_debug "Querying 9.9.9.11:853 for $hostname"
    local ip=$(kdig +short +tls-ca +tls-host=dns11.quad9.net @9.9.9.11 +time=2 "$hostname" A 2>/dev/null | head -n1)
    remove_temporary_exemption "dns_resolve"

    if [[ "$ip" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
        show_success "Resolved $hostname"
        show_debug "Resolved $hostname to $ip via 9.9.9.11"
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

# Cache file paths
CACHE_TOKEN_FILE="/tmp/pia_login_token"
CACHE_TOKEN_TS="/tmp/pia_login_token_ts"
CACHE_PIA_IPS="/tmp/pia_login_ips"
CACHE_SERVERLIST_IPS="/tmp/pia_serverlist_ips"
CACHE_METACN="/tmp/pia_serverlist"

# Maximum token age in seconds (24 hours)
MAX_TOKEN_AGE=86400

# Establish PIA_CN and PIA_IP
PIA_CN=${PIA_CN:-""}
PIA_IP=${PIA_IP:-""}

# Helper: Invalidate token cache (forces fresh fetch on next authenticate)
invalidate_token_cache() {
    show_debug "Invalidating token cache"
    rm -f "$CACHE_TOKEN_FILE" "$CACHE_TOKEN_TS"
}

# Helper: Check if cached token is fresh
is_token_fresh() {
    if [ -f "$CACHE_TOKEN_FILE" ] && [ -f "$CACHE_TOKEN_TS" ]; then
        local token_ts=$(cat "$CACHE_TOKEN_TS" 2>/dev/null || echo "0")
        local current_ts=$(date +%s)
        local token_age=$((current_ts - token_ts))

        if [ $token_age -lt $MAX_TOKEN_AGE ]; then
            local cached_token=$(cat "$CACHE_TOKEN_FILE" 2>/dev/null)
            if [ -n "$cached_token" ]; then
                show_debug "Cached token is fresh (age: ${token_age}s)"
                return 0
            fi
        fi
        show_debug "Cached token is stale (age: ${token_age}s, max: ${MAX_TOKEN_AGE}s)"
    fi
    return 1
}

# Helper: Try to authenticate with a specific IP
# Returns 0 and prints token on success, returns 1 on failure
try_auth_with_ip() {
    local auth_ip="$1"
    local pia_user="$2"
    local pia_pass="$3"

    show_debug "Trying authentication with IP: $auth_ip"

    # Add surgical exemption for authentication
    add_temporary_exemption "$auth_ip" "443" "tcp" "pia_auth"

    # Perform authentication
    local response=$(curl -s --insecure -w "\n%{http_code}" --connect-timeout 10 \
        -u "$pia_user:$pia_pass" \
        --connect-to "www.privateinternetaccess.com::$auth_ip:" \
        "https://www.privateinternetaccess.com/gtoken/generateToken" 2>/dev/null)

    # Remove exemption immediately
    remove_temporary_exemption "pia_auth"

    # Split response into body and HTTP code
    local http_code=$(echo "$response" | tail -1)
    local body=$(echo "$response" | sed '$d')

    show_debug "Auth response from $auth_ip: HTTP $http_code"

    # Check for network errors
    if [ "$http_code" = "000" ]; then
        show_debug "Network error with $auth_ip"
        return 1
    fi

    # Check for auth errors (don't retry these - credentials are wrong)
    if [ "$http_code" = "401" ] || [ "$http_code" = "403" ]; then
        show_error "Authentication failed: Invalid username or password"
        # Return special code to indicate auth failure (don't try more IPs)
        return 2
    fi

    if [ "$http_code" != "200" ]; then
        show_debug "HTTP error $http_code from $auth_ip"
        return 1
    fi

    # Check if we got valid JSON
    if ! echo "$body" | jq -e . >/dev/null 2>&1; then
        show_debug "Invalid JSON from $auth_ip"
        return 1
    fi

    # Extract token
    local token=$(echo "$body" | jq -r '.token // empty')

    if [ -z "$token" ]; then
        local error_msg=$(echo "$body" | jq -r '.message // .error // empty')
        show_debug "No token from $auth_ip: $error_msg"

        # Check for credential errors
        if [ "$error_msg" = "authentication failed" ]; then
            show_error "Authentication failed: Invalid username or password"
            return 2
        fi
        return 1
    fi

    # Success - save and return token
    show_debug "Token received from $auth_ip (length: ${#token})"
    echo "$token" > "$CACHE_TOKEN_FILE"
    chmod 600 "$CACHE_TOKEN_FILE"
    echo "$(date +%s)" > "$CACHE_TOKEN_TS"
    echo "$token"
    return 0
}

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

    # Step 1: Check if we have a fresh cached token
    if is_token_fresh; then
        local cached_token=$(cat "$CACHE_TOKEN_FILE")
        show_success "Using cached login token"
        echo "$cached_token"
        return 0
    fi

    # Step 2: Try cached PIA IPs
    if [ -f "$CACHE_PIA_IPS" ]; then
        show_debug "Trying cached PIA IPs..."
        while IFS= read -r cached_ip; do
            [ -z "$cached_ip" ] && continue

            local token
            token=$(try_auth_with_ip "$cached_ip" "$pia_user" "$pia_pass")
            local result=$?

            if [ $result -eq 0 ]; then
                show_success "Authentication succeeded with cached IP: $cached_ip"
                show_success "Retrieved login token"
                echo "$token"
                return 0
            elif [ $result -eq 2 ]; then
                # Credential error - don't try more IPs
                return 1
            fi
            # Otherwise try next IP
        done < "$CACHE_PIA_IPS"
        show_debug "All cached PIA IPs exhausted"
    fi

    # Step 3: Fall back to DNS resolution
    show_debug "Escalating to DNS resolution for www.privateinternetaccess.com"
    local auth_ip=$(resolve_hostname "www.privateinternetaccess.com")
    if [ -z "$auth_ip" ]; then
        show_error "Cannot resolve privateinternetaccess.com"
        return 1
    fi

    local token
    token=$(try_auth_with_ip "$auth_ip" "$pia_user" "$pia_pass")
    local result=$?

    if [ $result -eq 0 ]; then
        show_success "Retrieved login token"
        echo "$token"
        return 0
    fi

    show_error "Authentication failed after all attempts"
    return 1
}

# Helper: Try to fetch server list from a specific IP
# Returns 0 and sets all_regions variable on success
try_fetch_serverlist_with_ip() {
    local serverlist_ip="$1"

    show_debug "Trying to fetch server list from IP: $serverlist_ip"

    # Add surgical exemption for server list fetch
    add_temporary_exemption "$serverlist_ip" "443" "tcp" "pia_serverlist"

    local response=$(curl -s --connect-timeout 10 \
        --connect-to "serverlist.piaservers.net::$serverlist_ip:" \
        'https://serverlist.piaservers.net/vpninfo/servers/v6' 2>/dev/null | head -1)

    # Remove exemption immediately
    remove_temporary_exemption "pia_serverlist"

    show_debug "Server list response size: ${#response} bytes"

    if [ ${#response} -lt 1000 ]; then
        show_debug "Server list too small from $serverlist_ip (< 1000 bytes)"
        return 1
    fi

    # Set the global variable
    all_regions="$response"
    return 0
}

# Helper: Get candidates from cached metacn_cache
# Populates all_candidates file with servers matching locations and PF requirements
get_candidates_from_cache() {
    local locations="$1"
    local pf_required="$2"
    local all_candidates="$3"

    if [ ! -f "$CACHE_METACN" ]; then
        show_debug "No cached server list available"
        return 1
    fi

    show_debug "Using cached server list from $CACHE_METACN"

    local found_any=false

    for location in $locations; do
        show_debug "Looking for cached servers in: $location"

        # Filter by region and optionally by port forwarding
        local filter=".[] | select(.region == \"$location\")"
        if [ "$pf_required" = "true" ]; then
            filter="$filter | select(.pf == true)"
        fi

        local servers=$(jq -r "$filter | \"\(.ip) \(.cn) \(.region) \(.region)\"" "$CACHE_METACN" 2>/dev/null)

        if [ -n "$servers" ]; then
            echo "$servers" >> "$all_candidates"
            found_any=true
            local count=$(echo "$servers" | wc -l)
            show_debug "Found $count cached server(s) for $location"
        else
            show_debug "No cached servers for $location (pf_required=$pf_required)"
        fi
    done

    if [ "$found_any" = "true" ]; then
        return 0
    fi
    return 1
}

# Helper: Parse fresh server list response into candidates
# Populates all_candidates file
parse_serverlist_to_candidates() {
    local all_regions="$1"
    local locations="$2"
    local pf_required="$3"
    local all_candidates="$4"

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
        if [ "$pf_required" = "true" ]; then
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
}

# Helper: Test latency and select best server from candidates
# Sets BEST_* variables and returns result
test_and_select_server() {
    local all_candidates="$1"
    local max_latency="$2"
    local location_count="$3"

    local candidate_count=$(wc -l < "$all_candidates" 2>/dev/null || echo "0")
    show_debug "Total candidates collected: $candidate_count"

    if [ ! -s "$all_candidates" ]; then
        show_debug "No valid servers found after filtering"
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
        show_debug "All servers timed out, using fallback from candidates"
        # Fallback to first server if all timeout
        read -r ip cn location_id location_name <<< "$(head -1 "$all_candidates" 2>/dev/null)"
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

    rm -f "$latencies"

    # Save latency for metrics
    if [[ "$BEST_TIME" != "timeout" && "$BEST_TIME" =~ ^[0-9]+$ ]]; then
        show_debug "Saving latency metric: ${BEST_TIME}ms"
        echo "$BEST_TIME" > /tmp/server_latency_temp
    fi

    # Save server selection to temp files
    show_debug "Saving server selection to temp files"
    echo "$BEST_IP" > /tmp/server_endpoint
    echo "$BEST_CN" > /tmp/pia_cn
    echo "$BEST_LOCATION_ID" > /tmp/server_location
    echo "$BEST_LOCATION_NAME" > /tmp/server_location_name

    return 0
}

find_server() {
    local max_latency="${MAX_LATENCY:-1}"
    show_debug "Finding server with max_latency=${max_latency}s"

    # Parse locations (comma or space separated)
    local locations="${PIA_LOCATION}"
    locations=$(echo "$locations" | tr ',' ' ')
    local location_count=$(echo "$locations" | wc -w)
    show_debug "Processing $location_count location(s): $locations"

    local pf_required="false"
    [ "$PF_ENABLED" = "true" ] && pf_required="true"
    show_debug "Port forwarding required: $pf_required"

    local all_regions=""
    local all_candidates=$(mktemp)
    local got_fresh_list=false

    # Step 1: Try cached serverlist IPs to get fresh server list
    if [ -f "$CACHE_SERVERLIST_IPS" ]; then
        show_debug "Trying cached serverlist IPs..."
        while IFS= read -r cached_ip; do
            [ -z "$cached_ip" ] && continue

            if try_fetch_serverlist_with_ip "$cached_ip"; then
                show_success "Retrieved server list from cached IP: $cached_ip"
                got_fresh_list=true
                break
            fi
        done < "$CACHE_SERVERLIST_IPS"
    fi

    # Step 2: If we have a fresh list, parse it
    if [ "$got_fresh_list" = "true" ]; then
        parse_serverlist_to_candidates "$all_regions" "$locations" "$pf_required" "$all_candidates"
    fi

    # Step 3: If no fresh list or no candidates, try cached metacn_cache
    if [ ! -s "$all_candidates" ]; then
        show_debug "No candidates from fresh list, trying cached servers..."
        if get_candidates_from_cache "$locations" "$pf_required" "$all_candidates"; then
            show_success "Using cached server candidates"
        fi
    fi

    # Step 4: If still no candidates, escalate to DNS resolution
    if [ ! -s "$all_candidates" ]; then
        show_debug "Escalating to DNS resolution for serverlist.piaservers.net"
        local serverlist_ip=$(resolve_hostname "serverlist.piaservers.net")
        if [ -z "$serverlist_ip" ]; then
            rm -f "$all_candidates"
            show_error "Cannot resolve serverlist.piaservers.net"
            echo "error|No valid servers found"
            return 1
        fi

        if try_fetch_serverlist_with_ip "$serverlist_ip"; then
            parse_serverlist_to_candidates "$all_regions" "$locations" "$pf_required" "$all_candidates"
        else
            rm -f "$all_candidates"
            show_error "Could not fetch server list"
            echo "error|No valid servers found"
            return 1
        fi
    fi

    # Step 5: Test latency and select best server
    if ! test_and_select_server "$all_candidates" "$max_latency" "$location_count"; then
        rm -f "$all_candidates"
        echo "error|No valid servers found"
        return 1
    fi

    rm -f "$all_candidates"

    # Return simple info for display (just server name and latency)
    echo "$BEST_CN|$BEST_TIME"
}

generate_config() {
    if [ -n "${PIA_CN:-}" ] && [ -n "${PIA_IP:-}" ]; then
        echo $PIA_CN > /tmp/pia_cn
        echo $PIA_IP > /tmp/server_endpoint
    fi
    local token="$1"
    local endpoint_ip=$(cat /tmp/server_endpoint)
    local pia_cn=$(cat /tmp/pia_cn)
    
    show_debug "Generating WireGuard config for server: $pia_cn ($endpoint_ip)"
    
    # Allow manual PIA_CN override
    if [ -n "${PIA_CN:-}" ]; then
        show_warning "Using manual PIA_CN override: $PIA_CN"
        show_debug "PIA_CN override: $PIA_CN"
        pia_cn="$PIA_CN"
        echo "$PIA_CN" > /tmp/pia_cn
    fi

    local private_key=$(wg genkey)
    local public_key=$(echo "$private_key" | wg pubkey)
    show_debug "Public key generated: ${public_key:0:20}..."
    
    # Add surgical exemption for addKey registration (port 1337)
    add_temporary_exemption "$endpoint_ip" "1337" "tcp" "pia_addkey"
    
    show_debug "Registering public key with PIA server: https://$pia_cn:1337/addKey"
    local response=$(curl -s -k -G \
        --connect-to "$pia_cn::$endpoint_ip:" \
        --data-urlencode "pt=$token" \
        --data-urlencode "pubkey=$public_key" \
        "https://$pia_cn:1337/addKey")
    
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
        show_info "    Cached login token invalid?"
        show_debug "Registration status was '$status', expected 'OK'"
        # Token might be invalid - clear cache so next attempt gets fresh token
        invalidate_token_cache
        return 1
    else
        show_success "Login token accepted"
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
    if [ "${IPV6_ENABLED}" != "false" ]; then
        show_debug "IPv6 enabled, adding ::/0 to allowed IPs"
        allowed_ips="0.0.0.0/0, ::/0"
    else
        show_debug "IPv6 disabled"
    fi
    
    local mtu="${MTU:-1420}"
    show_debug "MTU: $mtu"
    
    show_debug "Writing WireGuard config to /etc/wireguard/pia0.conf"
    cat > /etc/wireguard/pia0.conf << EOF
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
    ip link delete pia0 2>/dev/null || true

    parse_wg_config "$config"
    
    # Create and configure interface
    # Try kernel WireGuard first, fall back to wireguard-go (userspace) if unavailable
    show_debug "Creating WireGuard interface: pia0"

    # Check for manual override
    local use_kernel=true
    if [ "${WG_BACKEND:-}" = "userspace" ]; then
        show_debug "WG_BACKEND=userspace, skipping kernel WireGuard"
        use_kernel=false
    elif [ "${WG_BACKEND:-}" = "kernel" ]; then
        # Explicit kernel request - try it, fail if unavailable
        if ip link add pia0 type wireguard 2>/dev/null; then
            show_debug "WG_BACKEND=kernel, using kernel WireGuard"
            echo "kernel" > /tmp/wg_mode
        else
            show_error "WG_BACKEND=kernel requested but kernel WireGuard unavailable"
            return 1
        fi
    elif ip link add pia0 type wireguard 2>/dev/null; then
        show_debug "Using kernel WireGuard"
        echo "kernel" > /tmp/wg_mode
    else
        use_kernel=false
    fi

    if [ "$use_kernel" = "false" ]; then
        show_warning "Kernel WireGuard unavailable, using wireguard-go (userspace)"

        # Ensure TUN device exists (required for userspace WireGuard)
        if [ ! -e /dev/net/tun ]; then
            show_debug "Creating /dev/net/tun device"
            mkdir -p /dev/net
            mknod /dev/net/tun c 10 200 2>/dev/null || {
                show_error "Failed to create /dev/net/tun - try adding '--device /dev/net/tun:/dev/net/tun' to docker run"
                return 1
            }
            chmod 600 /dev/net/tun
        fi

        # Start wireguard-go in background (it may run in foreground mode)
        # Cannot use command substitution as it waits for process to exit
        show_debug "Starting wireguard-go daemon"
        wireguard-go pia0 > /tmp/wireguard-go.log 2>&1 &
        local wg_pid=$!

        # Wait for interface to appear (up to 3 seconds)
        local waited=0
        while [ $waited -lt 30 ]; do
            if ip link show pia0 >/dev/null 2>&1; then
                show_debug "wireguard-go created interface (waited ${waited}00ms)"
                break
            fi
            sleep 0.1
            waited=$((waited + 1))
        done

        # Check if interface was created
        if ! ip link show pia0 >/dev/null 2>&1; then
            # Interface not created - check what went wrong
            sleep 0.5  # Give wireguard-go time to write error
            local wg_output=$(cat /tmp/wireguard-go.log 2>/dev/null || echo "no output")

            if echo "$wg_output" | grep -q "no such device"; then
                show_error "TUN kernel module not loaded - run 'sudo modprobe tun' on host"
            elif echo "$wg_output" | grep -q "operation not permitted"; then
                show_error "Permission denied creating TUN device - check capabilities"
            else
                show_error "Failed to create WireGuard interface"
                show_debug "wireguard-go output: $wg_output"
            fi
            return 1
        fi

        echo "userspace" > /tmp/wg_mode
    fi
    
    show_debug "Setting private key"
    wg set pia0 private-key <(echo "${WG_CONFIG[PrivateKey]}") || { show_debug "Failed to set private key"; return 1; }

    show_debug "Adding address: ${WG_CONFIG[Address]}"
    ip address add "${WG_CONFIG[Address]}" dev pia0 || { show_debug "Failed to add address"; return 1; }

    local mtu="${WG_CONFIG[MTU]:-1392}"
    show_debug "Setting MTU: $mtu"
    ip link set mtu "$mtu" dev pia0

    # CRITICAL: Set fwmark BEFORE configuring peer
    # When peer is configured with persistent-keepalive, WireGuard immediately sends handshakes
    # Without fwmark, these packets would be routed via table 51820 (VPN table) and fail
    show_debug "Setting fwmark: 51820"
    wg set pia0 fwmark 51820 || { show_debug "Failed to set fwmark"; return 1; }

    # Configure peer (this triggers handshake initiation with keepalive)
    show_debug "Configuring peer:"
    show_debug "  Endpoint: ${WG_CONFIG[Endpoint]}"
    show_debug "  Allowed IPs: ${WG_CONFIG[AllowedIPs]}"
    show_debug "  Keepalive: ${WG_CONFIG[PersistentKeepalive]}s"

    wg set pia0 \
        peer "${WG_CONFIG[PublicKey]}" \
        endpoint "${WG_CONFIG[Endpoint]}" \
        allowed-ips "${WG_CONFIG[AllowedIPs]}" \
        persistent-keepalive "${WG_CONFIG[PersistentKeepalive]}" || { show_debug "Failed to configure peer"; return 1; }
    
    show_debug "Bringing interface up"
    ip link set pia0 up || { show_debug "Failed to bring interface up"; return 1; }
    
    # Add VPN routes to separate table
    show_debug "Adding route: 0.0.0.0/0 via pia0 (table 51820)"
    ip route add 0.0.0.0/0 dev pia0 table 51820 || { show_debug "Failed to add route"; return 1; }
    
    # CRITICAL: Add local network exceptions BEFORE VPN routing rules
    # This ensures local traffic is evaluated first
    if [ -n "$LOCAL_NETWORKS" ]; then
        show_debug "Adding local network exceptions: $LOCAL_NETWORKS"
        IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORKS"
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
        wg show pia0 2>/dev/null | while read -r line; do
            show_debug "  $line"
        done
        
        show_debug "Routing rules:"
        ip rule show | grep -E "(priority 100|priority 200|priority 300)" | while read -r line; do
            show_debug "  $line"
        done || true
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
    if wg show pia0 >/dev/null 2>&1; then
        show_debug "Bringing down interface: pia0"
        ip link set pia0 down 2>/dev/null || true
        show_debug "Deleting interface: pia0"
        ip link del pia0 2>/dev/null || true
    else
        show_debug "Interface pia0 not found (already down)"
    fi

    # Clean up wireguard-go process if running (userspace mode)
    pkill -f "wireguard-go pia0" 2>/dev/null || true
    rm -f /var/run/wireguard/pia0.sock 2>/dev/null || true

    # Clean up local network exceptions
    cleanup_local_exceptions

    show_debug "Teardown complete"
}

#═══════════════════════════════════════════════════════════════════════════════
# MAIN SETUP FUNCTION (Called from run.sh)
#═══════════════════════════════════════════════════════════════════════════════

setup_vpn() {
    show_step "Authenticating with PIA..."
    local token
    token=$(authenticate) || return 1
    show_debug "Token length: ${#token}"
    
    if [ -z "$PIA_CN" ] && [ -z "$PIA_IP" ]; then
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
        IFS='|' read -r -t 3 server_name latency <<< "$server_info"
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
            show_success "Connecting to: ${bold}${server_name}${nc} (${latency}ms)"
        fi
    fi

    show_step "Configuring WireGuard tunnel..."
    generate_config "$token" && show_success "Tunnel configured" || return 1
}

# Only run setup_vpn if executed directly (not sourced)
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    setup_vpn "$@"
fi

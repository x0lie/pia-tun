#!/bin/bash

# Port forwarding API updater for various torrent clients
# Supports: qBittorrent, Transmission, Deluge, rTorrent

source /app/scripts/ui.sh

# Environment variables for API configuration
PORT_API_ENABLED=${PORT_API_ENABLED:-false}
PORT_API_TYPE=${PORT_API_TYPE:-""}           # qbittorrent, transmission, deluge, rtorrent
PORT_API_URL=${PORT_API_URL:-""}             # e.g., http://qbittorrent:8080
PORT_API_USER=${PORT_API_USER:-""}
PORT_API_PASS=${PORT_API_PASS:-""}

# Update qBittorrent port via API
update_qbittorrent() {
    local port=$1
    local base_url=$2
    local username=$3
    local password=$4
    
    # Login and get cookie
    local cookie=$(curl -s -i --connect-timeout 5 --max-time 10 --data "username=$username&password=$password" \
        "$base_url/api/v2/auth/login" 2>/dev/null | grep -i "set-cookie" | cut -d' ' -f2 | cut -d';' -f1)
    
    if [ -z "$cookie" ]; then
        return 1
    fi
    
    # Update listen port
    curl -s --connect-timeout 5 --max-time 10 -b "$cookie" --data "json={\"listen_port\":$port}" \
        "$base_url/api/v2/app/setPreferences" >/dev/null 2>&1 || return 1
    
    # Verify the change
    local current_port=$(curl -s --connect-timeout 5 --max-time 10 -b "$cookie" "$base_url/api/v2/app/preferences" 2>/dev/null | \
        grep -o '"listen_port":[0-9]*' | cut -d':' -f2)
    
    [ "$current_port" = "$port" ] && return 0 || return 1
}

# Update Transmission port via RPC
update_transmission() {
    local port=$1
    local base_url=$2
    local username=$3
    local password=$4
    
    # Get session ID
    local session_id=$(curl -s --connect-timeout 5 --max-time 10 -u "$username:$password" "$base_url/transmission/rpc" 2>/dev/null | \
        grep -o 'X-Transmission-Session-Id: [^<]*' | cut -d' ' -f2)
    
    [ -z "$session_id" ] && return 1
    
    # Update port
    local response=$(curl -s --connect-timeout 5 --max-time 10 -u "$username:$password" \
        -H "X-Transmission-Session-Id: $session_id" \
        "$base_url/transmission/rpc" \
        -d "{\"method\":\"session-set\",\"arguments\":{\"peer-port\":$port}}" 2>/dev/null)
    
    echo "$response" | grep -q '"result":"success"' && return 0 || return 1
}

# Update Deluge port via JSON-RPC
update_deluge() {
    local port=$1
    local base_url=$2
    local password=$3
    
    # Login to get session cookie
    local cookie_jar=$(mktemp)
    local login_response=$(curl -s --connect-timeout 5 --max-time 10 -c "$cookie_jar" \
        -d "{\"method\":\"auth.login\",\"params\":[\"$password\"],\"id\":1}" \
        "$base_url/json" 2>/dev/null)
    
    if ! echo "$login_response" | grep -q '"result":true'; then
        rm -f "$cookie_jar"
        return 1
    fi
    
    # Update listen ports
    local response=$(curl -s --connect-timeout 5 --max-time 10 -b "$cookie_jar" \
        -d "{\"method\":\"core.set_config\",\"params\":[{\"listen_ports\":[$port,$port]}],\"id\":2}" \
        "$base_url/json" 2>/dev/null)
    
    rm -f "$cookie_jar"
    
    echo "$response" | grep -q '"error":null' && return 0 || return 1
}

# Update rTorrent port via XMLRPC
update_rtorrent() {
    local port=$1
    local base_url=$2
    
    # rTorrent uses XML-RPC
    local response=$(curl -s --connect-timeout 5 --max-time 10 "$base_url" \
        -d "<?xml version='1.0'?><methodCall><methodName>network.port_range.set</methodName><params><param><value><string>$port-$port</string></value></param></params></methodCall>" 2>/dev/null)
    
    echo "$response" | grep -q '<methodResponse>' && return 0 || return 1
}

# Generic update function that routes to specific implementation
update_port_api() {
    local port=$1
    
    [ "$PORT_API_ENABLED" != "true" ] && return 0
    [ -z "$PORT_API_TYPE" ] && return 1
    [ -z "$PORT_API_URL" ] && return 1
    
    # Remove old marker
    rm -f /tmp/port_api_success
    
    local result=1
    case "$PORT_API_TYPE" in
        qbittorrent|qbit|qb)
            update_qbittorrent "$port" "$PORT_API_URL" "$PORT_API_USER" "$PORT_API_PASS"
            result=$?
            ;;
        transmission|trans)
            update_transmission "$port" "$PORT_API_URL" "$PORT_API_USER" "$PORT_API_PASS"
            result=$?
            ;;
        deluge)
            update_deluge "$port" "$PORT_API_URL" "$PORT_API_PASS"
            result=$?
            ;;
        rtorrent|rutorrent)
            update_rtorrent "$port" "$PORT_API_URL"
            result=$?
            ;;
        *)
            return 1
            ;;
    esac
    
    # Mark success for display purposes
    [ $result -eq 0 ] && touch /tmp/port_api_success
    
    return $result
}

# Test API connectivity (removed - no longer used)
test_api_connection() {
    return 0
}

# Export function for use in other scripts
export -f update_port_api

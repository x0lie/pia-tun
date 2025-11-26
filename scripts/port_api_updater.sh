#!/bin/bash
# Port forwarding API updater
# Supports: qBittorrent, Transmission, Deluge, rTorrent
# Performs quick retries for transient failures; caller handles long-term retry strategy

source /app/scripts/ui.sh

# Configuration
readonly PORT_API_ENABLED=${PORT_API_ENABLED:-false}
readonly PORT_API_TYPE=${PORT_API_TYPE:-""}
readonly PORT_API_URL=${PORT_API_URL:-""}
readonly PORT_API_USER=${PORT_API_USER:-""}
readonly PORT_API_PASS=${PORT_API_PASS:-""}
readonly CURL_TIMEOUT="--connect-timeout 5 --max-time 10"

show_debug "Port API updater configuration:"
show_debug "  PORT_API_ENABLED=$PORT_API_ENABLED"
show_debug "  PORT_API_TYPE=$PORT_API_TYPE"
show_debug "  PORT_API_URL=$PORT_API_URL"
show_debug "  PORT_API_USER=${PORT_API_USER:+set}"
show_debug "  PORT_API_PASS=${PORT_API_PASS:+set}"

# Update qBittorrent port via API
update_qbittorrent() {
    local port=$1 base_url=$2 username=$3 password=$4

    show_debug "update_qbittorrent: port=$port, url=$base_url, user=${username:+set}"
    
    # Login and get cookie
    show_debug "Attempting qBittorrent login to $base_url/api/v2/auth/login"
    local cookie=$(curl -s -i $CURL_TIMEOUT \
        --data "username=$username&password=$password" \
        "$base_url/api/v2/auth/login" 2>/dev/null | \
        grep -i "set-cookie" | cut -d' ' -f2 | cut -d';' -f1)

    if [ -z "$cookie" ]; then
        show_debug "qBittorrent login failed (no cookie received)"
        return 1
    fi
    
    show_debug "qBittorrent login successful, cookie: ${cookie:0:20}..."

    # Update listen port
    show_debug "Setting qBittorrent listen port to $port"
    if ! curl -s $CURL_TIMEOUT -b "$cookie" \
        --data "json={\"listen_port\":$port}" \
        "$base_url/api/v2/app/setPreferences" >/dev/null 2>&1; then
        show_debug "Failed to set qBittorrent port (curl error)"
        return 1
    fi
    
    show_debug "Port set command sent successfully"

    # Verify the change
    show_debug "Verifying port change..."
    local current_port=$(curl -s $CURL_TIMEOUT -b "$cookie" \
        "$base_url/api/v2/app/preferences" 2>/dev/null | \
        grep -o '"listen_port":[0-9]*' | cut -d':' -f2)

    show_debug "Current port from qBittorrent: ${current_port:-none}"
    
    if [ "$current_port" = "$port" ]; then
        show_debug "Port verification successful"
        return 0
    else
        show_debug "Port verification failed (expected: $port, got: ${current_port:-none})"
        return 1
    fi
}

# Update Transmission port via RPC
update_transmission() {
    local port=$1 base_url=$2 username=$3 password=$4

    show_debug "update_transmission: port=$port, url=$base_url, user=${username:+set}"
    
    # Get session ID
    show_debug "Getting Transmission session ID from $base_url/transmission/rpc"
    local session_id=$(curl -s $CURL_TIMEOUT -u "$username:$password" \
        "$base_url/transmission/rpc" 2>/dev/null | \
        grep -o 'X-Transmission-Session-Id: [^<]*' | cut -d' ' -f2)

    if [ -z "$session_id" ]; then
        show_debug "Failed to get Transmission session ID"
        return 1
    fi
    
    show_debug "Session ID obtained: $session_id"

    # Update port
    show_debug "Setting Transmission peer port to $port"
    local response=$(curl -s $CURL_TIMEOUT -u "$username:$password" \
        -H "X-Transmission-Session-Id: $session_id" \
        "$base_url/transmission/rpc" \
        -d "{\"method\":\"session-set\",\"arguments\":{\"peer-port\":$port}}" \
        2>/dev/null)

    show_debug "Transmission response: ${response:0:100}..."
    
    if echo "$response" | grep -q '"result":"success"'; then
        show_debug "Transmission port update successful"
        return 0
    else
        show_debug "Transmission port update failed (no success in response)"
        return 1
    fi
}

# Update Deluge port via JSON-RPC
update_deluge() {
    local port=$1 base_url=$2 password=$3
    local cookie_jar=$(mktemp)

    show_debug "update_deluge: port=$port, url=$base_url, password=${password:+set}"
    show_debug "Created temp cookie jar: $cookie_jar"
    
    # Login
    show_debug "Attempting Deluge login to $base_url/json"
    local login_response=$(curl -s $CURL_TIMEOUT -c "$cookie_jar" \
        -d "{\"method\":\"auth.login\",\"params\":[\"$password\"],\"id\":1}" \
        "$base_url/json" 2>/dev/null)

    show_debug "Deluge login response: ${login_response:0:100}..."

    if ! echo "$login_response" | grep -q '"result":true'; then
        show_debug "Deluge login failed"
        rm -f "$cookie_jar"
        return 1
    fi
    
    show_debug "Deluge login successful"

    # Update listen ports
    show_debug "Setting Deluge listen ports to $port-$port"
    local response=$(curl -s $CURL_TIMEOUT -b "$cookie_jar" \
        -d "{\"method\":\"core.set_config\",\"params\":[{\"listen_ports\":[$port,$port]}],\"id\":2}" \
        "$base_url/json" 2>/dev/null)

    show_debug "Deluge set_config response: ${response:0:100}..."
    
    rm -f "$cookie_jar"
    show_debug "Cleaned up cookie jar"
    
    if echo "$response" | grep -q '"error":null'; then
        show_debug "Deluge port update successful"
        return 0
    else
        show_debug "Deluge port update failed (error in response)"
        return 1
    fi
}

# Update rTorrent port via XMLRPC
update_rtorrent() {
    local port=$1 base_url=$2

    show_debug "update_rtorrent: port=$port, url=$base_url"
    show_debug "Setting rTorrent port range to $port-$port"
    
    local response=$(curl -s $CURL_TIMEOUT "$base_url" \
        -d "<?xml version='1.0'?><methodCall><methodName>network.port_range.set</methodName><params><param><value><string>$port-$port</string></value></param></params></methodCall>" \
        2>/dev/null)

    show_debug "rTorrent response: ${response:0:100}..."
    
    if echo "$response" | grep -q '<methodResponse>'; then
        show_debug "rTorrent port update successful"
        return 0
    else
        show_debug "rTorrent port update failed (no methodResponse)"
        return 1
    fi
}

# Internal function to perform a single update attempt
_update_port_api_attempt() {
    local port=$1
    local result=1

    case "$PORT_API_TYPE" in
        qbittorrent|qbit|qb)
            show_debug "Using qBittorrent updater"
            update_qbittorrent "$port" "$PORT_API_URL" "$PORT_API_USER" "$PORT_API_PASS"
            result=$?
            ;;
        transmission|trans)
            show_debug "Using Transmission updater"
            update_transmission "$port" "$PORT_API_URL" "$PORT_API_USER" "$PORT_API_PASS"
            result=$?
            ;;
        deluge)
            show_debug "Using Deluge updater"
            update_deluge "$port" "$PORT_API_URL" "$PORT_API_PASS"
            result=$?
            ;;
        rtorrent|rutorrent)
            show_debug "Using rTorrent updater"
            update_rtorrent "$port" "$PORT_API_URL"
            result=$?
            ;;
        custom)
            show_debug "Using custom command"
            if [ -z "$PORT_API_CMD" ]; then
                show_debug "PORT_API_CMD not set for custom type"
                return 1
            fi

            # Replace {PORT} placeholder in custom command
            local cmd="${PORT_API_CMD//\{PORT\}/$port}"
            show_debug "Executing custom command: $cmd"
            eval "$cmd" >/dev/null 2>&1
            result=$?
            show_debug "Custom command exit code: $result"
            ;;
        *)
            show_debug "Unknown PORT_API_TYPE: $PORT_API_TYPE"
            return 1
            ;;
    esac

    return $result
}

# Main update function with short retry for transient failures
# Returns 0 on success, 1 on failure (caller should handle long-term retry)
update_port_api() {
    local port=$1

    show_debug "update_port_api called with port=$port"

    # Quick validation
    if [ "$PORT_API_ENABLED" != "true" ]; then
        show_debug "PORT_API_ENABLED is not true, skipping update"
        return 0
    fi

    if [ -z "$PORT_API_TYPE" ] || [ -z "$PORT_API_URL" ]; then
        show_debug "PORT_API_TYPE or PORT_API_URL not set, cannot update"
        return 1
    fi

    # Try 3 times with short delays for transient network failures
    local max_attempts=3
    for attempt in $(seq 1 $max_attempts); do
        show_debug "Update attempt #$attempt (port=$port)"

        # Attempt update
        if _update_port_api_attempt "$port"; then
            show_debug "Update successful on attempt #$attempt"
            return 0
        fi

        # Failed - wait before retry (unless last attempt)
        if [ $attempt -lt $max_attempts ]; then
            show_debug "Update failed on attempt #$attempt, retrying in 2s"
            sleep 2
        else
            show_debug "Update failed on attempt #$attempt (final attempt)"
        fi
    done

    # All attempts failed - caller should handle long-term retry
    show_debug "All $max_attempts attempts failed, returning error"
    return 1
}

# Export for use in other scripts
export -f update_port_api
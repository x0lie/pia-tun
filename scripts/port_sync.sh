#!/bin/bash
# Port forwarding API updater
# Supports: qBittorrent, Transmission, Deluge, rTorrent
# Performs quick retries for transient failures; caller handles long-term retry strategy

source /app/scripts/ui.sh

# Configuration
readonly PORT_SYNC_ENABLED=${PORT_SYNC_ENABLED:-false}
readonly PORT_SYNC_CLIENT=${PORT_SYNC_CLIENT:-""}
readonly PORT_SYNC_URL=${PORT_SYNC_URL:-""}
readonly PORT_SYNC_USER=${PORT_SYNC_USER:-""}
readonly PORT_SYNC_PASS=${PORT_SYNC_PASS:-""}
readonly CURL_TIMEOUT="--connect-timeout 5 --max-time 10"

show_debug "Port API updater configuration:"
show_debug "  PORT_SYNC_ENABLED=$PORT_SYNC_ENABLED"
show_debug "  PORT_SYNC_CLIENT=$PORT_SYNC_CLIENT"
show_debug "  PORT_SYNC_URL=$PORT_SYNC_URL"
show_debug "  PORT_SYNC_USER=${PORT_SYNC_USER:+set}"
show_debug "  PORT_SYNC_PASS=${PORT_SYNC_PASS:+set}"
show_debug "  PORT_SYNC_CMD=${PORT_SYNC_CMD:+set}"

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

    # Ensure cleanup on any exit (including errors with set -e)
    trap "rm -f '$cookie_jar'" RETURN

    show_debug "update_deluge: port=$port, url=$base_url, password=${password:+set}"
    show_debug "Created temp cookie jar: $cookie_jar"

    # Login
    show_debug "Attempting Deluge login to $base_url/json"
    local login_response=$(curl -s $CURL_TIMEOUT -c "$cookie_jar" \
        -H "Content-Type: application/json" \
        -d "{\"method\":\"auth.login\",\"params\":[\"$password\"],\"id\":1}" \
        "$base_url/json" 2>/dev/null)

    show_debug "Deluge login response: ${login_response:0:100}..."

    if ! echo "$login_response" | grep -q '"result": *true'; then
        show_debug "Deluge login failed"
        rm -f "$cookie_jar"
        return 1
    fi

    show_debug "Deluge login successful"

    # Check if web UI is connected to daemon
    show_debug "Checking if web UI is connected to daemon"
    local connected=$(curl -s $CURL_TIMEOUT -b "$cookie_jar" \
        -H "Content-Type: application/json" \
        -d "{\"method\":\"web.connected\",\"params\":[],\"id\":2}" \
        "$base_url/json" 2>/dev/null)

    show_debug "Connection status: ${connected:0:100}..."

    # If not connected, connect to the first available host
    if echo "$connected" | grep -q '"result": *false'; then
        show_debug "Web UI not connected to daemon, attempting to connect"

        # Get available hosts
        local hosts=$(curl -s $CURL_TIMEOUT -b "$cookie_jar" \
            -H "Content-Type: application/json" \
            -d "{\"method\":\"web.get_hosts\",\"params\":[],\"id\":3}" \
            "$base_url/json" 2>/dev/null)

        show_debug "Available hosts: ${hosts:0:100}..."

        # Extract first host_id (32-character hex string)
        local host_id=$(echo "$hosts" | grep -o '"[a-f0-9]\{32\}"' | head -1 | tr -d '"')

        if [ -n "$host_id" ]; then
            show_debug "Connecting to daemon host: $host_id"
            local connect_response=$(curl -s $CURL_TIMEOUT -b "$cookie_jar" \
                -H "Content-Type: application/json" \
                -d "{\"method\":\"web.connect\",\"params\":[\"$host_id\"],\"id\":4}" \
                "$base_url/json" 2>/dev/null)

            show_debug "Connect response: ${connect_response:0:100}..."

            # Wait a moment for connection to establish
            sleep 1
        else
            show_debug "No daemon hosts found"
            rm -f "$cookie_jar"
            return 1
        fi
    else
        show_debug "Web UI already connected to daemon"
    fi

    # Update listen ports
    show_debug "Setting Deluge listen ports to $port-$port"
    local response=$(curl -s $CURL_TIMEOUT -b "$cookie_jar" \
        -H "Content-Type: application/json" \
        -d "{\"method\":\"core.set_config\",\"params\":[{\"listen_ports\":[$port,$port]}],\"id\":5}" \
        "$base_url/json" 2>/dev/null)

    show_debug "Deluge set_config response: ${response:0:100}..."

    rm -f "$cookie_jar"
    show_debug "Cleaned up cookie jar"

    if echo "$response" | grep -q '"error": *null'; then
        show_debug "Deluge port update successful"
        return 0
    else
        show_debug "Deluge port update failed (error in response)"
        return 1
    fi
}

# Update rTorrent port via XMLRPC
update_rtorrent() {
    local port=$1 base_url=$2 username=$3 password=$4

    show_debug "update_rtorrent: port=$port, url=$base_url, user=${username:+set}"
    show_debug "Setting rTorrent port range to $port-$port"

    # Build auth option if credentials provided
    local auth_opt=""
    if [ -n "$username" ] && [ -n "$password" ]; then
        auth_opt="-u $username:$password"
        show_debug "Using basic authentication"
    fi

    local response=$(curl -s $CURL_TIMEOUT \
        -X POST \
        -H "Content-Type: text/xml" \
        $auth_opt \
        "$base_url" \
        -d "<?xml version='1.0'?><methodCall><methodName>network.port_range.set</methodName><params><param><value><string></string></value></param><param><value><string>$port-$port</string></value></param></params></methodCall>" \
        2>/dev/null)

    show_debug "rTorrent response: $response"

    if echo "$response" | grep -q '<methodResponse>'; then
        # Check for fault response
        if echo "$response" | grep -q '<fault>'; then
            show_debug "rTorrent returned fault response"
            return 1
        fi
        show_debug "rTorrent port update successful"
        return 0
    else
        show_debug "rTorrent port update failed (no methodResponse)"
        return 1
    fi
}

# Execute custom command with port placeholder
_execute_custom_command() {
    local port=$1

    if [ -z "$PORT_SYNC_CMD" ]; then
        show_debug "_execute_custom_command: PORT_SYNC_CMD not set"
        return 1
    fi

    # Replace {PORT} placeholder in custom command
    local cmd="${PORT_SYNC_CMD//\{PORT\}/$port}"
    show_debug "Executing custom command (10s timeout): $cmd"

    # Use timeout with bash -c for safer execution and prevent hanging
    # 10 second timeout should be sufficient for most API calls and webhooks
    timeout 10 bash -c "$cmd" >/dev/null 2>&1
    local result=$?

    # timeout returns 124 on timeout, preserve that for debugging
    if [ $result -eq 124 ]; then
        show_debug "Custom command timed out after 10s"
    else
        show_debug "Custom command exit code: $result"
    fi

    return $result
}

# Internal function to perform a single client update attempt
_update_port_client_attempt() {
    local port=$1
    local result=1

    case "$PORT_SYNC_CLIENT" in
        qbittorrent|qbit|qb)
            show_debug "Using qBittorrent updater"
            update_qbittorrent "$port" "$PORT_SYNC_URL" "$PORT_SYNC_USER" "$PORT_SYNC_PASS"
            result=$?
            ;;
        transmission|trans)
            show_debug "Using Transmission updater"
            update_transmission "$port" "$PORT_SYNC_URL" "$PORT_SYNC_USER" "$PORT_SYNC_PASS"
            result=$?
            ;;
        deluge)
            show_debug "Using Deluge updater"
            update_deluge "$port" "$PORT_SYNC_URL" "$PORT_SYNC_PASS"
            result=$?
            ;;
        rtorrent|rutorrent)
            show_debug "Using rTorrent updater"
            update_rtorrent "$port" "$PORT_SYNC_URL" "$PORT_SYNC_USER" "$PORT_SYNC_PASS"
            result=$?
            ;;
        *)
            show_debug "Unknown PORT_SYNC_CLIENT: $PORT_SYNC_CLIENT"
            return 1
            ;;
    esac

    return $result
}

# Main update function with short retry for transient failures
# Supports three modes:
#   1. PORT_SYNC_CLIENT only - updates torrent client via API
#   2. PORT_SYNC_CMD only - executes custom command
#   3. Both - runs both sequentially, succeeds if either succeeds
# Returns 0 on success, 1 on failure (caller should handle long-term retry)
update_port_api() {
    local port=$1

    show_debug "update_port_api called with port=$port"

    # Quick validation
    if [ "$PORT_SYNC_ENABLED" != "true" ]; then
        show_debug "PORT_SYNC_ENABLED is not true, skipping update"
        return 0
    fi

    # Determine what we need to update
    local has_client=false
    local has_cmd=false

    if [ -n "$PORT_SYNC_CLIENT" ] && [ -n "$PORT_SYNC_URL" ]; then
        has_client=true
        show_debug "PORT_SYNC_CLIENT configured: $PORT_SYNC_CLIENT"
    fi

    if [ -n "$PORT_SYNC_CMD" ]; then
        has_cmd=true
        show_debug "PORT_SYNC_CMD configured"
    fi

    # Require at least one method
    if [ "$has_client" = false ] && [ "$has_cmd" = false ]; then
        show_debug "Neither PORT_SYNC_CLIENT+PORT_SYNC_URL nor PORT_SYNC_CMD is set, cannot update"
        return 1
    fi

    # Track results
    local client_success=false
    local cmd_success=false

    # Clean up any old success markers
    rm -f /tmp/port_sync_client_success /tmp/port_sync_cmd_success

    # Try to update client if configured
    if [ "$has_client" = true ]; then
        show_debug "Attempting client update ($PORT_SYNC_CLIENT)"
        local max_attempts=3
        for attempt in $(seq 1 $max_attempts); do
            show_debug "Client update attempt #$attempt (port=$port)"

            if _update_port_client_attempt "$port"; then
                show_debug "Client update successful on attempt #$attempt"
                client_success=true
                touch /tmp/port_sync_client_success
                break
            fi

            # Failed - wait before retry (unless last attempt)
            if [ $attempt -lt $max_attempts ]; then
                show_debug "Client update failed on attempt #$attempt, retrying in 2s"
                sleep 2
            else
                show_debug "Client update failed on attempt #$attempt (final attempt)"
            fi
        done
    fi

    # Try to execute custom command if configured
    if [ "$has_cmd" = true ]; then
        show_debug "Attempting custom command execution"
        local max_attempts=3
        for attempt in $(seq 1 $max_attempts); do
            show_debug "Custom command attempt #$attempt (port=$port)"

            if _execute_custom_command "$port"; then
                show_debug "Custom command successful on attempt #$attempt"
                cmd_success=true
                touch /tmp/port_sync_cmd_success
                break
            fi

            # Failed - wait before retry (unless last attempt)
            if [ $attempt -lt $max_attempts ]; then
                show_debug "Custom command failed on attempt #$attempt, retrying in 2s"
                sleep 2
            else
                show_debug "Custom command failed on attempt #$attempt (final attempt)"
            fi
        done
    fi

    # Determine overall success
    # If only one method is configured, its result determines success
    # If both are configured, either succeeding is considered success
    if [ "$has_client" = true ] && [ "$has_cmd" = true ]; then
        # Both configured - succeed if either succeeds
        if [ "$client_success" = true ] || [ "$cmd_success" = true ]; then
            show_debug "Overall result: SUCCESS (client=$client_success, cmd=$cmd_success)"
            return 0
        else
            show_debug "Overall result: FAILURE (both client and cmd failed)"
            return 1
        fi
    elif [ "$has_client" = true ]; then
        # Only client configured
        if [ "$client_success" = true ]; then
            show_debug "Overall result: SUCCESS (client)"
            return 0
        else
            show_debug "Overall result: FAILURE (client)"
            return 1
        fi
    else
        # Only cmd configured
        if [ "$cmd_success" = true ]; then
            show_debug "Overall result: SUCCESS (cmd)"
            return 0
        else
            show_debug "Overall result: FAILURE (cmd)"
            return 1
        fi
    fi
}

# Export for use in other scripts
export -f update_port_api
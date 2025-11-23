#!/bin/bash
# Proxy server management using Go-based SOCKS5/HTTP proxy
# No setuid/setgid required - works with minimal capabilities
#
# The Go proxy binary handles both SOCKS5 and HTTP proxy in a single process
# and supports authentication without requiring special permissions.

set -euo pipefail

source /app/scripts/ui.sh

# Configuration (set by run.sh, so don't override)
SOCKS5_PORT=${SOCKS5_PORT:-1080}
HTTP_PROXY_PORT=${HTTP_PROXY_PORT:-8888}
PROXY_USER=${PROXY_USER:-""}
PROXY_PASS=${PROXY_PASS:-""}

show_debug "Proxy configuration:"
show_debug "  SOCKS5_PORT=$SOCKS5_PORT"
show_debug "  HTTP_PROXY_PORT=$HTTP_PROXY_PORT"
show_debug "  PROXY_USER=${PROXY_USER:+set}"
show_debug "  PROXY_PASS=${PROXY_PASS:+set}"

# Internal constants
readonly PID_FILE="/tmp/proxy.pid"
readonly LOG_FILE="/tmp/proxy.log"

# Start proxy server (single Go binary handles both SOCKS5 and HTTP)
start_proxies() {
    show_info
    show_step "Starting proxy servers..."

    # Kill any existing proxy process
    show_debug "Cleaning up any existing proxy processes"
    stop_proxies_silent
    sleep 1

    # Determine credentials (check secrets first, then env vars)
    local proxy_user="${PROXY_USER:-}"
    local proxy_pass="${PROXY_PASS:-}"

    if [ -f "/run/secrets/proxy_user" ]; then
        show_debug "Found Docker secret: /run/secrets/proxy_user"
        proxy_user=$(cat /run/secrets/proxy_user)
    fi
    if [ -f "/run/secrets/proxy_pass" ]; then
        show_debug "Found Docker secret: /run/secrets/proxy_pass"
        proxy_pass=$(cat /run/secrets/proxy_pass)
    fi

    show_debug "Credentials: user=${proxy_user:+set}, pass=${proxy_pass:+set}"

    # Export for Go proxy (it will also check secrets, but this ensures compatibility)
    export PROXY_USER="$proxy_user"
    export PROXY_PASS="$proxy_pass"
    export SOCKS5_PORT HTTP_PROXY_PORT

    # Start the Go proxy in background
    show_debug "Starting /usr/local/bin/proxy (log: $LOG_FILE)"
    /usr/local/bin/proxy >"$LOG_FILE" 2>&1 &
    local proxy_pid=$!
    show_debug "Proxy process started with PID: $proxy_pid"

    # Wait and verify it started
    #show_debug "Waiting 3s for proxy to initialize..."
    #sleep 3
    
    if kill -0 $proxy_pid 2>/dev/null; then
        show_debug "Proxy process verified running, saving PID to $PID_FILE"
        echo "$proxy_pid" > "$PID_FILE"

        if [ -n "$proxy_user" ] && [ -n "$proxy_pass" ]; then
            show_success "Proxy servers ready (authenticated):"
            show_info "      SOCKS5: socks5://$proxy_user:****@<container-ip>:$SOCKS5_PORT"
            show_info "      HTTP:   http://$proxy_user:****@<container-ip>:$HTTP_PROXY_PORT"
            show_debug "Proxy authentication enabled"
        else
            show_success "Proxy servers ready:"
            show_info "      SOCKS5: socks5://<container-ip>:$SOCKS5_PORT"
            show_info "      HTTP:   http://<container-ip>:$HTTP_PROXY_PORT"
            show_debug "Proxy running without authentication"
        fi
        return 0
    else
        show_error "Failed to start proxy servers"
        show_debug "Proxy process $proxy_pid not running after 3s"
        
        if [ -f "$LOG_FILE" ]; then
            echo "  ${ylw}Debug output:${nc}"
            tail -10 "$LOG_FILE" | sed 's/^/    /'
            
            if [ $_LOG_LEVEL -ge 2 ]; then
                show_debug "Full proxy log:"
                cat "$LOG_FILE" | while read -r line; do
                    show_debug "  $line"
                done
            fi
        fi
        return 1
    fi
}

# Stop proxy servers (silent version for internal use)
stop_proxies_silent() {
    if [ -f "$PID_FILE" ]; then
        local pid=$(cat "$PID_FILE")
        show_debug "Found PID file with PID: $pid"
        if kill "$pid" 2>/dev/null; then
            show_debug "Killed proxy process $pid"
        else
            show_debug "Process $pid already dead"
        fi
        rm -f "$PID_FILE"
        show_debug "Removed PID file"
    else
        show_debug "No PID file found"
    fi
    
    if pkill -f proxy 2>/dev/null; then
        show_debug "Killed additional proxy processes via pkill"
    else
        show_debug "No additional proxy processes found"
    fi
}

# Stop proxy servers (with output)
stop_proxies() {
    stop_proxies_silent
}

# Restart proxy servers (used after VPN reconnection)
restart_proxies() {
    show_step "Restarting proxy servers..."
    show_debug "restart_proxies: Beginning proxy restart"
    
    stop_proxies_silent
    show_debug "Waiting 2s before restart..."
    sleep 2
    start_proxies
}

# Check if proxy is running
is_proxy_running() {
    if [ -f "$PID_FILE" ]; then
        local pid=$(cat "$PID_FILE")
        if kill -0 "$pid" 2>/dev/null; then
            show_debug "is_proxy_running: Yes (PID: $pid)"
            return 0
        else
            show_debug "is_proxy_running: No (PID file exists but process $pid is dead)"
            return 1
        fi
    else
        show_debug "is_proxy_running: No (no PID file)"
        return 1
    fi
}

# Get proxy status
get_proxy_status() {
    show_debug "get_proxy_status: Checking proxy status"
    
    if is_proxy_running; then
        echo "running"
        return 0
    else
        echo "stopped"
        return 1
    fi
}

# CLI interface (if script is executed directly)
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    show_debug "Script executed directly with command: ${1:-start}"
    
    case "${1:-start}" in
        start)
            start_proxies
            ;;
        stop)
            stop_proxies
            ;;
        restart)
            restart_proxies
            ;;
        status)
            local status=$(get_proxy_status)
            if [ "$status" = "running" ]; then
                echo "Proxy servers are running (PID: $(cat "$PID_FILE"))"
                exit 0
            else
                echo "Proxy servers are not running"
                exit 1
            fi
            ;;
        *)
            echo "Usage: $0 {start|stop|restart|status}"
            exit 1
            ;;
    esac
fi
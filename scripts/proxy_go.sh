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

# Internal constants
readonly PID_FILE="/tmp/proxy.pid"
readonly LOG_FILE="/tmp/proxy.log"

# Start proxy server (single Go binary handles both SOCKS5 and HTTP)
start_proxies() {
    echo ""
    show_step "Starting proxy servers..."

    # Kill any existing proxy process
    stop_proxies_silent
    sleep 1

    # Determine credentials (check secrets first, then env vars)
    local proxy_user="${PROXY_USER:-}"
    local proxy_pass="${PROXY_PASS:-}"

    if [ -f "/run/secrets/proxy_user" ]; then
        proxy_user=$(cat /run/secrets/proxy_user)
    fi
    if [ -f "/run/secrets/proxy_pass" ]; then
        proxy_pass=$(cat /run/secrets/proxy_pass)
    fi

    # Export for Go proxy (it will also check secrets, but this ensures compatibility)
    export PROXY_USER="$proxy_user"
    export PROXY_PASS="$proxy_pass"
    export SOCKS5_PORT HTTP_PROXY_PORT

    # Start the Go proxy in background
    /usr/local/bin/proxy >"$LOG_FILE" 2>&1 &
    local proxy_pid=$!

    # Wait and verify it started
    sleep 3
    if kill -0 $proxy_pid 2>/dev/null; then
        echo "$proxy_pid" > "$PID_FILE"

        if [ -n "$proxy_user" ] && [ -n "$proxy_pass" ]; then
            show_success "Proxy servers ready (authenticated):"
            echo "      SOCKS5: socks5://$proxy_user:****@<container-ip>:$SOCKS5_PORT"
            echo "      HTTP:   http://$proxy_user:****@<container-ip>:$HTTP_PROXY_PORT"
        else
            show_success "Proxy servers ready:"
            echo "      SOCKS5: socks5://<container-ip>:$SOCKS5_PORT"
            echo "      HTTP:   http://<container-ip>:$HTTP_PROXY_PORT"
        fi
        return 0
    else
        show_error "Failed to start proxy servers"
        if [ -f "$LOG_FILE" ]; then
            echo "  ${ylw}Debug output:${nc}"
            tail -10 "$LOG_FILE" | sed 's/^/    /'
        fi
        return 1
    fi
}

# Stop proxy servers (silent version for internal use)
stop_proxies_silent() {
    if [ -f "$PID_FILE" ]; then
        kill $(cat "$PID_FILE") 2>/dev/null || true
        rm -f "$PID_FILE"
    fi
    pkill -f proxy 2>/dev/null || true
}

# Stop proxy servers (with output)
stop_proxies() {
    stop_proxies_silent
}

# Restart proxy servers (used after VPN reconnection)
restart_proxies() {
    show_step "Restarting proxy servers..."
    stop_proxies_silent
    sleep 2
    start_proxies
}

# Check if proxy is running
is_proxy_running() {
    [ -f "$PID_FILE" ] && kill -0 $(cat "$PID_FILE") 2>/dev/null
}

# Get proxy status
get_proxy_status() {
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

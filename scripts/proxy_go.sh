#!/bin/bash

# Proxy server management using Go-based proxy (NO SETUID/SETGID NEEDED!)

source /app/scripts/ui.sh

SOCKS5_PORT=${SOCKS5_PORT:-1080}
HTTP_PROXY_PORT=${HTTP_PROXY_PORT:-8888}
PROXY_USER=${PROXY_USER:-""}
PROXY_PASS=${PROXY_PASS:-""}

# Start proxy server (single Go binary handles both SOCKS5 and HTTP)
start_proxies() {
    show_step "Starting proxy servers..."
    
    # Kill any existing proxy process
    pkill -f proxy 2>/dev/null || true
    sleep 1
    
    # Export environment variables for Go proxy
    export SOCKS5_PORT HTTP_PROXY_PORT PROXY_USER PROXY_PASS
    
    # Start the Go proxy in background
    /usr/local/bin/proxy >/tmp/proxy.log 2>&1 &
    local proxy_pid=$!
    
    # Wait and check if it started
    sleep 3
    if kill -0 $proxy_pid 2>/dev/null; then
        echo "$proxy_pid" > /tmp/proxy.pid
        
        if [ -n "$PROXY_USER" ] && [ -n "$PROXY_PASS" ]; then
	    show_success "Proxy servers ready (authenticated):"
            echo "      SOCKS5: socks5://$PROXY_USER:****@<container-ip>:$SOCKS5_PORT"
            echo "      HTTP:   http://$PROXY_USER:****@<container-ip>:$HTTP_PROXY_PORT"
        else
            show_success "Proxy servers ready:"
            echo "      SOCKS5: socks5://<container-ip>:$SOCKS5_PORT"
            echo "      HTTP:   http://<container-ip>:$HTTP_PROXY_PORT"
        fi
        echo ""
        return 0
    else
        show_error "Failed to start proxy servers"
        if [ -f /tmp/proxy.log ]; then
            echo "  ${ylw}Debug output:${nc}"
            tail -10 /tmp/proxy.log | sed 's/^/    /'
        fi
        return 1
    fi
}

# Stop proxy servers
stop_proxies() {
    show_step "Stopping proxy servers..."
    
    if [ -f /tmp/proxy.pid ]; then
        kill $(cat /tmp/proxy.pid) 2>/dev/null || true
        rm -f /tmp/proxy.pid
    fi
    pkill -f proxy 2>/dev/null || true
    
    show_success "Proxy servers stopped"
}

# Restart proxy servers (used after VPN reconnection)
restart_proxies() {
    show_step "Restarting proxy servers..."
    stop_proxies
    sleep 2
    start_proxies
}

# Check if proxy is running
is_proxy_running() {
    [ -f /tmp/proxy.pid ] && kill -0 $(cat /tmp/proxy.pid) 2>/dev/null
}

# Only run if executed directly (not sourced)
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
            if is_proxy_running; then
                echo "Proxy servers are running (PID: $(cat /tmp/proxy.pid))"
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

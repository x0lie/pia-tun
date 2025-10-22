#!/bin/bash

set -e

source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/vpn.sh
source /app/scripts/verify_connection.sh
source /app/scripts/proxy_go.sh

# OPTIMIZED: Set defaults inline, export only what's needed by child processes
DISABLE_IPV6=${DISABLE_IPV6:-true}
DNS=${DNS:-pia}
LOCAL_NETWORK=${LOCAL_NETWORK:-""}
QUIET_MODE=true
KILLSWITCH_EXEMPT_PORTS=${KILLSWITCH_EXEMPT_PORTS:-""}
HANDSHAKE_TIMEOUT=${HANDSHAKE_TIMEOUT:-180}
CHECK_INTERVAL=${CHECK_INTERVAL:-15}
MAX_FAILURES=${MAX_FAILURES:-2}
RESTART_SERVICES=${RESTART_SERVICES:-""}
PROXY_ENABLED=${PROXY_ENABLED:-false}
SOCKS5_PORT=${SOCKS5_PORT:-1080}
HTTP_PROXY_PORT=${HTTP_PROXY_PORT:-8888}
PORT_API_ENABLED=${PORT_API_ENABLED:-false}

# Export only what child processes actually need
export DISABLE_IPV6 DNS LOCAL_NETWORK KILLSWITCH_EXEMPT_PORTS
export CHECK_INTERVAL MAX_FAILURES HANDSHAKE_TIMEOUT
export PROXY_ENABLED SOCKS5_PORT HTTP_PROXY_PORT
export PORT_API_ENABLED PORT_API_TYPE PORT_API_URL PORT_API_USER PORT_API_PASS
export RESTART_SERVICES MONITOR_DEBUG METRICS METRICS_PORT
export MONITOR_PARALLEL_CHECKS MONITOR_FAST_FAIL MONITOR_WATCH_HANDSHAKE

# Boolean flags (set once, check many times)
PF_ENABLED=false
[ "$PORT_FORWARDING" = "true" ] && PF_ENABLED=true

DEBUG_MODE=false
[ "$MONITOR_DEBUG" = "true" ] && DEBUG_MODE=true

PROXY_ENABLED_FLAG=false
[ "$PROXY_ENABLED" = "true" ] && PROXY_ENABLED_FLAG=true

cleanup() {
    echo ""
    show_step "Shutting down..."
    $PROXY_ENABLED_FLAG && stop_proxies
    pkill -f "monitor" 2>/dev/null || true
    pkill -f "port_forwarding.sh" 2>/dev/null || true
    pkill -f "port_monitor.sh" 2>/dev/null || true
    teardown_wireguard
    cleanup_killswitch
    show_success "Cleanup complete"
    exit 0
}

trap cleanup SIGTERM SIGINT SIGQUIT

initial_connect() {
    local quiet_mode="${1:-false}"
    
    [ ! -f /tmp/reconnecting ] && print_banner
    
    # OPTIMIZED: Only capture real IP if not already captured
    if [ ! -f /tmp/real_ip ]; then
        capture_real_ip
        echo ""
    fi
    
    # Only show these steps if not in quiet mode (first connection)
    if [ "$quiet_mode" != "true" ]; then
        setup_pre_tunnel_killswitch
        
        # Debug info
        if $DEBUG_MODE; then
            echo ""
            echo "  ${blu}[DEBUG]${nc} Pre-tunnel firewall rules:"
            iptables -L VPN_OUT -n -v 2>/dev/null | head -20 | sed 's/^/    /' || \
                nft list chain inet vpn_filter output 2>/dev/null | head -20 | sed 's/^/    /'
            echo ""
            echo "  ${blu}[DEBUG]${nc} Testing connectivity:"
            printf "    DNS: "
            nslookup www.privateinternetaccess.com >/dev/null 2>&1 && echo "${grn}OK${nc}" || echo "${red}FAIL${nc}"
            printf "    HTTPS: "
            curl -s --max-time 5 https://www.privateinternetaccess.com >/dev/null 2>&1 && echo "${grn}OK${nc}" || echo "${red}FAIL${nc}"
        fi
        echo ""
    else
        # Silent killswitch setup during reconnection
        setup_pre_tunnel_killswitch >/dev/null 2>&1
    fi
    
    /app/scripts/vpn.sh "$quiet_mode" || return 1
    
    # Save server latency for metrics
    [ -f /tmp/server_latency_temp ] && mv /tmp/server_latency_temp /tmp/server_latency
    
    # Only show tunnel establishment if not in quiet mode
    if [ "$quiet_mode" != "true" ]; then
        show_step "Establishing VPN connection..."
        bring_up_wireguard /etc/wireguard/pia.conf && show_success "VPN tunnel established" || \
            { show_error "Failed to bring up tunnel"; return 1; }
        sleep 3
        echo ""
        
        finalize_killswitch
        echo ""
    else
        # Silent operations during reconnection
        bring_up_wireguard /etc/wireguard/pia.conf >/dev/null 2>&1 || \
            { show_error "Failed to bring up tunnel"; return 1; }
        sleep 3
        finalize_killswitch >/dev/null 2>&1
    fi
    
    show_step "Verifying connection..."
    verify_connection && echo "" || { show_warning "Connection verification found issues"; echo ""; }
}

perform_reconnection() {
    show_reconnecting
    touch /tmp/reconnecting
    
    $PF_ENABLED && {
        pkill -f "port_forwarding.sh" 2>/dev/null || true
        pkill -f "port_monitor.sh" 2>/dev/null || true
    }
    $PROXY_ENABLED_FLAG && stop_proxies
    
    # Silent teardown
    teardown_wireguard >/dev/null 2>&1
    
    if initial_connect "true"; then
        [ -n "$RESTART_SERVICES" ] && { restart_services "$RESTART_SERVICES"; echo ""; }
        
        # Start proxies after VPN is up
        $PROXY_ENABLED_FLAG && start_proxies
        
        if $PF_ENABLED; then
            /app/scripts/port_forwarding.sh &
            
            # Wait for port forwarding to complete (max 30s)
            local waited=0
            while [ $waited -lt 30 ]; do
                [ -f /tmp/port_forwarding_complete ] && { rm -f /tmp/port_forwarding_complete; break; }
                sleep 1
                waited=$((waited + 1))
            done
            
            # Restart port monitor if API is enabled
            if [ "$PORT_API_ENABLED" = "true" ]; then
                show_step "Restarting port monitor..."
                /app/scripts/port_monitor.sh &
                show_success "Port monitor restarted"
                echo ""
            fi
        else
            show_vpn_connected
        fi
        
        show_step "Health monitor still running..."
        show_success "Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"
        echo ""
        return 0
    else
        show_error "Reconnection failed"
        return 1
    fi
}

main_loop() {
    # Start proxies first if enabled
    $PROXY_ENABLED_FLAG && start_proxies
    
    if $PF_ENABLED; then
        /app/scripts/port_forwarding.sh &
        
        # Wait for port forwarding to complete (max 30s)
        local waited=0
        while [ $waited -lt 30 ]; do
            [ -f /tmp/port_forwarding_complete ] && { rm -f /tmp/port_forwarding_complete; break; }
            sleep 1
            waited=$((waited + 1))
        done
        
        # Start port monitor if API is enabled
        [ "$PORT_API_ENABLED" = "true" ] && /app/scripts/port_monitor.sh &
    else
        show_vpn_connected
    fi
    
    # IMPROVED: Add stabilization delay before starting monitor
    # This gives the VPN time to establish handshakes and settle
    show_step "Waiting for VPN to stabilize..."
    sleep 5
    show_success "VPN ready"
    echo ""
    
    show_step "Starting health monitor..."
    /usr/local/bin/monitor &
    show_success "Health monitor active (PID: $!)"
    show_success "Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"
    
    # Show active detection modes
    [ "$MONITOR_FAST_FAIL" = "true" ] && show_success "Fast-fail mode: enabled"
    [ "$MONITOR_PARALLEL_CHECKS" = "true" ] && show_success "Parallel checks: enabled"
    [ "$MONITOR_WATCH_HANDSHAKE" = "true" ] && show_success "Handshake monitoring: enabled (timeout: ${HANDSHAKE_TIMEOUT}s)"
    
    if [ "$METRICS" = "true" ]; then
        echo "  ${grn}✓${nc} Metrics available on port ${METRICS_PORT:-9090}"
        echo "      Prometheus:  http://<container-ip>:${METRICS_PORT:-9090}/metrics?format=prometheus"
        echo "      JSON:        http://<container-ip>:${METRICS_PORT:-9090}/metrics"
        echo "      Health:      http://<container-ip>:${METRICS_PORT:-9090}/health"
    fi
    
    echo ""
    
    while true; do
        if [ -f /tmp/vpn_reconnect_requested ]; then
            rm -f /tmp/vpn_reconnect_requested
            perform_reconnection || {
                show_warning "Retrying..."
                sleep 5
                touch /tmp/vpn_reconnect_requested
            }
        fi
        sleep 5
    done
}

initial_connect || { show_error "Initial connection failed"; exit 1; }
main_loop

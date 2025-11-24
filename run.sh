#!/bin/bash

set -euo pipefail

source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/vpn.sh
source /app/scripts/verify_connection.sh
source /app/scripts/proxy_go.sh

# OPTIMIZED: Set defaults inline, export only what's needed by child processes
PORT_FORWARDING=${PORT_FORWARDING:-false}
DISABLE_IPV6=${DISABLE_IPV6:-true}
DNS=${DNS:-pia}
LOCAL_NETWORK=${LOCAL_NETWORK:-""}
KILLSWITCH_EXEMPT_PORTS=${KILLSWITCH_EXEMPT_PORTS:-""}
HANDSHAKE_TIMEOUT=${HANDSHAKE_TIMEOUT:-180}
CHECK_INTERVAL=${CHECK_INTERVAL:-15}
MAX_FAILURES=${MAX_FAILURES:-2}
RESTART_SERVICES=${RESTART_SERVICES:-""}
PROXY_ENABLED=${PROXY_ENABLED:-false}
SOCKS5_PORT=${SOCKS5_PORT:-1080}
HTTP_PROXY_PORT=${HTTP_PROXY_PORT:-8888}
PORT_API_ENABLED=${PORT_API_ENABLED:-false}

# Auto-enable PORT_API and PORT_FORWARDING if PORT_API_TYPE is set
[ -n "${PORT_API_TYPE:-}" ] && PORT_API_ENABLED=true && PORT_FORWARDING=true

# Export only what child processes actually need
export PORT_FORWARDING
export DISABLE_IPV6 DNS LOCAL_NETWORK KILLSWITCH_EXEMPT_PORTS
export CHECK_INTERVAL MAX_FAILURES HANDSHAKE_TIMEOUT
export PROXY_ENABLED SOCKS5_PORT HTTP_PROXY_PORT
export PORT_API_ENABLED PORT_API_TYPE PORT_API_URL PORT_API_USER PORT_API_PASS PORT_API_CMD
export RESTART_SERVICES METRICS METRICS_PORT
export MONITOR_PARALLEL_CHECKS
export WEBHOOK_URL

# Boolean flags (set once, check many times)
PF_ENABLED=false
[ "$PORT_FORWARDING" = "true" ] && PF_ENABLED=true

PROXY_ENABLED_FLAG=false
[ "$PROXY_ENABLED" = "true" ] && PROXY_ENABLED_FLAG=true

trap cleanup SIGTERM SIGINT SIGQUIT

cleanup() {
    show_info
    show_step "Shutting down..."
    
    show_debug "Cleanup: Stopping proxies (enabled=$PROXY_ENABLED_FLAG)"
    $PROXY_ENABLED_FLAG && stop_proxies
    
    show_debug "Cleanup: Killing monitor processes"
    pkill -f "monitor" 2>/dev/null || true
    
    show_debug "Cleanup: Killing port forwarding processes"
    pkill -f "port_forwarding.sh" 2>/dev/null || true
    pkill -f "port_monitor.sh" 2>/dev/null || true

    teardown_wireguard
    
    show_success "Cleanup complete"
    exit 0
}

initial_connect() {
    local restart="${1:-false}"
    
    show_debug "initial_connect called"

    [ ! -f /tmp/reconnecting ] && print_banner

    # Only capture real IP if not already captured
    if [ ! -f /tmp/real_ip ]; then
        capture_real_ip
        show_info
    else
        show_debug "Real IP already captured: $(cat /tmp/real_ip 2>/dev/null || echo 'unknown')"
    fi

    show_debug "Clearing /etc/resolv.conf"
    echo > /etc/resolv.conf

    # Setup baseline killswitch first (includes bypass route allowances)
    if [ "$restart" != "true" ]; then
        setup_baseline_killswitch
        show_info

        # Debug: Show killswitch rules
        if [ $_LOG_LEVEL -ge 2 ]; then
            show_info
            show_debug "Baseline killswitch rules:"
            if command -v nft >/dev/null 2>&1; then
                nft list chain inet vpn_filter output 2>/dev/null | head -20 | while read -r line; do
                    show_debug "  $line"
                done
            else
                iptables -L VPN_OUT -n -v 2>/dev/null | head -20 | while read -r line; do
                    show_debug "  $line"
                done
            fi
            show_info
        fi
    else
        show_debug "Skipping baseline killswitch setup (restart=true)"
    fi

    # VPN setup (uses surgical exemptions internally)
    /app/scripts/vpn.sh "$restart" || {
        show_error "vpn.sh failed with exit code $?"
        return 1
    }

    # Save server latency for metrics
    if [ -f /tmp/server_latency_temp ]; then
        show_debug "Saving server latency: $(cat /tmp/server_latency_temp)"
        mv /tmp/server_latency_temp /tmp/server_latency
    fi

    show_step "Establishing VPN connection..."
    if bring_up_wireguard /etc/wireguard/pia.conf; then
        show_success "VPN tunnel established"
        show_debug "WireGuard interface brought up successfully"
    else
        show_error "Failed to bring up tunnel, error code $?"
        return 1
    fi

    # Add VPN to killswitch (now it's safe to route through VPN)
    if [ "$restart" != "true" ]; then
        add_vpn_to_killswitch
        show_ruleset_stats
    else
        show_debug "Adding VPN to killswitch (reconnection, suppressing output)"
        add_vpn_to_killswitch >/dev/null 2>&1
    fi

    show_info
    show_step "Verifying connection..."
    if verify_connection; then
        show_debug "Connection verification passed"
    else
        show_warning "Connection verification found issues"
        show_debug "verify_connection returned non-zero exit code"
        show_info
    fi
}

perform_reconnection() {
    show_debug "Reconnection requested"
    show_reconnecting
    touch /tmp/reconnecting

    if $PF_ENABLED; then
        show_debug "Stopping port forwarding processes"
        pkill -f "port_forwarding.sh" 2>/dev/null || true
        pkill -f "port_monitor.sh" 2>/dev/null || true
    fi
    
    if $PROXY_ENABLED_FLAG; then
        show_debug "Stopping proxies"
        stop_proxies
    fi

    # Teardown VPN (removes from killswitch first, then tears down interface)
    teardown_wireguard

    if initial_connect "true"; then
        show_debug "Reconnection: VPN established successfully"
        
        # Start proxies after VPN is up
        if $PROXY_ENABLED_FLAG; then
            show_debug "Restarting proxies"
            start_proxies >/dev/null 2>&1
        fi

        if $PF_ENABLED; then
            show_debug "Starting port forwarding script"
            /app/scripts/port_forwarding.sh &

            # Wait for port forwarding to complete (max 30s)
            local waited=0
            show_debug "Waiting for port forwarding completion (max 30s)"
            while [ $waited -lt 30 ]; do
                if [ -f /tmp/port_forwarding_complete ]; then
                    show_debug "Port forwarding completed after ${waited}s"
                    rm -f /tmp/port_forwarding_complete
                    break
                fi
                sleep 1
                waited=$((waited + 1))
            done
            [ $waited -ge 30 ] && show_debug "Port forwarding wait timed out after 30s"
        else
            show_vpn_connected
        fi

        show_step "Health monitor still running..."
        show_success "Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"

        # Restart port monitor if both PF and API are enabled (now after health status)
        if $PF_ENABLED && [ "$PORT_API_ENABLED" = "true" ]; then
            show_info
            show_step "Restarting port monitor..."
            show_debug "Launching port_monitor.sh"
            /app/scripts/port_monitor.sh &
        fi
        
        sleep 2
        
        if [ -n "$RESTART_SERVICES" ]; then
            show_info
            show_debug "Restarting dependent services: $RESTART_SERVICES"
            restart_services "$RESTART_SERVICES"
            show_info
        fi
        
        show_debug "Removing reconnecting flag"
        rm -f /tmp/reconnecting
        return 0
    else
        show_error "Reconnection failed"
        show_debug "initial_connect returned error during reconnection"
        return 1
    fi
}

main_loop() {
    
    # Start proxies first if enabled
    if $PROXY_ENABLED_FLAG; then
        start_proxies
    fi

    if $PF_ENABLED; then
        show_debug "Starting port forwarding script"
        /app/scripts/port_forwarding.sh &

        # Wait for port forwarding to complete (max 30s)
        local waited=0
        show_debug "Waiting for port forwarding completion (max 30s)"
        while [ $waited -lt 30 ]; do
            if [ -f /tmp/port_forwarding_complete ]; then
                show_debug "Port forwarding completed after ${waited}s"
                rm -f /tmp/port_forwarding_complete
                break
            fi
            sleep 1
            waited=$((waited + 1))
        done
        [ $waited -ge 30 ] && show_debug "Port forwarding wait timed out after 30s"
    else
        show_vpn_connected
    fi

    show_step "Starting health monitor..."
    /usr/local/bin/monitor &
    show_success "Health monitor active"
    show_success "Check interval: ${CHECK_INTERVAL}s, Failure threshold: ${MAX_FAILURES}"

    if [ "$METRICS" = "true" ]; then
        show_success "Metrics available on port ${METRICS_PORT:-9090}"
        show_info "      Prometheus:  http://<container-ip>:${METRICS_PORT:-9090}/metrics?format=prometheus"
        show_info "      JSON:        http://<container-ip>:${METRICS_PORT:-9090}/metrics"
        show_info "      Health:      http://<container-ip>:${METRICS_PORT:-9090}/health"
    fi

    # Start port monitor if PF and API updater are enabled
    if $PF_ENABLED && [ "$PORT_API_ENABLED" = "true" ]; then
        show_debug "Starting port monitor (PF + API enabled)"
        /app/scripts/port_monitor.sh &
    fi

    sleep 1
    
    if [ -n "$RESTART_SERVICES" ]; then
        show_info
        show_debug "Restarting dependent services: $RESTART_SERVICES"
        restart_services "$RESTART_SERVICES"
        show_info
    fi

  # Create named pipe for monitor communication
  RECONNECT_PIPE="/tmp/vpn_reconnect_pipe"
  [ -p "$RECONNECT_PIPE" ] || mkfifo "$RECONNECT_PIPE"

  show_debug "Entering reconnection monitor loop (blocking on pipe)"
  while true; do
      # This blocks until monitor writes to the pipe
      if read -r reason < "$RECONNECT_PIPE"; then
          show_debug "Reconnection requested: ${reason:-no reason given}"

          if perform_reconnection; then
              show_debug "Reconnection successful"
          else
              show_warning "Retrying..."
              sleep 5
          fi
      fi
  done
}

# Debug: Show environment configuration
show_debug "Environment configuration:"
show_debug "  PORT_FORWARDING=$PORT_FORWARDING"
show_debug "  DISABLE_IPV6=$DISABLE_IPV6"
show_debug "  DNS=$DNS"
show_debug "  LOCAL_NETWORK=$LOCAL_NETWORK"
show_debug "  KILLSWITCH_EXEMPT_PORTS=$KILLSWITCH_EXEMPT_PORTS"
show_debug "  CHECK_INTERVAL=$CHECK_INTERVAL"
show_debug "  MAX_FAILURES=$MAX_FAILURES"
show_debug "  HANDSHAKE_TIMEOUT=$HANDSHAKE_TIMEOUT"
show_debug "  PROXY_ENABLED=$PROXY_ENABLED"
show_debug "  PORT_API_ENABLED=$PORT_API_ENABLED"
show_debug "  RESTART_SERVICES=$RESTART_SERVICES"
show_debug "  METRICS=$METRICS"
show_debug "  LOG_LEVEL=${LOG_LEVEL} (numeric: $_LOG_LEVEL)"

show_debug "Starting initial connection"
if initial_connect; then
    main_loop
else
    show_error "Initial connection failed"
    show_debug "Exiting with error code 1"
    exit 1
fi
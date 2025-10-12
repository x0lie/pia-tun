#!/bin/bash

# Configuration
CHECK_INTERVAL=${CHECK_INTERVAL:-30}
MAX_FAILURES=${MAX_FAILURES:-2}
RECONNECT_DELAY=${RECONNECT_DELAY:-5}
MAX_RECONNECT_DELAY=${MAX_RECONNECT_DELAY:-300}
RESTART_SERVICES=${RESTART_SERVICES:-""}

# Set boolean flag once
DEBUG_MODE=false
[ "$MONITOR_DEBUG" = "true" ] && DEBUG_MODE=true

source /app/scripts/ui.sh

failure_count=0
reconnect_attempts=0

is_interface_up() {
    ip link show pia >/dev/null 2>&1 || return 1
    ip addr show pia 2>/dev/null | grep -q "inet " || \
    wg show pia peers 2>/dev/null | grep -q "." || \
    ! ip link show pia 2>/dev/null | grep -q "state DOWN"
}

check_external_connectivity() {
    ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1 || \
    ping -c 1 -W 3 8.8.8.8 >/dev/null 2>&1 || \
    curl --max-time 5 -s http://1.1.1.1 >/dev/null 2>&1
}

check_vpn_health() {
    $DEBUG_MODE && echo "  ${blu}[DEBUG]${nc} Health check at $(date '+%H:%M:%S')" >&2
    
    is_interface_up || { $DEBUG_MODE && echo "  ${red}[DEBUG]${nc} Interface check failed" >&2; return 1; }
    $DEBUG_MODE && echo "  ${grn}[DEBUG]${nc} Interface is up" >&2
    
    # Check connectivity with single retry
    check_external_connectivity && { $DEBUG_MODE && echo "  ${grn}[DEBUG]${nc} Connectivity passed" >&2; return 0; }
    
    $DEBUG_MODE && echo "  ${ylw}[DEBUG]${nc} First check failed, retrying..." >&2
    sleep 2
    check_external_connectivity && { $DEBUG_MODE && echo "  ${grn}[DEBUG]${nc} Retry passed" >&2; return 0; }
    
    $DEBUG_MODE && echo "  ${red}[DEBUG]${nc} All checks failed" >&2
    return 1
}

trigger_reconnect() {
    reconnect_attempts=$((reconnect_attempts + 1))
    local delay=$((RECONNECT_DELAY * reconnect_attempts))
    [ $delay -gt $MAX_RECONNECT_DELAY ] && delay=$MAX_RECONNECT_DELAY
    
    echo "▶ Reconnecting in ${delay}s..."
    sleep $delay
    touch /tmp/vpn_reconnect_requested
}

monitor_loop() {
    local consecutive_successes=0
    local first_check=true

    while true; do
        sleep $CHECK_INTERVAL
        $first_check && { first_check=false; continue; }

        if check_vpn_health; then
            [ $failure_count -gt 0 ] && {
                show_success "VPN connection restored"
                failure_count=0
                reconnect_attempts=0
            }
            consecutive_successes=$((consecutive_successes + 1))
        else
            failure_count=$((failure_count + 1))
            consecutive_successes=0

            if [ $failure_count -lt $MAX_FAILURES ]; then
                show_warning "VPN health check failed (${failure_count}/${MAX_FAILURES})"
                $DEBUG_MODE && [ $failure_count -eq 1 ] && {
                    echo "  ${ylw}ℹ${nc} Debug info:"
                    wg show pia 2>&1 | head -5 | sed 's/^/    /'
                }
            else
                show_error "VPN connection lost (${failure_count}/${MAX_FAILURES})"
                trigger_reconnect
                failure_count=0
            fi
        fi
    done
}

# Only run if executed directly (not sourced)
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    monitor_loop
fi

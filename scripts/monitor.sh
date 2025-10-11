#!/bin/bash

# VPN Health Monitor - Detects failures and triggers reconnection

# Configuration
CHECK_INTERVAL=${CHECK_INTERVAL:-30}
MAX_FAILURES=${MAX_FAILURES:-2}
RECONNECT_DELAY=${RECONNECT_DELAY:-5}
MAX_RECONNECT_DELAY=${MAX_RECONNECT_DELAY:-300}
RESTART_SERVICES=${RESTART_SERVICES:-""}
MONITOR_DEBUG=${MONITOR_DEBUG:-false}

# Source UI helpers
source /app/scripts/ui.sh

# Failure counter
failure_count=0
reconnect_attempts=0

# Get last handshake timestamp
get_last_handshake() {
    local handshake=$(wg show pia latest-handshakes 2>/dev/null | awk '{print $2}')
    if [ -z "$handshake" ] || [ "$handshake" = "0" ]; then
        echo "0"
        return 1
    fi
    echo "$handshake"
    return 0
}

# Check if VPN interface exists and is up
is_interface_up() {
    if ! ip link show pia >/dev/null 2>&1; then
        return 1
    fi

    # Check if interface has IP, peer, or is not DOWN
    if ip addr show pia 2>/dev/null | grep -q "inet " || \
       wg show pia peers 2>/dev/null | grep -q "." || \
       ! ip link show pia 2>/dev/null | grep -q "state DOWN"; then
        return 0
    fi

    return 1
}

# Verify external connectivity through VPN
check_external_connectivity() {
    # Method 1: Ping Cloudflare DNS
    if ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1; then
        return 0
    fi

    # Method 2: Ping Google DNS
    if ping -c 1 -W 3 8.8.8.8 >/dev/null 2>&1; then
        return 0
    fi

    # Method 3: Try curl as last resort
    if curl --max-time 5 -s http://1.1.1.1 >/dev/null 2>&1; then
        return 0
    fi

    return 1
}

# Perform comprehensive health check
check_vpn_health() {
    if [ "$MONITOR_DEBUG" = "true" ]; then
        echo "  ${blu}[DEBUG]${nc} Starting health check at $(date '+%H:%M:%S')" >&2
    fi

    # Check 1: Interface exists and is configured
    if ! is_interface_up; then
        [ "$MONITOR_DEBUG" = "true" ] && echo "  ${red}[DEBUG]${nc} Interface check failed" >&2
        return 1
    fi

    [ "$MONITOR_DEBUG" = "true" ] && echo "  ${grn}[DEBUG]${nc} Interface is up" >&2

    # Check 2: External connectivity with single retry
    if check_external_connectivity; then
        [ "$MONITOR_DEBUG" = "true" ] && echo "  ${grn}[DEBUG]${nc} Connectivity check passed" >&2
        return 0
    fi

    # First attempt failed, retry once
    [ "$MONITOR_DEBUG" = "true" ] && echo "  ${ylw}[DEBUG]${nc} First connectivity check failed, retrying..." >&2
    sleep 2

    if check_external_connectivity; then
        [ "$MONITOR_DEBUG" = "true" ] && echo "  ${grn}[DEBUG]${nc} Connectivity check passed on retry" >&2
        return 0
    fi

    [ "$MONITOR_DEBUG" = "true" ] && echo "  ${red}[DEBUG]${nc} All connectivity checks failed" >&2
    return 1
}

# Trigger reconnection
trigger_reconnect() {
    reconnect_attempts=$((reconnect_attempts + 1))
    local delay=$((RECONNECT_DELAY * reconnect_attempts))

    # Cap at max delay
    [ $delay -gt $MAX_RECONNECT_DELAY ] && delay=$MAX_RECONNECT_DELAY

    echo "▶ Reconnecting in ${delay}s..."
    sleep $delay

    # Signal main process to reconnect
    touch /tmp/vpn_reconnect_requested
}

# Main monitoring loop
monitor_loop() {
    local consecutive_successes=0
    local first_check=true

    while true; do
        sleep $CHECK_INTERVAL

        # Skip first check (grace period for VPN to stabilize)
        if [ "$first_check" = true ]; then
            first_check=false
            continue
        fi

        if check_vpn_health; then
            # Health check passed
            if [ $failure_count -gt 0 ]; then
                show_success "VPN connection restored"
                failure_count=0
                reconnect_attempts=0
            fi
            consecutive_successes=$((consecutive_successes + 1))
        else
            # Health check failed
            failure_count=$((failure_count + 1))
            consecutive_successes=0

            if [ $failure_count -lt $MAX_FAILURES ]; then
                show_warning "VPN health check failed (${failure_count}/${MAX_FAILURES})"
                if [ "$MONITOR_DEBUG" = "true" ] && [ $failure_count -eq 1 ]; then
                    echo "  ${ylw}ℹ${nc} Debug info:"
                    wg show pia 2>&1 | head -5 | sed 's/^/    /'
                fi
            else
                # Max failures reached, trigger reconnect
                show_error "VPN connection lost (${failure_count}/${MAX_FAILURES})"
                trigger_reconnect
                failure_count=0
            fi
        fi
    done
}

# Run if executed directly
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    monitor_loop
fi

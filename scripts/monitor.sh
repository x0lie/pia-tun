#!/bin/bash

# VPN Health Monitor - Detects failures and triggers reconnection

# Configuration
HANDSHAKE_TIMEOUT=${HANDSHAKE_TIMEOUT:-180}  # 3 minutes default (not used anymore)
CHECK_INTERVAL=${CHECK_INTERVAL:-30}          # Check every 30 seconds
MAX_FAILURES=${MAX_FAILURES:-2}               # Failures before reconnect (reduced from 3)
RECONNECT_DELAY=${RECONNECT_DELAY:-5}         # Initial reconnect delay (reduced from 10)
MAX_RECONNECT_DELAY=${MAX_RECONNECT_DELAY:-300} # Max 5 minutes
RESTART_SERVICES=${RESTART_SERVICES:-""}      # Comma-separated container names
MONITOR_DEBUG=${MONITOR_DEBUG:-false}         # Enable verbose debug output

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

# Check if handshake is stale
is_handshake_stale() {
    local last_handshake=$(get_last_handshake)
    if [ "$last_handshake" = "0" ]; then
        return 0  # No handshake = stale
    fi

    local current_time=$(date +%s)
    local time_since_handshake=$((current_time - last_handshake))

    # Handle negative values (clock skew or future timestamps)
    # If negative and small, treat as fresh (recent handshake with minor clock difference)
    if [ $time_since_handshake -lt 0 ]; then
        local abs_diff=$((-time_since_handshake))
        if [ $abs_diff -lt 300 ]; then
            # Less than 5 minutes in future, treat as fresh (likely clock skew)
            return 1
        else
            # Way in the future, something is wrong
            return 0
        fi
    fi

    if [ $time_since_handshake -gt $HANDSHAKE_TIMEOUT ]; then
        return 0  # Stale
    fi
    return 1  # Fresh
}

# Check if VPN interface exists and is up
is_interface_up() {
    # Check if interface exists first
    if ! ip link show pia >/dev/null 2>&1; then
        return 1
    fi

    # For WireGuard, check if it's operational
    # Method 1: Check it has an IP address assigned
    if ip addr show pia 2>/dev/null | grep -q "inet "; then
        return 0
    fi

    # Method 2: Check WireGuard peer exists
    if wg show pia peers 2>/dev/null | grep -q "."; then
        return 0
    fi

    # Method 3: Check link is not DOWN
    if ! ip link show pia 2>/dev/null | grep -q "state DOWN"; then
        return 0
    fi

    # All checks failed
    return 1
}

# Verify external connectivity through VPN
check_external_connectivity() {
    # Simple, fast check: Can we ping a reliable DNS server?
    # This is much faster and more reliable than curl

    # Method 1: Ping Cloudflare DNS (most reliable)
    if ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1; then
        return 0
    fi

    # Method 2: Ping Google DNS (backup)
    if ping -c 1 -W 3 8.8.8.8 >/dev/null 2>&1; then
        return 0
    fi

    # Method 3: Try curl as last resort
    if curl --max-time 5 -s http://1.1.1.1 >/dev/null 2>&1; then
        return 0
    fi

    # All methods failed
    return 1
}

# Perform comprehensive health check
check_vpn_health() {
    if [ "$MONITOR_DEBUG" = "true" ]; then
        echo "  ${blu}[DEBUG]${nc} Starting health check at $(date '+%H:%M:%S')" >&2
    fi

    # Check 1: Interface exists and is configured
    if ! is_interface_up; then
        if [ "$MONITOR_DEBUG" = "true" ]; then
            echo "  ${red}[DEBUG]${nc} Interface check failed" >&2
        fi
        return 1
    fi

    if [ "$MONITOR_DEBUG" = "true" ]; then
        echo "  ${grn}[DEBUG]${nc} Interface is up" >&2
    fi

    # Check 2: External connectivity (ONLY check - handshake doesn't matter)
    # Single retry with brief delay
    if check_external_connectivity; then
        if [ "$MONITOR_DEBUG" = "true" ]; then
            echo "  ${grn}[DEBUG]${nc} Connectivity check passed - VPN is working" >&2
        fi
        return 0
    fi

    # First attempt failed, wait a moment and retry once
    if [ "$MONITOR_DEBUG" = "true" ]; then
        echo "  ${ylw}[DEBUG]${nc} First connectivity check failed, retrying..." >&2
    fi

    sleep 2

    if check_external_connectivity; then
        if [ "$MONITOR_DEBUG" = "true" ]; then
            echo "  ${grn}[DEBUG]${nc} Connectivity check passed on retry" >&2
        fi
        return 0
    fi

    # Both attempts failed
    if [ "$MONITOR_DEBUG" = "true" ]; then
        echo "  ${red}[DEBUG]${nc} All connectivity checks failed" >&2
    fi

    return 1
}

# Restart dependent services
restart_dependent_services() {
    if [ -z "$RESTART_SERVICES" ]; then
        return 0
    fi

    show_step "Restarting dependent services..."

    IFS=',' read -ra SERVICES <<< "$RESTART_SERVICES"
    for service in "${SERVICES[@]}"; do
        service=$(echo "$service" | xargs)  # Trim whitespace
        if [ -n "$service" ]; then
            echo "  ${blu}↻${nc} Restarting container: $service"
            docker restart "$service" 2>/dev/null || echo "  ${ylw}⚠${nc} Could not restart $service (not found or no access)"
        fi
    done

    show_success "Dependent services restarted"
}

# Trigger reconnection
trigger_reconnect() {
    reconnect_attempts=$((reconnect_attempts + 1))
    local delay=$((RECONNECT_DELAY * reconnect_attempts))

    # Cap at max delay
    if [ $delay -gt $MAX_RECONNECT_DELAY ]; then
        delay=$MAX_RECONNECT_DELAY
    fi

    echo "▶ Reconnecting in ${delay}s..."
    sleep $delay

    # Signal main process to reconnect
    # We'll use a flag file that run.sh can check
    touch /tmp/vpn_reconnect_requested
}

# Main monitoring loop
monitor_loop() {
    # Don't print anything here - run.sh handles the initial announcement
    # This keeps output clean and prevents duplication

    # Track consecutive successes for logging
    consecutive_successes=0
    first_check=true

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
                # Connection restored after failures
                show_success "VPN connection restored"
                failure_count=0
                reconnect_attempts=0
            fi
            # Don't log anything during normal healthy operation (too verbose)
            consecutive_successes=$((consecutive_successes + 1))
        else
            # Health check failed
            failure_count=$((failure_count + 1))
            consecutive_successes=0

            if [ $failure_count -eq 1 ]; then
                show_warning "VPN health check failed (${failure_count}/${MAX_FAILURES})"
                # Only show debug info if debug mode is enabled
                if [ "$MONITOR_DEBUG" = "true" ]; then
                    echo "  ${ylw}ℹ${nc} Debug info:"
                    wg show pia 2>&1 | head -5 | sed 's/^/    /'
                fi
            elif [ $failure_count -lt $MAX_FAILURES ]; then
                show_warning "VPN health check failed (${failure_count}/${MAX_FAILURES})"
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

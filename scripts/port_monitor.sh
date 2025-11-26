#!/bin/bash
# Continuous port monitoring service
# Watches port file and updates torrent client API when port changes or becomes available
#
# This daemon runs continuously and:
# - Monitors /etc/wireguard/port for changes
# - Updates the configured torrent client API
# - Retries failed updates until successful
# - Handles client unavailability gracefully

set -euo pipefail

source /app/scripts/ui.sh
source /app/scripts/port_api_updater.sh

trap 'show_debug "port_monitor: Received termination signal, exiting"; exit 0' SIGTERM SIGINT

# Configuration (PORT_FILE may be set by environment)
PORT_FILE="${PORT_FILE:-/etc/wireguard/port}"

show_debug "Port monitor configuration:"
show_debug "  PORT_FILE=$PORT_FILE"
show_debug "  PORT_API_TYPE=${PORT_API_TYPE:-none}"
show_debug "  PORT_API_URL=${PORT_API_URL:-none}"

# Internal constants
readonly RETRY_INTERVAL=60  # Retry failed updates every 60 seconds

show_debug "Retry interval: ${RETRY_INTERVAL}s"

# State tracking
LAST_PORT=""
LAST_UPDATE_SUCCESS=false

# Wait for port file to exist before starting
wait_for_port_file() {
    show_debug "wait_for_port_file: Waiting for $PORT_FILE to exist"
    local waited=0
    
    while [ ! -f "$PORT_FILE" ]; do
        waited=$((waited + 5))
        [ $waited -gt 0 ] && [ $((waited % 30)) -eq 0 ] && \
            show_debug "Still waiting for port file (${waited}s elapsed)"
        sleep 5
    done
    
    show_debug "Port file found after ${waited}s"
}

# Main monitoring loop
monitor_port_changes() {
    show_debug "monitor_port_changes: Entering main loop (event-driven via pipe)"

    # Create named pipe for port change notifications
    PORT_CHANGE_PIPE="/tmp/port_change_pipe"
    [ -p "$PORT_CHANGE_PIPE" ] || mkfifo "$PORT_CHANGE_PIPE"

    # On first run, check the port file immediately (handles case where port_forwarding.sh
    # already wrote the port before port_monitor.sh started)
    if [ -f "$PORT_FILE" ]; then
        INITIAL_PORT=$(cat "$PORT_FILE" 2>/dev/null)
        if [[ -n "$INITIAL_PORT" && "$INITIAL_PORT" =~ ^[0-9]+$ ]]; then
            show_debug "Initial port from file: $INITIAL_PORT"
            if update_port_api "$INITIAL_PORT"; then
                show_success "[$(date '+%H:%M:%S')] $PORT_API_TYPE port set to $INITIAL_PORT"
                LAST_UPDATE_SUCCESS=true
            else
                show_warning "[$(date '+%H:%M:%S')] $PORT_API_TYPE not reachable, will retry"
                show_debug "Initial port update failed, will retry"
                LAST_UPDATE_SUCCESS=false
            fi
            LAST_PORT="$INITIAL_PORT"
            show_debug "State initialized: LAST_PORT=$LAST_PORT, LAST_UPDATE_SUCCESS=$LAST_UPDATE_SUCCESS"
        else
            show_debug "Port file exists but port is empty or invalid: ${INITIAL_PORT:-empty}"
        fi
    else
        show_debug "Port file not found on startup (will wait for notification)"
    fi

    local cycle=0

    while true; do
        cycle=$((cycle + 1))
        show_debug "====== Port monitor cycle #$cycle (waiting for port change) ======"

        # Block until port_forwarding.sh signals a change (with timeout for retry logic)
        # Timeout serves dual purpose: catch port changes immediately + periodic retry attempts
        if read -t $RETRY_INTERVAL -r new_port < "$PORT_CHANGE_PIPE" 2>/dev/null; then
            show_debug "Port change notification received: $new_port"
            CURRENT_PORT="$new_port"
        else
            # Timeout occurred - check if we need to retry a failed update
            CURRENT_PORT=$(cat "$PORT_FILE" 2>/dev/null)
            show_debug "Timeout waiting for port change, current port: ${CURRENT_PORT:-empty}"
        fi

        # Skip if port is empty or invalid
        if [[ -z "$CURRENT_PORT" || ! "$CURRENT_PORT" =~ ^[0-9]+$ ]]; then
            show_debug "Port is empty or invalid, waiting for next notification"
            continue
        fi

        # Determine if we should try to update
        local should_update=false
        local reason=""

        # Case 1: Port changed
        if [ "$CURRENT_PORT" != "$LAST_PORT" ]; then
            should_update=true
            reason="port changed from $LAST_PORT to $CURRENT_PORT"
            show_debug "Update trigger: $reason"
        fi

        # Case 2: Previous update failed (keep retrying)
        if [ "$LAST_UPDATE_SUCCESS" = false ] && [ -n "$LAST_PORT" ]; then
            should_update=true
            reason="retrying previous failed update"
            show_debug "Update trigger: $reason"
        fi

        # Try to update if needed
        if [ "$should_update" = true ]; then
            show_debug "Attempting API update: $reason"

            if update_port_api "$CURRENT_PORT"; then
                # Success!
                if [ "$CURRENT_PORT" != "$LAST_PORT" ]; then
                    show_success "[$(date '+%H:%M:%S')] $PORT_API_TYPE port updated to $CURRENT_PORT"
                    show_debug "Port update successful (new port: $CURRENT_PORT)"
                else
                    show_success "[$(date '+%H:%M:%S')] $PORT_API_TYPE now reachable, port set to $CURRENT_PORT"
                    show_debug "Retry successful (port: $CURRENT_PORT)"
                fi
                LAST_UPDATE_SUCCESS=true
            else
                # Failed - will retry next cycle
                if [ "$LAST_UPDATE_SUCCESS" = true ]; then
                    # Only log on first failure (not on every retry)
                    show_warning "[$(date '+%H:%M:%S')] $PORT_API_TYPE not reachable, will retry"
                    show_debug "API update failed (first failure)"
                else
                    show_debug "API update failed (continuing retry cycle)"
                fi
                LAST_UPDATE_SUCCESS=false
            fi

            LAST_PORT="$CURRENT_PORT"
            show_debug "State updated: LAST_PORT=$LAST_PORT, LAST_UPDATE_SUCCESS=$LAST_UPDATE_SUCCESS"
        else
            show_debug "No update needed (port stable: $CURRENT_PORT, last_success: $LAST_UPDATE_SUCCESS)"
        fi
    done
}

# Entry point
main() {
    show_debug "port_monitor: Starting"
    
    wait_for_port_file

    # Check if this is a restart (reconnecting marker exists)
    if [ ! -f /tmp/reconnecting ]; then
        show_info
        show_step "Port monitor starting (API: $PORT_API_TYPE)"
    else
        show_debug "Reconnecting mode detected, suppressing startup message"
    fi

    monitor_port_changes
}

# Run if executed directly
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    main
fi
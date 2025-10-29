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

# Configuration (PORT_FILE may be set by environment)
PORT_FILE="${PORT_FILE:-/etc/wireguard/port}"

# Internal constants
readonly CHECK_INTERVAL=30  # Check every 30 seconds

# State tracking
LAST_PORT=""
LAST_UPDATE_SUCCESS=false

# Wait for port file to exist before starting
wait_for_port_file() {
    while [ ! -f "$PORT_FILE" ]; do
        sleep 5
    done
}

# Main monitoring loop
monitor_port_changes() {
    while true; do
        # Read current port
        CURRENT_PORT=$(cat "$PORT_FILE" 2>/dev/null || echo "")

        # Skip if port file is empty or invalid
        if [[ -z "$CURRENT_PORT" || ! "$CURRENT_PORT" =~ ^[0-9]+$ ]]; then
            sleep $CHECK_INTERVAL
            continue
        fi

        # Determine if we should try to update
        local should_update=false
        local reason=""

        # Case 1: Port changed
        if [ "$CURRENT_PORT" != "$LAST_PORT" ]; then
            should_update=true
            reason="port changed from $LAST_PORT to $CURRENT_PORT"
        fi

        # Case 2: Previous update failed (keep retrying)
        if [ "$LAST_UPDATE_SUCCESS" = false ] && [ -n "$LAST_PORT" ]; then
            should_update=true
            reason="retrying previous failed update"
        fi

        # Try to update if needed
        if [ "$should_update" = true ]; then
            if update_port_api "$CURRENT_PORT"; then
                # Success!
                if [ "$CURRENT_PORT" != "$LAST_PORT" ]; then
                    echo "  ${grn}✓${nc} [$(date '+%H:%M:%S')] $PORT_API_TYPE port updated to $CURRENT_PORT"
                else
                    echo "  ${grn}✓${nc} [$(date '+%H:%M:%S')] $PORT_API_TYPE now reachable, port set to $CURRENT_PORT"
                fi
                LAST_UPDATE_SUCCESS=true
            else
                # Failed - will retry next cycle
                if [ "$LAST_UPDATE_SUCCESS" = true ]; then
                    # Only log on first failure (not on every retry)
                    echo "  ${ylw}⚠${nc} [$(date '+%H:%M:%S')] $PORT_API_TYPE not reachable, will retry"
                fi
                LAST_UPDATE_SUCCESS=false
            fi

            LAST_PORT="$CURRENT_PORT"
        fi

        sleep $CHECK_INTERVAL
    done
}

# Entry point
main() {
    wait_for_port_file

    # Check if this is a restart (reconnecting marker exists)
    if [ ! -f /tmp/reconnecting ]; then
        show_step "Port monitor starting (API: $PORT_API_TYPE)"
    fi

    monitor_port_changes
}

# Run if executed directly
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    main
fi

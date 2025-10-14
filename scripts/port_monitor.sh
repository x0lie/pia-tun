#!/bin/bash

# Continuous port monitoring service
# Watches port file and updates API whenever it changes or client becomes available

source /app/scripts/ui.sh
source /app/scripts/port_api_updater.sh

PORT_FILE="${PORT_FILE:-/etc/wireguard/port}"
LAST_PORT=""
LAST_UPDATE_SUCCESS=false
CHECK_INTERVAL=30  # Check every 30 seconds

# Wait for port file to exist
while [ ! -f "$PORT_FILE" ]; do
    sleep 5
done

# Check if this is a restart (file exists with reconnecting marker)
if [ -f /tmp/reconnecting ]; then
    echo "[$(date '+%H:%M:%S')] ${blu}↻${nc} Port monitor restarted"
else
    show_step "Port monitor starting (API: $PORT_API_TYPE)"
fi

while true; do
    # Read current port
    CURRENT_PORT=$(cat "$PORT_FILE" 2>/dev/null)
    
    # Skip if port file is empty or invalid
    if [[ -z "$CURRENT_PORT" || ! "$CURRENT_PORT" =~ ^[0-9]+$ ]]; then
        sleep $CHECK_INTERVAL
        continue
    fi
    
    # Determine if we should try to update
    SHOULD_UPDATE=false
    
    # Case 1: Port changed
    if [ "$CURRENT_PORT" != "$LAST_PORT" ]; then
        SHOULD_UPDATE=true
        REASON="port changed from $LAST_PORT to $CURRENT_PORT"
    fi
    
    # Case 2: Previous update failed (keep retrying)
    if [ "$LAST_UPDATE_SUCCESS" = false ] && [ -n "$LAST_PORT" ]; then
        SHOULD_UPDATE=true
        REASON="retrying previous failed update"
    fi
    
    # Try to update if needed
    if [ "$SHOULD_UPDATE" = true ]; then
        if update_port_api "$CURRENT_PORT"; then
            # Success!
            if [ "$CURRENT_PORT" != "$LAST_PORT" ]; then
                echo "[$(date '+%H:%M:%S')] ${grn}✓${nc} $PORT_API_TYPE port updated to $CURRENT_PORT"
            else
                echo "[$(date '+%H:%M:%S')] ${grn}✓${nc} $PORT_API_TYPE now reachable, port set to $CURRENT_PORT"
            fi
            LAST_UPDATE_SUCCESS=true
        else
            # Failed - will retry next cycle
            if [ "$LAST_UPDATE_SUCCESS" = true ]; then
                # Only log on first failure (not on every retry)
                echo "[$(date '+%H:%M:%S')] ${ylw}⚠${nc} $PORT_API_TYPE not reachable, will retry"
            fi
            LAST_UPDATE_SUCCESS=false
        fi
        
        LAST_PORT="$CURRENT_PORT"
    fi
    
    sleep $CHECK_INTERVAL
done

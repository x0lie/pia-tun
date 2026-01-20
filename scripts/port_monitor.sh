#!/bin/bash
# Continuous port monitoring service
# Watches port file and updates torrent client API when port changes or becomes available
#
# This daemon runs continuously and:
# - Monitors /run/pia-tun/port for changes
# - Updates the configured torrent client API
# - Retries failed updates until successful
# - Handles client unavailability gracefully

set -euo pipefail

source /app/scripts/ui.sh
source /app/scripts/killswitch.sh
source /app/scripts/port_sync.sh

trap 'show_debug "port_monitor: Received termination signal, exiting"; exit 0' SIGTERM SIGINT

# Configuration (PORT_FILE may be set by environment)
PORT_FILE="${PORT_FILE:-/run/pia-tun/port}"

show_debug "Port monitor configuration:"
show_debug "  PORT_FILE=$PORT_FILE"
show_debug "  PORT_SYNC_CLIENT=${PORT_SYNC_CLIENT:-none}"
show_debug "  PORT_SYNC_URL=${PORT_SYNC_URL:-none}"
show_debug "  PORT_SYNC_CMD=${PORT_SYNC_CMD:+set}"

# Determine display name for logging (used for startup message and failure messages)
if [ -n "${PORT_SYNC_CLIENT:-}" ] && [ -n "${PORT_SYNC_CMD:-}" ]; then
    SYNC_DISPLAY_NAME="$PORT_SYNC_CLIENT + custom command"
elif [ -n "${PORT_SYNC_CLIENT:-}" ]; then
    SYNC_DISPLAY_NAME="$PORT_SYNC_CLIENT"
else
    SYNC_DISPLAY_NAME="custom command"
fi
show_debug "  SYNC_DISPLAY_NAME=$SYNC_DISPLAY_NAME"

# Internal constants
readonly RETRY_INTERVAL=60  # Retry failed updates every 60 seconds

show_debug "Retry interval: ${RETRY_INTERVAL}s"

# State tracking - track each method independently
LAST_PORT=""
LAST_CLIENT_SUCCESS=""  # Empty = not yet attempted, true = last succeeded, false = last failed
LAST_CMD_SUCCESS=""     # Empty = not yet attempted, true = last succeeded, false = last failed

# Determine which methods are configured (set once at startup)
HAS_CLIENT=false
HAS_CMD=false
# PORT_SYNC_URL is auto-detected from PORT_SYNC_CLIENT, so just check PORT_SYNC_CLIENT
[ -n "${PORT_SYNC_CLIENT:-}" ] && HAS_CLIENT=true
[ -n "${PORT_SYNC_CMD:-}" ] && HAS_CMD=true

show_debug "Configured methods: HAS_CLIENT=$HAS_CLIENT, HAS_CMD=$HAS_CMD"

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

    # Open pipe on FD 3 in read-write mode to prevent blocking on open()
    # Opening in read-write mode (O_RDWR) doesn't block waiting for a writer
    exec 3<> "$PORT_CHANGE_PIPE"
    show_debug "Opened pipe on FD 3"

    # On first run, check the port file immediately (handles case where port_forwarding.sh
    # already wrote the port before port_monitor.sh started)
    if [ -f "$PORT_FILE" ]; then
        INITIAL_PORT=$(cat "$PORT_FILE" 2>/dev/null)
        if [[ -n "$INITIAL_PORT" && "$INITIAL_PORT" =~ ^[0-9]+$ ]]; then
            show_debug "Initial port from file: $INITIAL_PORT"

            # Update firewall to allow forwarded port
            if ! add_forwarded_port_to_input "$INITIAL_PORT"; then
                show_warning "Failed to add port forwarding rule to firewall (may not be ready yet)"
            fi

            # Attempt update (will set success markers)
            update_port_api "$INITIAL_PORT" || true  # Don't exit on failure (set -e)

            # Check which methods succeeded
            local client_succeeded=false
            local cmd_succeeded=false
            [ -f /tmp/port_sync_client_success ] && client_succeeded=true
            [ -f /tmp/port_sync_cmd_success ] && cmd_succeeded=true

            # Display success for each method
            if [ "$HAS_CLIENT" = true ] && [ "$client_succeeded" = true ]; then
                show_success "[$(date '+%Y-%m-%d %H:%M:%S')] ${PORT_SYNC_CLIENT} port updated"
                LAST_CLIENT_SUCCESS="true"
            fi
            if [ "$HAS_CMD" = true ] && [ "$cmd_succeeded" = true ]; then
                show_success "[$(date '+%Y-%m-%d %H:%M:%S')] Custom command executed successfully"
                LAST_CMD_SUCCESS="true"
            fi

            # Display first failure for each method that failed
            if [ "$HAS_CLIENT" = true ] && [ "$client_succeeded" = false ]; then
                show_warning "[$(date '+%Y-%m-%d %H:%M:%S')] ${PORT_SYNC_CLIENT} not reachable, will retry"
                LAST_CLIENT_SUCCESS="false"
            fi
            if [ "$HAS_CMD" = true ] && [ "$cmd_succeeded" = false ]; then
                show_warning "[$(date '+%Y-%m-%d %H:%M:%S')] Custom command failed, will retry"
                LAST_CMD_SUCCESS="false"
            fi

            LAST_PORT="$INITIAL_PORT"
            show_debug "State initialized: LAST_PORT=$LAST_PORT, client_success=$LAST_CLIENT_SUCCESS, cmd_success=$LAST_CMD_SUCCESS"
        else
            show_debug "Port file exists but port is empty or invalid: ${INITIAL_PORT:-empty}"
        fi
    else
        show_debug "Port file not found on startup (will wait for notification)"
    fi

    # Main event loop - switches between event-only mode and retry mode
    while true; do
        # Determine if we have any failures that need retrying
        local has_failures=false
        if [ "$HAS_CLIENT" = true ] && [ "$LAST_CLIENT_SUCCESS" = "false" ]; then
            has_failures=true
        fi
        if [ "$HAS_CMD" = true ] && [ "$LAST_CMD_SUCCESS" = "false" ]; then
            has_failures=true
        fi

        if [ "$has_failures" = false ]; then
            # All methods successful - pure event-driven mode (no timeout)
            show_debug "All sync methods successful - entering event-only mode (blocking indefinitely on pipe)"

            if read -r new_port <&3 2>/dev/null; then
                show_debug "Port change notification: $new_port"

                # Validate port
                if [[ -n "$new_port" && "$new_port" =~ ^[0-9]+$ ]]; then
                    # Update firewall
                    if ! add_forwarded_port_to_input "$new_port"; then
                        show_warning "Failed to add port forwarding rule to firewall"
                    fi

                    # Sync the new port
                    update_port_api "$new_port" || true  # Don't exit on failure (set -e)

                    # Check results and update state
                    process_sync_results "$new_port" true
                    LAST_PORT="$new_port"
                else
                    show_debug "Invalid port received: ${new_port:-empty}"
                fi
            else
                show_debug "Pipe read failed or closed, restarting loop"
            fi
        else
            # Has failures - retry mode with 60s timeout
            show_debug "Entering retry mode (has failures, 60s timeout)"
            local retry_cycle=0

            while true; do
                retry_cycle=$((retry_cycle + 1))
                show_debug "====== Retry cycle #$retry_cycle (timeout=${RETRY_INTERVAL}s) ======"

                if read -t "$RETRY_INTERVAL" -r new_port <&3 2>/dev/null; then
                    # Port change received
                    show_debug "Port change notification: $new_port"

                    if [[ -n "$new_port" && "$new_port" =~ ^[0-9]+$ ]]; then
                        # Update firewall
                        if ! add_forwarded_port_to_input "$new_port"; then
                            show_warning "Failed to add port forwarding rule to firewall"
                        fi

                        # Sync the new port
                        update_port_api "$new_port" || true  # Don't exit on failure (set -e)
                        process_sync_results "$new_port" true
                        LAST_PORT="$new_port"
                    fi
                else
                    # Timeout - retry existing port
                    show_debug "Timeout after ${RETRY_INTERVAL}s - retrying failed methods"
                    CURRENT_PORT=$(cat "$PORT_FILE" 2>/dev/null)

                    if [[ -n "$CURRENT_PORT" && "$CURRENT_PORT" =~ ^[0-9]+$ ]]; then
                        update_port_api "$CURRENT_PORT" || true  # Don't exit on failure (set -e)
                        process_sync_results "$CURRENT_PORT" false
                    fi
                fi

                # Check if all methods are now successful
                local all_good=true
                if [ "$HAS_CLIENT" = true ] && [ "$LAST_CLIENT_SUCCESS" = "false" ]; then
                    all_good=false
                fi
                if [ "$HAS_CMD" = true ] && [ "$LAST_CMD_SUCCESS" = "false" ]; then
                    all_good=false
                fi

                if [ "$all_good" = true ]; then
                    show_debug "All methods recovered - exiting retry mode"
                    break
                fi
            done
        fi
    done
}

# Process sync results and update state
# Args: $1=port, $2=is_port_change (true/false)
process_sync_results() {
    local port=$1
    local is_port_change=$2

    # Check which methods succeeded
    local client_succeeded=false
    local cmd_succeeded=false
    [ -f /tmp/port_sync_client_success ] && client_succeeded=true
    [ -f /tmp/port_sync_cmd_success ] && cmd_succeeded=true

    # Handle client results
    if [ "$HAS_CLIENT" = true ]; then
        if [ "$client_succeeded" = true ]; then
            # Success
            if [ "$LAST_CLIENT_SUCCESS" = "false" ]; then
                show_success "[$(date '+%Y-%m-%d %H:%M:%S')] ${PORT_SYNC_CLIENT} now reachable, port updated"
            elif [ "$is_port_change" = "true" ] || [ -z "$LAST_CLIENT_SUCCESS" ]; then
                show_success "[$(date '+%Y-%m-%d %H:%M:%S')] ${PORT_SYNC_CLIENT} port updated"
            fi
            LAST_CLIENT_SUCCESS="true"
        else
            # Failed
            if [ "$LAST_CLIENT_SUCCESS" != "false" ]; then
                show_warning "[$(date '+%Y-%m-%d %H:%M:%S')] ${PORT_SYNC_CLIENT} not reachable, will retry"
            fi
            LAST_CLIENT_SUCCESS="false"
        fi
    fi

    # Handle custom command results
    if [ "$HAS_CMD" = true ]; then
        if [ "$cmd_succeeded" = true ]; then
            # Success
            if [ "$LAST_CMD_SUCCESS" = "false" ]; then
                show_success "[$(date '+%Y-%m-%d %H:%M:%S')] Custom command now successful"
            elif [ "$is_port_change" = "true" ] || [ -z "$LAST_CMD_SUCCESS" ]; then
                show_success "[$(date '+%Y-%m-%d %H:%M:%S')] Custom command executed successfully"
            fi
            LAST_CMD_SUCCESS="true"
        else
            # Failed
            if [ "$LAST_CMD_SUCCESS" != "false" ]; then
                show_warning "[$(date '+%Y-%m-%d %H:%M:%S')] Custom command failed, will retry"
            fi
            LAST_CMD_SUCCESS="false"
        fi
    fi

    show_debug "Sync results: port=$port, client=$LAST_CLIENT_SUCCESS, cmd=$LAST_CMD_SUCCESS"
}

# Entry point
main() {
    show_debug "port_monitor: Starting"
    
    wait_for_port_file

    # Check if this is a restart (reconnecting marker exists)
    if [ ! -f /tmp/reconnecting ]; then
        show_info
        show_step "Port monitor starting (sync: $SYNC_DISPLAY_NAME)"
    else
        show_debug "Reconnecting mode detected, suppressing startup message"
    fi

    monitor_port_changes
}

# Run if executed directly
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    main
fi
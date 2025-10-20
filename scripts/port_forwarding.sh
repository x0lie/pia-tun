#!/bin/bash

source /app/scripts/ui.sh
source /app/scripts/port_api_updater.sh

TOKEN=$(cat /tmp/pia_token)
PEER_IP=$(cat /tmp/client_ip)
META_CN=$(cat /tmp/meta_cn)
PF_GATEWAY=$(cat /tmp/pf_gateway)

# Configuration
BIND_INTERVAL=${PF_BIND_INTERVAL:-600}
SIGNATURE_REFRESH_DAYS=${PF_SIGNATURE_REFRESH_DAYS:-7}
SIGNATURE_SAFETY_HOURS=${PF_SIGNATURE_SAFETY_HOURS:-24}
DEBUG_PF=${DEBUG_PF:-false}

# OPTIMIZED: Debug logging with early exit to avoid overhead
debug_log() {
    [ "$DEBUG_PF" != "true" ] && return 0  # Early exit - no string processing!
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] ${blu}[PF-DEBUG]${nc} $*"
}

[[ -z "$PF_GATEWAY" || "$PF_GATEWAY" = "null" ]] && {
    show_error "No PF gateway available"
    touch /tmp/port_forwarding_complete
    exit 1
}

get_signature() {
    debug_log "Requesting signature from $PF_GATEWAY..."
    sleep 2
    
    local result=$(curl -s -m 10 -k --interface pia -G \
        --data-urlencode "token=$TOKEN" \
        "https://$PF_GATEWAY:19999/getSignature" 2>&1)
    
    echo "$result" > /tmp/pf_response
    
    if [ ! -s /tmp/pf_response ]; then
        debug_log "ERROR: Empty response from getSignature"
        return 1
    fi
    
    if ! jq -e '.status' /tmp/pf_response >/dev/null 2>&1; then
        debug_log "ERROR: Invalid JSON in response"
        debug_log "Response: $(cat /tmp/pf_response)"
        return 1
    fi
    
    local status=$(jq -r '.status' /tmp/pf_response)
    debug_log "getSignature status: $status"
    
    [ "$status" = "OK" ]
}

bind_port() {
    local payload="$1"
    local signature="$2"
    
    debug_log "Calling bindPort..."
    debug_log "Payload length: ${#payload} bytes"
    debug_log "Signature length: ${#signature} bytes"
    
    local result=$(curl -s -m 10 -k --interface pia -G \
        --data-urlencode "payload=$payload" \
        --data-urlencode "signature=$signature" \
        "https://$PF_GATEWAY:19999/bindPort" 2>&1)
    
    echo "$result" > /tmp/pf_bind_response
    
    if [ ! -s /tmp/pf_bind_response ]; then
        debug_log "ERROR: Empty response from bindPort"
        return 1
    fi
    
    local status=$(jq -r '.status // empty' /tmp/pf_bind_response 2>/dev/null)
    debug_log "bindPort status: $status"
    
    if [ "$status" != "OK" ]; then
        debug_log "ERROR: bindPort failed"
        debug_log "Response: $(cat /tmp/pf_bind_response)"
        return 1
    fi
    
    debug_log "✓ bindPort successful"
    return 0
}

# Parse response and extract all needed fields
parse_pf_response() {
    jq -r '[
        (.payload | @base64d | fromjson | .port),
        .payload,
        .signature,
        ((.payload | @base64d | fromjson | .expires_at) | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601)
    ] | @tsv' /tmp/pf_response
}

# Calculate seconds until expiration
seconds_until_expiry() {
    local expires_at="$1"
    local current_time=$(date +%s)
    echo $((expires_at - current_time))
}

# Format timestamp for display
format_timestamp() {
    local timestamp="$1"
    date -d "@$timestamp" '+%Y-%m-%d %H:%M:%S' 2>/dev/null || date -r "$timestamp" '+%Y-%m-%d %H:%M:%S'
}

# Initial signature acquisition with retry
show_step "Acquiring port forward signature..."
MAX_RETRIES=5
retry=0
while [ $retry -lt $MAX_RETRIES ]; do
    if get_signature; then
        debug_log "Signature acquired on attempt $((retry + 1))"
        break
    fi
    retry=$((retry + 1))
    [ $retry -lt $MAX_RETRIES ] && {
        debug_log "Signature attempt $retry failed, retrying in $((5 * retry))s..."
        sleep $((5 * retry))
    }
done

[ $retry -ge $MAX_RETRIES ] && {
    show_error "Port forwarding failed after $MAX_RETRIES attempts"
    show_vpn_connected_warning
    touch /tmp/port_forwarding_complete
    tail -f /dev/null
    exit 0
}

# Parse initial response
debug_log "Parsing initial response..."
read -r PORT PAYLOAD SIGNATURE EXPIRES_AT <<< "$(parse_pf_response)"

debug_log "Parsed values:"
debug_log "  Port: $PORT"
debug_log "  Expires at: $EXPIRES_AT ($(format_timestamp "$EXPIRES_AT"))"

[[ -z "$PORT" || "$PORT" = "null" ]] && {
    show_error "Failed to extract port from response"
    show_vpn_connected_warning
    touch /tmp/port_forwarding_complete
    tail -f /dev/null
    exit 0
}

# Initial bind
debug_log "Performing initial bind..."
if bind_port "$PAYLOAD" "$SIGNATURE"; then
    debug_log "✓ Initial bind successful"
else
    show_warning "Initial bind failed, but continuing..."
fi

# Always write to file immediately
echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"
debug_log "Port written to ${PORT_FILE:-/etc/wireguard/port}"

# Try API update if enabled (with retry logic)
if [ "$PORT_API_ENABLED" = "true" ]; then
    debug_log "Attempting API update..."
    
    # Try up to 3 times with exponential backoff
    api_success=false
    for attempt in 1 2 3; do
        if update_port_api "$PORT"; then
            api_success=true
            break
        fi
        
        if [ $attempt -lt 3 ]; then
            debug_log "API update attempt $attempt failed, retrying in $((attempt * 2))s..."
            sleep $((attempt * 2))
        fi
    done
    
    if $api_success; then
        show_success "Port: ${grn}${bold}${PORT}${nc}"
        show_success "Updated via: File + API ($PORT_API_TYPE)"
    else
        show_success "Port: ${grn}${bold}${PORT}${nc}"
        show_success "Updated via: File only (API: $PORT_API_TYPE unreachable)"
        debug_log "API update failed after 3 attempts, port_monitor.sh will retry"
    fi
else
    show_success "Port: ${grn}${bold}${PORT}${nc}"
    show_success "Updated via: File"
fi

# Calculate and display expiration info
SECONDS_UNTIL_EXPIRY=$(seconds_until_expiry "$EXPIRES_AT")
DAYS_UNTIL_EXPIRY=$((SECONDS_UNTIL_EXPIRY / 86400))
EXPIRY_DATE=$(format_timestamp "$EXPIRES_AT")

show_success "Port expires: $EXPIRY_DATE (in ${DAYS_UNTIL_EXPIRY} days)"
show_success "Keep-alive: Bind refresh every $((BIND_INTERVAL / 60)) minutes"

# Only show signature refresh interval if it's not 0 (testing mode)
if [ "$SIGNATURE_REFRESH_DAYS" -gt 0 ] 2>/dev/null; then
    show_success "Signature refresh: Every $SIGNATURE_REFRESH_DAYS days"
fi

if [ "$DEBUG_PF" = "true" ]; then
    echo ""
    echo "  ${blu}[PF-DEBUG]${nc} Debug mode enabled"
    echo "  ${blu}[PF-DEBUG]${nc} BIND_INTERVAL=${BIND_INTERVAL}s ($((BIND_INTERVAL / 60))min)"
    echo "  ${blu}[PF-DEBUG]${nc} SIGNATURE_REFRESH_DAYS=${SIGNATURE_REFRESH_DAYS}"
    echo "  ${blu}[PF-DEBUG]${nc} SIGNATURE_SAFETY_HOURS=${SIGNATURE_SAFETY_HOURS}"
    echo "  ${blu}[PF-DEBUG]${nc} Next bind in ${BIND_INTERVAL}s"
fi

show_vpn_connected
touch /tmp/port_forwarding_complete

echo ""

# Main loop with dual-purpose refreshing
LAST_SIGNATURE_TIME=$(date +%s)
BIND_COUNT=0
SIGNATURE_REFRESH_COUNT=0

debug_log "Entering main keep-alive loop (interval: ${BIND_INTERVAL}s)"

while true; do
    # Sleep for bind interval
    debug_log "Sleeping ${BIND_INTERVAL}s until next bind..."
    sleep $BIND_INTERVAL
    
    CURRENT_TIME=$(date +%s)
    TIME_SINCE_SIGNATURE=$((CURRENT_TIME - LAST_SIGNATURE_TIME))
    SECONDS_UNTIL_EXPIRY=$(seconds_until_expiry "$EXPIRES_AT")
    
    debug_log "Woke up for bind cycle #$((BIND_COUNT + 1))"
    debug_log "Time since last signature: ${TIME_SINCE_SIGNATURE}s ($((TIME_SINCE_SIGNATURE / 86400)) days)"
    debug_log "Seconds until expiry: $SECONDS_UNTIL_EXPIRY ($((SECONDS_UNTIL_EXPIRY / 86400)) days)"
    
    # Check if we need a new signature
    NEED_NEW_SIGNATURE=false
    REASON=""
    
    # Reason 1: Scheduled refresh
    if [ $TIME_SINCE_SIGNATURE -ge $((SIGNATURE_REFRESH_DAYS * 86400)) ]; then
        NEED_NEW_SIGNATURE=true
        REASON="scheduled refresh (${SIGNATURE_REFRESH_DAYS}-day interval)"
        debug_log "Signature refresh needed: $REASON"
    fi
    
    # Reason 2: Signature expiring soon
    if [ -n "$SECONDS_UNTIL_EXPIRY" ] && [ "$SECONDS_UNTIL_EXPIRY" -le $((SIGNATURE_SAFETY_HOURS * 3600)) ]; then
        NEED_NEW_SIGNATURE=true
        REASON="signature expiring soon (within ${SIGNATURE_SAFETY_HOURS}h)"
        debug_log "Signature refresh needed: $REASON"
    fi
    
    # Get new signature if needed
    if [ "$NEED_NEW_SIGNATURE" = "true" ]; then
        SIGNATURE_REFRESH_COUNT=$((SIGNATURE_REFRESH_COUNT + 1))
        
        # Only show refresh message if not in rapid test mode
        if [ "$SIGNATURE_REFRESH_DAYS" -gt 0 ] 2>/dev/null || [ "$DEBUG_PF" = "true" ]; then
            echo "[$(date '+%Y-%m-%d %H:%M:%S')] ${blu}↻${nc} Getting new signature ($REASON)..."
        fi
        
        if get_signature; then
            debug_log "New signature obtained successfully"
            
            # Parse new response
            read -r NEW_PORT NEW_PAYLOAD NEW_SIGNATURE NEW_EXPIRES_AT <<< "$(parse_pf_response)"
            
            debug_log "New signature values:"
            debug_log "  Port: $NEW_PORT"
            debug_log "  Expires at: $NEW_EXPIRES_AT ($(format_timestamp "$NEW_EXPIRES_AT"))"
            
            # Check if port changed
            if [ "$NEW_PORT" != "$PORT" ]; then
                # Only show port change message if not in rapid test mode
                if [ "$SIGNATURE_REFRESH_DAYS" -gt 0 ] 2>/dev/null || [ "$DEBUG_PF" = "true" ]; then
                    echo "  ${ylw}ℹ${nc} Port changed: $PORT → $NEW_PORT"
                fi
                PORT=$NEW_PORT
                echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"
                
                # Update API if enabled (with retry)
                if [ "$PORT_API_ENABLED" = "true" ]; then
                    debug_log "Updating API with new port..."
                    api_success=false
                    for attempt in 1 2 3; do
                        if update_port_api "$PORT"; then
                            api_success=true
                            break
                        fi
                        if [ $attempt -lt 3 ]; then
                            debug_log "API update attempt $attempt failed, retrying in $((attempt * 2))s..."
                            sleep $((attempt * 2))
                        fi
                    done
                    
                    if ! $api_success && [ "$DEBUG_PF" = "true" ]; then
                        echo "  ${ylw}⚠${nc} API update failed, port_monitor.sh will retry"
                    fi
                fi
            fi
            
            # Update signature data
            PAYLOAD=$NEW_PAYLOAD
            SIGNATURE=$NEW_SIGNATURE
            EXPIRES_AT=$NEW_EXPIRES_AT
            LAST_SIGNATURE_TIME=$CURRENT_TIME
            
            # Calculate new expiry info
            SECONDS_UNTIL_EXPIRY=$(seconds_until_expiry "$EXPIRES_AT")
            DAYS_UNTIL_EXPIRY=$((SECONDS_UNTIL_EXPIRY / 86400))
            EXPIRY_DATE=$(format_timestamp "$EXPIRES_AT")
            
            # Bind with new signature
            debug_log "Binding with new signature..."
            if bind_port "$PAYLOAD" "$SIGNATURE"; then
                # Only show success message if not in rapid test mode
                if [ "$SIGNATURE_REFRESH_DAYS" -gt 0 ] 2>/dev/null || [ "$DEBUG_PF" = "true" ]; then
                    show_success "Signature refreshed, port ${grn}${PORT}${nc} expires: $EXPIRY_DATE"
                fi
            else
                show_warning "Got new signature but bind failed"
            fi
        else
            show_warning "Signature refresh failed, continuing with existing signature"
            debug_log "Will retry signature refresh in next cycle"
        fi
        
        # Only add blank line if we showed messages
        if [ "$SIGNATURE_REFRESH_DAYS" -gt 0 ] 2>/dev/null || [ "$DEBUG_PF" = "true" ]; then
            echo ""
        fi
    else
        # Regular keep-alive bind (no new signature needed)
        BIND_COUNT=$((BIND_COUNT + 1))
        
        debug_log "Performing keep-alive bind #${BIND_COUNT}..."
        
        if bind_port "$PAYLOAD" "$SIGNATURE"; then
            debug_log "✓ Keep-alive bind #${BIND_COUNT} successful"
            
            # Only log every 6th bind (once per hour) to avoid log spam
            if [ $((BIND_COUNT % 6)) -eq 0 ]; then
                HOURS_LOGGED=$((BIND_COUNT * BIND_INTERVAL / 3600))
                echo "[$(date '+%Y-%m-%d %H:%M:%S')] ${grn}✓${nc} Keep-alive active (${HOURS_LOGGED}h, bind #${BIND_COUNT})"
            fi
        else
            show_warning "Keep-alive bind #${BIND_COUNT} failed"
            debug_log "ERROR: Bind failed, will retry in ${BIND_INTERVAL}s"
            
            # If bind fails multiple times in a row, maybe we need a new signature?
            if [ $((BIND_COUNT % 3)) -eq 0 ]; then
                echo "  ${ylw}ℹ${nc} Multiple bind failures detected, will request new signature next cycle"
                # Force signature refresh by setting last signature time to 0
                LAST_SIGNATURE_TIME=0
            fi
        fi
    fi
done

#!/bin/bash

source /app/scripts/ui.sh
source /app/scripts/port_api_updater.sh

TOKEN=$(cat /tmp/pia_token)
PEER_IP=$(cat /tmp/client_ip)
META_CN=$(cat /tmp/meta_cn)
PF_GATEWAY=$(cat /tmp/pf_gateway)

# Configuration
BIND_INTERVAL=${PF_BIND_INTERVAL:-600}
SIGNATURE_REFRESH_DAYS=${PF_SIGNATURE_REFRESH_DAYS:-30}
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
    local max_retries=3
    local retry_count=0
    local backoff=2

    while [ $retry_count -lt $max_retries ]; do
        retry_count=$((retry_count + 1))
        debug_log "Requesting signature from $PF_GATEWAY (attempt $retry_count/$max_retries)..."
        
        if [ $retry_count -gt 1 ]; then
            debug_log "Waiting $backoff seconds before retry..."
            sleep $backoff
            backoff=$((backoff * 2))  # Exponential backoff
        else
            sleep 2
        fi
        
        local result=$(curl -s -m 10 -k --interface pia -G \
            --data-urlencode "token=$TOKEN" \
            "https://$PF_GATEWAY:19999/getSignature" 2>&1)
        
        echo "$result" > /tmp/pf_response
        
        if [ ! -s /tmp/pf_response ]; then
            debug_log "ERROR: Empty response from getSignature (attempt $retry_count)"
            if [ $retry_count -eq $max_retries ]; then
                return 1
            fi
            continue
        fi
        
        if ! jq -e '.status' /tmp/pf_response >/dev/null 2>&1; then
            debug_log "ERROR: Invalid JSON in response (attempt $retry_count)"
            debug_log "Response: $(cat /tmp/pf_response)"
            if [ $retry_count -eq $max_retries ]; then
                return 1
            fi
            continue
        fi
        
        local status=$(jq -r '.status' /tmp/pf_response)
        debug_log "getSignature status: $status (attempt $retry_count)"
        
        if [ "$status" = "OK" ]; then
            return 0
        fi
        
        # Non-OK status: treat as failure, retry if attempts remain
        debug_log "Non-OK status '$status' from getSignature (attempt $retry_count)"
        if [ $retry_count -eq $max_retries ]; then
            return 1
        fi
    done
    
    # If we exit the loop without returning, it's a final failure
    debug_log "All $max_retries attempts failed; triggering reconnection"
    return 1
}

bind_port() {
    local payload="$1"
    local signature="$2"
    
    local max_retries=3
    local retry_count=0
    local backoff=2  # Initial backoff in seconds for retries

    while [ $retry_count -lt $max_retries ]; do
        retry_count=$((retry_count + 1))
        debug_log "Calling bindPort (attempt $retry_count/$max_retries)..."
        debug_log "Payload length: ${#payload} bytes"
        debug_log "Signature length: ${#signature} bytes"
        
        if [ $retry_count -gt 1 ]; then
            debug_log "Waiting $backoff seconds before retry..."
            sleep $backoff
            backoff=$((backoff * 2))  # Exponential backoff
        fi
        
        local result=$(curl -s -m 10 -k --interface pia -G \
            --data-urlencode "payload=$payload" \
            --data-urlencode "signature=$signature" \
            "https://$PF_GATEWAY:19999/bindPort" 2>&1)
        
        echo "$result" > /tmp/pf_bind_response
        
        if [ ! -s /tmp/pf_bind_response ]; then
            debug_log "ERROR: Empty response from bindPort (attempt $retry_count)"
            if [ $retry_count -eq $max_retries ]; then
                return 1
            fi
            continue
        fi
        
        local status=$(jq -r '.status // empty' /tmp/pf_bind_response 2>/dev/null)
        debug_log "bindPort status: $status (attempt $retry_count)"
        
        if [ "$status" = "OK" ]; then
            debug_log "✓ bindPort successful"
            return 0
        fi
        
        # Non-OK status: treat as failure, retry if attempts remain
        debug_log "ERROR: bindPort failed (attempt $retry_count)"
        debug_log "Response: $(cat /tmp/pf_bind_response)"
        if [ $retry_count -eq $max_retries ]; then
            return 1
        fi
    done
    
    # If we exit the loop without returning, it's a final failure
    debug_log "All $max_retries attempts failed; triggering reconnection"
    return 1
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

# Send webhook notification for port changes
notify_webhook() {
    local port="$1"
    local ip="${2:-}"

    [ -z "${WEBHOOK_URL-}" ] && return 0

    debug_log "Sending webhook notification for port $port..."

    # Prepare JSON payload
    local payload
    if [ -n "$ip" ]; then
        payload=$(jq -n --arg port "$port" --arg ip "$ip" \
            '{port: $port, ip: $ip, timestamp: (now | todateiso8601)}')
    else
        payload=$(jq -n --arg port "$port" \
            '{port: $port, timestamp: (now | todateiso8601)}')
    fi

    # Send webhook (async, don't block on failure)
    (
        response=$(curl -s -m 10 -X POST \
            -H "Content-Type: application/json" \
            -d "$payload" \
            "$WEBHOOK_URL" 2>&1)

        if [ $? -eq 0 ]; then
            debug_log "Webhook notification sent successfully"
        else
            debug_log "Webhook notification failed: $response"
        fi
    ) &
}

# Initial signature acquisition with retry
echo ""
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

# Get public VPN IP for webhook (async to avoid blocking)
(
    VPN_IP=$(timeout 5 curl -s --interface pia https://api.ipify.org 2>/dev/null || echo "")
    notify_webhook "$PORT" "$VPN_IP"
) &

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

# Main loop with dual-purpose refreshing
BIND_COUNT=0
SIGNATURE_REFRESH_COUNT=0
CONSECUTIVE_BIND_FAILURES=0
CONSECUTIVE_REFRESH_FAILURES=0  # New: Separate counter for refresh fails
FORCE_REFRESH=false
LAST_SIGNATURE_TIME=$(date +%s)

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
    debug_log "Consecutive bind failures: $CONSECUTIVE_BIND_FAILURES"
    debug_log "Consecutive refresh failures: $CONSECUTIVE_REFRESH_FAILURES"
    
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
    
    # Reason 3: Forced due to bind failures
    if [ "$FORCE_REFRESH" = true ]; then
        NEED_NEW_SIGNATURE=true
        REASON="forced by bind failures"
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
            
            # Parse new response
            read -r NEW_PORT NEW_PAYLOAD NEW_SIGNATURE NEW_EXPIRES_AT <<< "$(parse_pf_response)"
            
            show_success "New signature acquired with port: $NEW_PORT"
            
            # Check if port changed
            if [ "$NEW_PORT" != "$PORT" ]; then
                # Only show port change message if not in rapid test mode
                if [ "$SIGNATURE_REFRESH_DAYS" -gt 0 ] 2>/dev/null || [ "$DEBUG_PF" = "true" ]; then
                    echo "  ${ylw}ℹ${nc} Port changed: $PORT → $NEW_PORT"
                fi
                PORT=$NEW_PORT
                echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"
                
                # Notify webhook of port change (async)
                (
                    VPN_IP=$(timeout 5 curl -s --interface pia https://api.ipify.org 2>/dev/null || echo "")
                    notify_webhook "$PORT" "$VPN_IP"
                ) &

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
            CONSECUTIVE_BIND_FAILURES=0
            CONSECUTIVE_REFRESH_FAILURES=0  # Reset on refresh success
            FORCE_REFRESH=false
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
                # Note: Bind fail after fresh sig is odd—still reset counters, but consider logging harder
                CONSECUTIVE_BIND_FAILURES=1  # Start a new streak
            fi
        else
            echo ""
            show_error "Signature refresh failed, reconnecting..."
            
            CONSECUTIVE_REFRESH_FAILURES=$((CONSECUTIVE_REFRESH_FAILURES + 1))
            FORCE_REFRESH=false  # Clear to avoid immediate re-trigger
            
            # Immediate reconnect on any refresh failure
            if [ $CONSECUTIVE_REFRESH_FAILURES -ge 1 ]; then
                debug_log "ERROR: Signature refresh failed ($CONSECUTIVE_REFRESH_FAILURES times). Triggering reconnection."
                touch /tmp/pf_signature_failed
                # Exit the loop to let the parent process handle reconnection
                exit 1
            fi
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
            CONSECUTIVE_BIND_FAILURES=0
            CONSECUTIVE_REFRESH_FAILURES=0  # Reset both on bind success too
            FORCE_REFRESH=false
            
        else
            show_warning "Keep-alive bind #${BIND_COUNT} failed"
            debug_log "ERROR: Bind failed, will retry in ${BIND_INTERVAL}s"
            CONSECUTIVE_BIND_FAILURES=$((CONSECUTIVE_BIND_FAILURES + 1))
            
            # Trigger force refresh only after 2 consecutive bind failures
            if [ $CONSECUTIVE_BIND_FAILURES -ge 2 ]; then
                echo "  ${ylw}ℹ${nc} Multiple bind failures detected ($CONSECUTIVE_BIND_FAILURES), will request new signature next cycle"
                FORCE_REFRESH=true
            fi
        fi
    fi
done
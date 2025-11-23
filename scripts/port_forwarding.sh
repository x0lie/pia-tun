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

show_debug "Port forwarding configuration:"
show_debug "  BIND_INTERVAL=${BIND_INTERVAL}s ($((BIND_INTERVAL / 60))min)"
show_debug "  SIGNATURE_REFRESH_DAYS=${SIGNATURE_REFRESH_DAYS}"
show_debug "  SIGNATURE_SAFETY_HOURS=${SIGNATURE_SAFETY_HOURS}"
show_debug "  PF_GATEWAY=${PF_GATEWAY}"
show_debug "  TOKEN length: ${#TOKEN}"
show_debug "  PEER_IP: ${PEER_IP}"
show_debug "  META_CN: ${META_CN}"

# OPTIMIZED: Debug logging with early exit to avoid overhead
debug_log() {
    show_debug "$*"
}

if [[ -z "$PF_GATEWAY" || "$PF_GATEWAY" = "null" ]]; then
    show_error "No PF gateway available"
    show_debug "PF_GATEWAY is empty or null, cannot proceed"
    touch /tmp/port_forwarding_complete
    exit 1
fi

get_signature() {
    local max_retries=3
    local retry_count=0
    local backoff=2

    show_debug "get_signature: Starting (max_retries: $max_retries)"

    while [ $retry_count -lt $max_retries ]; do
        retry_count=$((retry_count + 1))
        show_debug "Requesting signature from $PF_GATEWAY (attempt $retry_count/$max_retries)"
        
        if [ $retry_count -gt 1 ]; then
            show_debug "Waiting ${backoff}s before retry..."
            sleep $backoff
            backoff=$((backoff * 2))  # Exponential backoff
        else
            show_debug "Initial 2s delay before first request"
            sleep 2
        fi
        
        show_debug "Executing: curl -s -m 10 -k --interface pia https://$PF_GATEWAY:19999/getSignature"
        local result=$(curl -s -m 10 -k --interface pia -G \
            --data-urlencode "token=$TOKEN" \
            "https://$PF_GATEWAY:19999/getSignature" 2>&1)
        
        echo "$result" > /tmp/pf_response
        show_debug "Response saved to /tmp/pf_response (size: $(wc -c < /tmp/pf_response 2>/dev/null || echo 0) bytes)"
        
        if [ ! -s /tmp/pf_response ]; then
            show_debug "ERROR: Empty response from getSignature (attempt $retry_count)"
            if [ $retry_count -eq $max_retries ]; then
                show_debug "Max retries reached, returning failure"
                return 1
            fi
            continue
        fi
        
        if ! jq -e '.status' /tmp/pf_response >/dev/null 2>&1; then
            show_debug "ERROR: Invalid JSON in response (attempt $retry_count)"
            show_debug "Response content: $(cat /tmp/pf_response)"
            if [ $retry_count -eq $max_retries ]; then
                show_debug "Max retries reached, returning failure"
                return 1
            fi
            continue
        fi
        
        local status=$(jq -r '.status' /tmp/pf_response)
        show_debug "getSignature status: '$status' (attempt $retry_count)"
        
        if [ "$status" = "OK" ]; then
            show_debug "getSignature successful"
            return 0
        fi
        
        # Non-OK status: treat as failure, retry if attempts remain
        show_debug "Non-OK status '$status' from getSignature (attempt $retry_count)"
        if [ $retry_count -eq $max_retries ]; then
            show_debug "Max retries reached with non-OK status, returning failure"
            return 1
        fi
    done
    
    # If we exit the loop without returning, it's a final failure
    show_debug "All $max_retries attempts failed; returning failure"
    return 1
}

bind_port() {
    local payload="$1"
    local signature="$2"
    
    local max_retries=3
    local retry_count=0
    local backoff=2  # Initial backoff in seconds for retries

    show_debug "bind_port: Starting (max_retries: $max_retries)"
    show_debug "  Payload length: ${#payload} bytes"
    show_debug "  Signature length: ${#signature} bytes"

    while [ $retry_count -lt $max_retries ]; do
        retry_count=$((retry_count + 1))
        show_debug "Calling bindPort (attempt $retry_count/$max_retries)"
        
        if [ $retry_count -gt 1 ]; then
            show_debug "Waiting ${backoff}s before retry..."
            sleep $backoff
            backoff=$((backoff * 2))  # Exponential backoff
        fi
        
        show_debug "Executing: curl -s -m 10 -k --interface pia https://$PF_GATEWAY:19999/bindPort"
        local result=$(curl -s -m 10 -k --interface pia -G \
            --data-urlencode "payload=$payload" \
            --data-urlencode "signature=$signature" \
            "https://$PF_GATEWAY:19999/bindPort" 2>&1)
        
        echo "$result" > /tmp/pf_bind_response
        show_debug "Bind response saved to /tmp/pf_bind_response (size: $(wc -c < /tmp/pf_bind_response 2>/dev/null || echo 0) bytes)"
        
        if [ ! -s /tmp/pf_bind_response ]; then
            show_debug "ERROR: Empty response from bindPort (attempt $retry_count)"
            if [ $retry_count -eq $max_retries ]; then
                show_debug "Max retries reached, returning failure"
                return 1
            fi
            continue
        fi
        
        local status=$(jq -r '.status // empty' /tmp/pf_bind_response 2>/dev/null)
        show_debug "bindPort status: '$status' (attempt $retry_count)"
        
        if [ "$status" = "OK" ]; then
            return 0
        fi
        
        # Non-OK status: treat as failure, retry if attempts remain
        show_debug "ERROR: bindPort failed with status '$status' (attempt $retry_count)"
        show_debug "Response content: $(cat /tmp/pf_bind_response)"
        if [ $retry_count -eq $max_retries ]; then
            show_debug "Max retries reached, returning failure"
            return 1
        fi
    done
    
    # If we exit the loop without returning, it's a final failure
    show_debug "All $max_retries attempts failed; returning failure"
    return 1
}

# Parse response and extract all needed fields
parse_pf_response() {
    local parsed=$(jq -r '[
        (.payload | @base64d | fromjson | .port),
        .payload,
        .signature,
        ((.payload | @base64d | fromjson | .expires_at) | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601)
    ] | @tsv' /tmp/pf_response)
    
    show_debug "Parsed response (raw): $parsed"
    echo "$parsed"
}

# Calculate seconds until expiration
seconds_until_expiry() {
    local expires_at="$1"
    local current_time=$(date +%s)
    local seconds=$((expires_at - current_time))
    show_debug "seconds_until_expiry: expires_at=$expires_at, current=$current_time, diff=${seconds}s"
    echo $seconds
}

# Format timestamp for display
format_timestamp() {
    local timestamp="$1"
    local formatted=$(date -d "@$timestamp" '+%Y-%m-%d %H:%M:%S' 2>/dev/null || date -r "$timestamp" '+%Y-%m-%d %H:%M:%S')
    show_debug "format_timestamp: $timestamp -> $formatted"
    echo "$formatted"
}

# Send webhook notification for port changes
notify_webhook() {
    local port="$1"
    local ip="${2:-}"

    [ -z "${WEBHOOK_URL-}" ] && { show_debug "No WEBHOOK_URL configured, skipping notification"; return 0; }

    show_debug "Sending webhook notification for port $port (ip: ${ip:-none})..."

    # Prepare JSON payload
    local payload
    if [ -n "$ip" ]; then
        payload=$(jq -n --arg port "$port" --arg ip "$ip" \
            '{port: $port, ip: $ip, timestamp: (now | todateiso8601)}')
        show_debug "Webhook payload (with IP): $payload"
    else
        payload=$(jq -n --arg port "$port" \
            '{port: $port, timestamp: (now | todateiso8601)}')
        show_debug "Webhook payload (no IP): $payload"
    fi

    # Send webhook (async, don't block on failure)
    (
        show_debug "Executing webhook POST to $WEBHOOK_URL"
        response=$(curl -s -m 10 -X POST \
            -H "Content-Type: application/json" \
            -d "$payload" \
            "$WEBHOOK_URL" 2>&1)

        if [ $? -eq 0 ]; then
            show_debug "Webhook notification sent successfully: $response"
        else
            show_debug "Webhook notification failed: $response"
        fi
    ) &
}

# Initial signature acquisition with retry
show_info
show_step "Acquiring port forward signature..."
show_debug "Starting initial signature acquisition (max retries: 5)"

MAX_RETRIES=5
retry=0
while [ $retry -lt $MAX_RETRIES ]; do
    show_debug "Initial signature attempt $((retry + 1))/$MAX_RETRIES"
    
    if get_signature; then
        break
    fi
    
    retry=$((retry + 1))
    if [ $retry -lt $MAX_RETRIES ]; then
        local wait_time=$((5 * retry))
        show_debug "Signature attempt $retry failed, retrying in ${wait_time}s..."
        sleep $wait_time
    fi
done

if [ $retry -ge $MAX_RETRIES ]; then
    show_error "Port forwarding failed after $MAX_RETRIES attempts"
    show_debug "Exhausted all initial signature attempts, giving up"
    show_vpn_connected_warning
    touch /tmp/port_forwarding_complete
    tail -f /dev/null
    exit 0
fi

# Parse initial response
show_debug "Parsing initial signature response..."
read -r PORT PAYLOAD SIGNATURE EXPIRES_AT <<< "$(parse_pf_response)"

show_debug "Initial parsed values:"
show_debug "  Port: $PORT"
show_debug "  Payload length: ${#PAYLOAD} bytes"
show_debug "  Signature length: ${#SIGNATURE} bytes"
show_debug "  Expires at: $EXPIRES_AT ($(format_timestamp "$EXPIRES_AT"))"

if [[ -z "$PORT" || "$PORT" = "null" ]]; then
    show_error "Failed to extract port from response"
    show_debug "PORT is empty or null after parsing"
    show_vpn_connected_warning
    touch /tmp/port_forwarding_complete
    tail -f /dev/null
    exit 0
fi

# Initial bind
show_debug "Performing initial bind..."
if bind_port "$PAYLOAD" "$SIGNATURE"; then
    show_debug "Initial bind successful"
else
    show_warning "Initial bind failed, but continuing..."
    show_debug "Initial bind failure is non-fatal, continuing with port announcement"
fi

# Always write to file immediately
show_debug "Writing port $PORT to ${PORT_FILE:-/etc/wireguard/port}"
echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"

# Get public VPN IP for webhook (async to avoid blocking)
(
    show_debug "Fetching public VPN IP for webhook notification..."
    VPN_IP=$(timeout 5 curl -s --interface pia https://api.ipify.org 2>/dev/null || show_info)
    show_debug "Public VPN IP: ${VPN_IP:-failed to retrieve}"
    notify_webhook "$PORT" "$VPN_IP"
) &

# Try API update if enabled (with retry logic)
if [ "$PORT_API_ENABLED" = "true" ]; then
    show_debug "PORT_API_ENABLED=true, attempting API update..."
    show_debug "API type: ${PORT_API_TYPE:-none}"
    
    # Try up to 3 times with exponential backoff
    api_success=false
    for attempt in 1 2 3; do
        show_debug "API update attempt $attempt/3"
        
        if update_port_api "$PORT"; then
            show_debug "API update successful on attempt $attempt"
            api_success=true
            break
        fi
        
        if [ "$attempt" -lt 3 ]; then
            show_debug "API update attempt $attempt failed, retrying in $((attempt * 2))s..."
            sleep $((attempt * 2))
        fi

        # if [ $attempt -lt 3 ]; then
        #     local wait_time=$((attempt * 2))
        #     show_debug "API update attempt $attempt failed, retrying in ${wait_time}s..."
        #     sleep $wait_time
        # fi
    done
    
    if $api_success; then
        show_success "Port: ${grn}${bold}${PORT}${nc}"
        show_success "Updated via: File + API ($PORT_API_TYPE)"
    else
        show_success "Port: ${grn}${bold}${PORT}${nc}"
        show_success "Updated via: File only (API: $PORT_API_TYPE unreachable)"
        show_debug "API update failed after 3 attempts, port_monitor.sh will retry"
    fi
else
    show_debug "PORT_API_ENABLED=false, skipping API update"
    show_success "Port: ${grn}${bold}${PORT}${nc}"
    show_success "Updated via: File"
fi

# Calculate and display expiration info
SECONDS_UNTIL_EXPIRY=$(seconds_until_expiry "$EXPIRES_AT")
DAYS_UNTIL_EXPIRY=$((SECONDS_UNTIL_EXPIRY / 86400))
EXPIRY_DATE=$(format_timestamp "$EXPIRES_AT")

show_debug "Expiration info: $SECONDS_UNTIL_EXPIRY seconds ($DAYS_UNTIL_EXPIRY days)"
show_success "Port expires: $EXPIRY_DATE (in ${DAYS_UNTIL_EXPIRY} days)"
show_success "Keep-alive: Bind refresh every $((BIND_INTERVAL / 60)) minutes"

# Only show signature refresh interval if it's not 0 (testing mode)
if [ "$SIGNATURE_REFRESH_DAYS" -gt 0 ] 2>/dev/null; then
    show_success "Signature refresh: Every $SIGNATURE_REFRESH_DAYS days"
else
    show_debug "Signature refresh disabled (SIGNATURE_REFRESH_DAYS=0, testing mode)"
fi

show_vpn_connected
touch /tmp/port_forwarding_complete
show_debug "Port forwarding setup complete, entering main loop"

# Main loop with dual-purpose refreshing
BIND_COUNT=0
SIGNATURE_REFRESH_COUNT=0
CONSECUTIVE_BIND_FAILURES=0
CONSECUTIVE_REFRESH_FAILURES=0  # New: Separate counter for refresh fails
FORCE_REFRESH=false
LAST_SIGNATURE_TIME=$(date +%s)

show_debug "Main loop initialized:"
show_debug "  BIND_COUNT=0"
show_debug "  SIGNATURE_REFRESH_COUNT=0"
show_debug "  CONSECUTIVE_BIND_FAILURES=0"
show_debug "  CONSECUTIVE_REFRESH_FAILURES=0"
show_debug "  LAST_SIGNATURE_TIME=$(format_timestamp $LAST_SIGNATURE_TIME)"

while true; do
    # Sleep for bind interval
    show_debug "Sleeping ${BIND_INTERVAL}s until next bind..."
    sleep $BIND_INTERVAL
    
    CURRENT_TIME=$(date +%s)
    TIME_SINCE_SIGNATURE=$((CURRENT_TIME - LAST_SIGNATURE_TIME))
    SECONDS_UNTIL_EXPIRY=$(seconds_until_expiry "$EXPIRES_AT")
    
    show_debug "====== Keep-alive cycle #$((BIND_COUNT + 1)) ======"
    show_debug "Current time: $(format_timestamp $CURRENT_TIME)"
    show_debug "Time since last signature: ${TIME_SINCE_SIGNATURE}s ($((TIME_SINCE_SIGNATURE / 86400)) days)"
    show_debug "Seconds until expiry: $SECONDS_UNTIL_EXPIRY ($((SECONDS_UNTIL_EXPIRY / 86400)) days)"
    show_debug "Consecutive bind failures: $CONSECUTIVE_BIND_FAILURES"
    show_debug "Consecutive refresh failures: $CONSECUTIVE_REFRESH_FAILURES"
    show_debug "Force refresh flag: $FORCE_REFRESH"
    
    # Check if we need a new signature
    NEED_NEW_SIGNATURE=false
    REASON=""
    
    # Reason 1: Scheduled refresh
    if [ $TIME_SINCE_SIGNATURE -ge $((SIGNATURE_REFRESH_DAYS * 86400)) ]; then
        NEED_NEW_SIGNATURE=true
        REASON="scheduled refresh (${SIGNATURE_REFRESH_DAYS}-day interval)"
        show_debug "Signature refresh trigger: $REASON"
    fi
    
    # Reason 2: Signature expiring soon
    if [ -n "$SECONDS_UNTIL_EXPIRY" ] && [ "$SECONDS_UNTIL_EXPIRY" -le $((SIGNATURE_SAFETY_HOURS * 3600)) ]; then
        NEED_NEW_SIGNATURE=true
        REASON="signature expiring soon (within ${SIGNATURE_SAFETY_HOURS}h)"
        show_debug "Signature refresh trigger: $REASON"
    fi
    
    # Reason 3: Forced due to bind failures
    if [ "$FORCE_REFRESH" = true ]; then
        NEED_NEW_SIGNATURE=true
        REASON="forced by bind failures"
        show_debug "Signature refresh trigger: $REASON"
    fi
    
    # Get new signature if needed
    if [ "$NEED_NEW_SIGNATURE" = "true" ]; then
        SIGNATURE_REFRESH_COUNT=$((SIGNATURE_REFRESH_COUNT + 1))
        show_debug "Initiating signature refresh #$SIGNATURE_REFRESH_COUNT (reason: $REASON)"
        
        # Only show refresh message if not in rapid test mode
        if [ "$SIGNATURE_REFRESH_DAYS" -gt 0 ] 2>/dev/null; then
            echo "[$(date '+%Y-%m-%d %H:%M:%S')] ${blu}↻${nc} Getting new signature ($REASON)..."
        fi
        
        if get_signature; then
            show_debug "New signature acquired successfully"
            
            # Parse new response
            read -r NEW_PORT NEW_PAYLOAD NEW_SIGNATURE NEW_EXPIRES_AT <<< "$(parse_pf_response)"
            
            show_debug "New signature values:"
            show_debug "  Port: $NEW_PORT"
            show_debug "  Payload length: ${#NEW_PAYLOAD} bytes"
            show_debug "  Signature length: ${#NEW_SIGNATURE} bytes"
            show_debug "  Expires at: $NEW_EXPIRES_AT ($(format_timestamp "$NEW_EXPIRES_AT"))"
            
            show_success "New signature acquired with port: $NEW_PORT"
            
            # Check if port changed
            if [ "$NEW_PORT" != "$PORT" ]; then
                show_debug "Port changed: $PORT -> $NEW_PORT"
                
                # Only show port change message if not in rapid test mode
                if [ "$SIGNATURE_REFRESH_DAYS" -gt 0 ] 2>/dev/null; then
                    echo "  ${ylw}ℹ${nc} Port changed: $PORT → $NEW_PORT"
                fi
                
                PORT=$NEW_PORT
                show_debug "Writing new port to ${PORT_FILE:-/etc/wireguard/port}"
                echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"
                
                # Notify webhook of port change (async)
                (
                    show_debug "Fetching VPN IP for port change webhook..."
                    VPN_IP=$(timeout 5 curl -s --interface pia https://api.ipify.org 2>/dev/null || show_info)
                    show_debug "VPN IP for webhook: ${VPN_IP:-failed}"
                    notify_webhook "$PORT" "$VPN_IP"
                ) &

                # Update API if enabled (with retry)
                if [ "$PORT_API_ENABLED" = "true" ]; then
                    show_debug "Updating API with new port..."
                    api_success=false
                    for attempt in 1 2 3; do
                        show_debug "API update attempt $attempt/3 (port: $PORT)"
                        
                        if update_port_api "$PORT"; then
                            show_debug "API update successful on attempt $attempt"
                            api_success=true
                            break
                        fi
                        
                        if [ "$attempt" -lt 3 ]; then
                            show_debug "API update attempt $attempt failed, retrying in $((attempt * 2))s..."
                            sleep $((attempt * 2))
                        fi
                    done
                    
                    if ! $api_success; then
                        show_debug "API update failed after 3 attempts, port_monitor.sh will retry"
                        echo "  ${ylw}⚠${nc} API update failed, port_monitor.sh will retry"
                    fi
                fi
            else
                show_debug "Port unchanged: $PORT"
            fi
            
            # Update signature data
            PAYLOAD=$NEW_PAYLOAD
            SIGNATURE=$NEW_SIGNATURE
            EXPIRES_AT=$NEW_EXPIRES_AT
            CONSECUTIVE_BIND_FAILURES=0
            CONSECUTIVE_REFRESH_FAILURES=0  # Reset on refresh success
            FORCE_REFRESH=false
            LAST_SIGNATURE_TIME=$CURRENT_TIME
            
            show_debug "Signature data updated, counters reset"
            show_debug "  CONSECUTIVE_BIND_FAILURES=0"
            show_debug "  CONSECUTIVE_REFRESH_FAILURES=0"
            show_debug "  FORCE_REFRESH=false"
            show_debug "  LAST_SIGNATURE_TIME=$(format_timestamp $LAST_SIGNATURE_TIME)"
            
            # Calculate new expiry info
            SECONDS_UNTIL_EXPIRY=$(seconds_until_expiry "$EXPIRES_AT")
            DAYS_UNTIL_EXPIRY=$((SECONDS_UNTIL_EXPIRY / 86400))
            EXPIRY_DATE=$(format_timestamp "$EXPIRES_AT")
            
            # Bind with new signature
            show_debug "Binding with new signature..."
            if bind_port "$PAYLOAD" "$SIGNATURE"; then
                show_debug "Bind with new signature successful"
                # Only show success message if not in rapid test mode
                if [ "$SIGNATURE_REFRESH_DAYS" -gt 0 ] 2>/dev/null; then
                    show_success "Signature refreshed, port ${grn}${PORT}${nc} expires: $EXPIRY_DATE"
                fi
            else
                show_warning "Got new signature but bind failed"
                show_debug "Bind failed after fresh signature (unusual), starting new failure streak"
                # Note: Bind fail after fresh sig is odd—still reset counters, but consider logging harder
                CONSECUTIVE_BIND_FAILURES=1  # Start a new streak
            fi
        else
            show_info
            show_error "Signature refresh failed, reconnecting..."
            show_debug "Signature refresh failed after retries"
            
            CONSECUTIVE_REFRESH_FAILURES=$((CONSECUTIVE_REFRESH_FAILURES + 1))
            FORCE_REFRESH=false  # Clear to avoid immediate re-trigger
            
            show_debug "Incremented CONSECUTIVE_REFRESH_FAILURES to $CONSECUTIVE_REFRESH_FAILURES"
            
            # Immediate reconnect on any refresh failure
            if [ $CONSECUTIVE_REFRESH_FAILURES -ge 1 ]; then
                show_debug "Triggering VPN reconnection due to signature refresh failure"
                touch /tmp/pf_signature_failed
                # Exit the loop to let the parent process handle reconnection
                exit 1
            fi
        fi
        
        # Only add blank line if we showed messages
        if [ "$SIGNATURE_REFRESH_DAYS" -gt 0 ] 2>/dev/null; then
            show_info
        fi
    else
        # Regular keep-alive bind (no new signature needed)
        BIND_COUNT=$((BIND_COUNT + 1))
        
        show_debug "Performing regular keep-alive bind #${BIND_COUNT}..."
        
        if bind_port "$PAYLOAD" "$SIGNATURE"; then
            show_debug "Keep-alive bind #${BIND_COUNT} successful"
            CONSECUTIVE_BIND_FAILURES=0
            CONSECUTIVE_REFRESH_FAILURES=0  # Reset both on bind success too
            FORCE_REFRESH=false
            
            show_debug "Bind success, all failure counters reset"
            
        else
            show_warning "Keep-alive bind #${BIND_COUNT} failed"
            show_debug "Bind failed, incrementing failure counter"
            CONSECUTIVE_BIND_FAILURES=$((CONSECUTIVE_BIND_FAILURES + 1))
            
            show_debug "CONSECUTIVE_BIND_FAILURES now at $CONSECUTIVE_BIND_FAILURES"
            
            # Trigger force refresh only after 2 consecutive bind failures
            if [ $CONSECUTIVE_BIND_FAILURES -ge 2 ]; then
                echo "  ${ylw}ℹ${nc} Multiple bind failures detected ($CONSECUTIVE_BIND_FAILURES), will request new signature next cycle"
                show_debug "Setting FORCE_REFRESH=true due to $CONSECUTIVE_BIND_FAILURES consecutive failures"
                FORCE_REFRESH=true
            fi
        fi
    fi
    
    show_debug "====== End of cycle #$((BIND_COUNT + SIGNATURE_REFRESH_COUNT)) ======"
done
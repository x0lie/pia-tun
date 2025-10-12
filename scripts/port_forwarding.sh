#!/bin/bash

source /app/scripts/ui.sh

TOKEN=$(cat /tmp/pia_token)
PEER_IP=$(cat /tmp/client_ip)
META_CN=$(cat /tmp/meta_cn)
PF_GATEWAY=$(cat /tmp/pf_gateway)

[[ -z "$PF_GATEWAY" || "$PF_GATEWAY" = "null" ]] && {
    show_error "No PF gateway available"
    touch /tmp/port_forwarding_complete
    exit 1
}

get_signature() {
    sleep 2
    curl -s -m 10 -k --interface pia -G \
        --data-urlencode "token=$TOKEN" \
        "https://$PF_GATEWAY:19999/getSignature" \
        -o /tmp/pf_response 2>&1 || return 1

    [ ! -s /tmp/pf_response ] && return 1
    jq -e '.status' /tmp/pf_response >/dev/null 2>&1 || return 1
    [ "$(jq -r '.status' /tmp/pf_response)" = "OK" ]
}

bind_port() {
    curl -s -m 10 -k --interface pia -G \
        --data-urlencode "payload=$1" \
        --data-urlencode "signature=$2" \
        "https://$PF_GATEWAY:19999/bindPort" \
        -o /tmp/pf_bind_response 2>&1 || return 1

    [ "$(jq -r '.status // empty' /tmp/pf_bind_response 2>/dev/null)" = "OK" ]
}

# Initial signature with retry
MAX_RETRIES=5
retry=0
while [ $retry -lt $MAX_RETRIES ]; do
    get_signature 2>/dev/null && break
    retry=$((retry + 1))
    [ $retry -lt $MAX_RETRIES ] && sleep $((5 * retry))
done

[ $retry -ge $MAX_RETRIES ] && {
    show_error "Port forwarding failed after $MAX_RETRIES attempts"
    show_vpn_connected_warning
    touch /tmp/port_forwarding_complete
    tail -f /dev/null
    exit 0
}

# Parse all fields at once (single jq call)
PF_DATA=$(jq -r '[(.payload | @base64d | fromjson | .port), .payload, .signature, (.payload | @base64d | fromjson | .expires_at)] | @tsv' /tmp/pf_response)
read -r PORT PAYLOAD SIGNATURE EXPIRES_AT <<< "$PF_DATA"

[[ -z "$PORT" || "$PORT" = "null" ]] && {
    show_error "Failed to extract port from response"
    show_vpn_connected_warning
    touch /tmp/port_forwarding_complete
    tail -f /dev/null
    exit 0
}

bind_port "$PAYLOAD" "$SIGNATURE" >/dev/null 2>&1
echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"

show_success "Port: ${grn}${bold}${PORT}${nc}"
show_vpn_connected
touch /tmp/port_forwarding_complete

show_step "Refreshing port in 24 hours..."
echo ""

REFRESH_COUNT=0
while true; do
    sleep 86400
    REFRESH_COUNT=$((REFRESH_COUNT + 1))
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] ${blu}↻${nc} Refreshing port signature (day #${REFRESH_COUNT})..."

    get_signature 2>/dev/null || { show_warning "Refresh failed, will retry tomorrow"; continue; }

    PF_REFRESH=$(jq -r '[.payload, .signature, (.payload | @base64d | fromjson | .port)] | @tsv' /tmp/pf_response)
    read -r PAYLOAD SIGNATURE NEW_PORT <<< "$PF_REFRESH"

    [ "$NEW_PORT" != "$PORT" ] && {
        echo "  ${ylw}ℹ${nc} Port changed: $PORT → $NEW_PORT"
        PORT=$NEW_PORT
        echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"
    }

    bind_port "$PAYLOAD" "$SIGNATURE" >/dev/null 2>&1 && \
        show_success "Port ${grn}${PORT}${nc} refreshed successfully" || \
        show_warning "Bind failed, will retry tomorrow"
    echo ""
done

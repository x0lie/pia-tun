#!/bin/bash

set -e

# Load required data
TOKEN=$(cat /tmp/pia_token)
PEER_IP=$(cat /tmp/client_ip)
META_CN=$(cat /tmp/meta_cn)
PF_GATEWAY=$(cat /tmp/pf_gateway)

if [ -z "$PF_GATEWAY" ] || [ "$PF_GATEWAY" = "null" ]; then
  echo "Error: No PF gateway available. This region may not support port forwarding."
  exit 1
fi

echo "Starting port forwarding for $META_CN (gateway: $PF_GATEWAY)..."

# Function to get port signature
get_signature() {
  local response
  echo "Requesting initial port signature..."
  
  # Use the internal PF gateway IP (already on the VPN network)
  response=$(curl -s -m 10 \
    --interface pia \
    -G \
    --data-urlencode "token=$TOKEN" \
    "http://$PF_GATEWAY:19999/getSignature" 2>&1)
  
  if [ $? -ne 0 ]; then
    echo "getSignature curl failed: $response"
    return 1
  fi
  
  if ! echo "$response" | jq -e '.status' >/dev/null 2>&1; then
    echo "getSignature response is not valid JSON: $response"
    return 1
  fi
  
  local status=$(echo "$response" | jq -r '.status')
  if [ "$status" != "OK" ]; then
    echo "getSignature failed: $response"
    return 1
  fi
  
  echo "$response"
}

# Function to bind port
bind_port() {
  local payload="$1"
  local response
  
  echo "Binding port..."
  response=$(curl -s -m 10 \
    --interface pia \
    -G \
    --data-urlencode "payload=$payload" \
    --data-urlencode "signature=$SIGNATURE" \
    "http://$PF_GATEWAY:19999/bindPort" 2>&1)
  
  if [ $? -ne 0 ]; then
    echo "bindPort curl failed: $response"
    return 1
  fi
  
  local status=$(echo "$response" | jq -r '.status // empty')
  if [ "$status" != "OK" ]; then
    echo "bindPort failed: $response"
    return 1
  fi
  
  echo "Port bound successfully"
}

# Get initial signature
SIG_RESPONSE=$(get_signature)
if [ $? -ne 0 ]; then
  echo "Initial port request failed. Waiting 5s and retrying once..."
  sleep 5
  SIG_RESPONSE=$(get_signature)
  if [ $? -ne 0 ]; then
    echo "Port forwarding setup failed after retry. Exiting."
    exit 1
  fi
fi

PORT=$(echo "$SIG_RESPONSE" | jq -r '.payload' | base64 -d | jq -r '.port')
PAYLOAD=$(echo "$SIG_RESPONSE" | jq -r '.payload')
SIGNATURE=$(echo "$SIG_RESPONSE" | jq -r '.signature')
EXPIRES_AT=$(echo "$SIG_RESPONSE" | jq -r '.payload' | base64 -d | jq -r '.expires_at')

echo "Port forwarding initialized:"
echo "  Forwarded Port: $PORT"
echo "  Expires at: $(date -d @$EXPIRES_AT 2>/dev/null || date -r $EXPIRES_AT 2>/dev/null || echo $EXPIRES_AT)"

# Bind the port
bind_port "$PAYLOAD"

# Save port to file
echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"
echo "Port saved to ${PORT_FILE:-/etc/wireguard/port}"

# Refresh loop (every 15 minutes)
while true; do
  sleep 900  # 15 minutes
  
  echo "Refreshing port signature..."
  SIG_RESPONSE=$(get_signature)
  if [ $? -ne 0 ]; then
    echo "Warning: Failed to refresh signature. Will retry next cycle."
    continue
  fi
  
  PAYLOAD=$(echo "$SIG_RESPONSE" | jq -r '.payload')
  SIGNATURE=$(echo "$SIG_RESPONSE" | jq -r '.signature')
  NEW_PORT=$(echo "$PAYLOAD" | base64 -d | jq -r '.port')
  
  if [ "$NEW_PORT" != "$PORT" ]; then
    echo "Port changed from $PORT to $NEW_PORT"
    PORT=$NEW_PORT
    echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"
  fi
  
  bind_port "$PAYLOAD"
  echo "Port $PORT refreshed at $(date)"
done

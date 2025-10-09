#!/bin/bash

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

# Function to get port signature - writes response to file
get_signature() {
  echo "Requesting port signature..."
  
  # Wait for network to be ready
  sleep 2
  
  # Make HTTPS request to PF gateway
  curl -s -m 10 -k \
    --interface pia \
    -G \
    --data-urlencode "token=$TOKEN" \
    "https://$PF_GATEWAY:19999/getSignature" \
    -o /tmp/pf_response 2>&1
  
  local curl_exit=$?
  
  if [ $curl_exit -ne 0 ]; then
    echo "Error: curl failed with exit code $curl_exit"
    return 1
  fi
  
  if [ ! -s /tmp/pf_response ]; then
    echo "Error: Empty response from gateway"
    return 1
  fi
  
  # Validate JSON
  if ! jq -e '.status' /tmp/pf_response >/dev/null 2>&1; then
    echo "Error: Invalid JSON response"
    cat /tmp/pf_response
    return 1
  fi
  
  local status=$(jq -r '.status' /tmp/pf_response)
  if [ "$status" != "OK" ]; then
    echo "Error: Request failed with status: $status"
    cat /tmp/pf_response
    return 1
  fi
  
  return 0
}

# Function to bind port
bind_port() {
  local payload="$1"
  local signature="$2"
  
  echo "Binding port..."
  
  curl -s -m 10 -k \
    --interface pia \
    -G \
    --data-urlencode "payload=$payload" \
    --data-urlencode "signature=$signature" \
    "https://$PF_GATEWAY:19999/bindPort" \
    -o /tmp/pf_bind_response 2>&1
  
  local curl_exit=$?
  
  if [ $curl_exit -ne 0 ]; then
    echo "Warning: bindPort curl failed (exit $curl_exit)"
    return 1
  fi
  
  local status=$(jq -r '.status // empty' /tmp/pf_bind_response 2>/dev/null)
  if [ "$status" != "OK" ]; then
    echo "Warning: bindPort failed"
    cat /tmp/pf_bind_response
    return 1
  fi
  
  echo "Port bound successfully"
  return 0
}

# Retry logic for initial signature
MAX_RETRIES=5
retry=0

while [ $retry -lt $MAX_RETRIES ]; do
  echo "Attempt $((retry + 1))/$MAX_RETRIES..."
  
  if get_signature; then
    echo "Successfully got signature"
    break
  fi
  
  retry=$((retry + 1))
  if [ $retry -lt $MAX_RETRIES ]; then
    wait_time=$((5 * retry))
    echo "Failed. Retrying in ${wait_time}s..."
    sleep $wait_time
  fi
done

if [ $retry -ge $MAX_RETRIES ]; then
  echo "Port forwarding setup failed after $MAX_RETRIES attempts."
  echo "Keeping container alive without port forwarding..."
  tail -f /dev/null
  exit 0
fi

# Parse response
PORT=$(jq -r '.payload' /tmp/pf_response | base64 -d | jq -r '.port')
PAYLOAD=$(jq -r '.payload' /tmp/pf_response)
SIGNATURE=$(jq -r '.signature' /tmp/pf_response)
EXPIRES_AT=$(jq -r '.payload' /tmp/pf_response | base64 -d | jq -r '.expires_at')

if [ -z "$PORT" ] || [ "$PORT" = "null" ]; then
  echo "Error: Failed to extract port from response"
  cat /tmp/pf_response
  echo "Keeping container alive without port forwarding..."
  tail -f /dev/null
  exit 0
fi

echo "Port forwarding initialized:"
echo "  Forwarded Port: $PORT"
echo "  Expires at: $(date -d @$EXPIRES_AT 2>/dev/null || date -r $EXPIRES_AT 2>/dev/null || echo $EXPIRES_AT)"

# Bind the port
bind_port "$PAYLOAD" "$SIGNATURE" || echo "Warning: Initial bind failed, will retry in refresh loop"

# Save port to file
echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"
echo "Port saved to ${PORT_FILE:-/etc/wireguard/port}"
echo ""

# Refresh loop (every 15 minutes)
echo "Starting refresh loop..."
while true; do
  sleep 900  # 15 minutes
  
  echo "Refreshing port signature at $(date)..."
  
  if ! get_signature; then
    echo "Warning: Failed to refresh. Will retry next cycle."
    continue
  fi
  
  PAYLOAD=$(jq -r '.payload' /tmp/pf_response)
  SIGNATURE=$(jq -r '.signature' /tmp/pf_response)
  NEW_PORT=$(echo "$PAYLOAD" | base64 -d | jq -r '.port')
  
  if [ "$NEW_PORT" != "$PORT" ]; then
    echo "Port changed from $PORT to $NEW_PORT"
    PORT=$NEW_PORT
    echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"
  fi
  
  if bind_port "$PAYLOAD" "$SIGNATURE"; then
    echo "Port $PORT refreshed successfully"
  else
    echo "Warning: Bind failed during refresh"
  fi
done

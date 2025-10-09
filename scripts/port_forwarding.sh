#!/bin/bash

# Define colors
red=$'\033[0;31m'
grn=$'\033[0;32m'
blu=$'\033[0;34m'
cyn=$'\033[0;36m'
ylw=$'\033[0;33m'
nc=$'\033[0m'
bold=$'\033[1m'

# Load required data
TOKEN=$(cat /tmp/pia_token)
PEER_IP=$(cat /tmp/client_ip)
META_CN=$(cat /tmp/meta_cn)
PF_GATEWAY=$(cat /tmp/pf_gateway)

if [ -z "$PF_GATEWAY" ] || [ "$PF_GATEWAY" = "null" ]; then
  echo "  ${red}✗${nc} No PF gateway available"
  exit 1
fi

# Function to get port signature
get_signature() {
  sleep 2
  
  curl -s -m 10 -k \
    --interface pia \
    -G \
    --data-urlencode "token=$TOKEN" \
    "https://$PF_GATEWAY:19999/getSignature" \
    -o /tmp/pf_response 2>&1
  
  local curl_exit=$?
  
  if [ $curl_exit -ne 0 ] || [ ! -s /tmp/pf_response ]; then
    return 1
  fi
  
  if ! jq -e '.status' /tmp/pf_response >/dev/null 2>&1; then
    return 1
  fi
  
  local status=$(jq -r '.status' /tmp/pf_response)
  if [ "$status" != "OK" ]; then
    return 1
  fi
  
  return 0
}

# Function to bind port
bind_port() {
  local payload="$1"
  local signature="$2"
  
  curl -s -m 10 -k \
    --interface pia \
    -G \
    --data-urlencode "payload=$payload" \
    --data-urlencode "signature=$signature" \
    "https://$PF_GATEWAY:19999/bindPort" \
    -o /tmp/pf_bind_response 2>&1
  
  local curl_exit=$?
  
  if [ $curl_exit -ne 0 ]; then
    return 1
  fi
  
  local status=$(jq -r '.status // empty' /tmp/pf_bind_response 2>/dev/null)
  if [ "$status" != "OK" ]; then
    return 1
  fi
  
  return 0
}

# Retry logic for initial signature
MAX_RETRIES=5
retry=0

while [ $retry -lt $MAX_RETRIES ]; do
  if get_signature 2>/dev/null; then
    break
  fi
  
  retry=$((retry + 1))
  if [ $retry -lt $MAX_RETRIES ]; then
    sleep $((5 * retry))
  fi
done

if [ $retry -ge $MAX_RETRIES ]; then
  echo "  ${red}✗${nc} Port forwarding failed after $MAX_RETRIES attempts"
  echo ""
  echo "${cyn}╔════════════════════════════════════════════════╗${nc}"
  echo "${cyn}║${nc}  ${ylw}⚠${nc} ${bold}VPN Active (No Port Forwarding)${nc}          ${cyn}║${nc}"
  echo "${cyn}╚════════════════════════════════════════════════╝${nc}"
  echo ""
  tail -f /dev/null
  exit 0
fi

# Parse response
PORT=$(jq -r '.payload' /tmp/pf_response | base64 -d | jq -r '.port')
PAYLOAD=$(jq -r '.payload' /tmp/pf_response)
SIGNATURE=$(jq -r '.signature' /tmp/pf_response)
EXPIRES_AT=$(jq -r '.payload' /tmp/pf_response | base64 -d | jq -r '.expires_at')

if [ -z "$PORT" ] || [ "$PORT" = "null" ]; then
  echo "  ${red}✗${nc} Failed to extract port from response"
  echo ""
  echo "${cyn}╔════════════════════════════════════════════════╗${nc}"
  echo "${cyn}║${nc}  ${ylw}⚠${nc} ${bold}VPN Connected${nc}          ${cyn}║${nc}"
  echo "${cyn}╚════════════════════════════════════════════════╝${nc}"
  echo ""
  tail -f /dev/null
  exit 0
fi

# Bind the port
bind_port "$PAYLOAD" "$SIGNATURE" >/dev/null 2>&1

# Save port to file
echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"

# Success message
echo "  ${grn}✓${nc} Port: ${grn}${bold}${PORT}${nc}"
echo ""

echo "${grn}╔════════════════════════════════════════════════╗${nc}"
echo "${grn}║${nc}  ${grn}✓${nc} ${bold}VPN Connected${nc}            ${grn}║${nc}"
echo "${grn}╚════════════════════════════════════════════════╝${nc}"
echo ""

# Refresh loop (every 24 hours to keep binding alive)
echo "${blu}▶${nc} Refreshing port in 24 hours..."
echo ""

REFRESH_COUNT=0
while true; do
  sleep 86400  # 24 hours
  
  REFRESH_COUNT=$((REFRESH_COUNT + 1))
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] ${blu}↻${nc} Refreshing port signature (day #${REFRESH_COUNT})..."
  
  if ! get_signature 2>/dev/null; then
    echo "  ${ylw}⚠${nc} Refresh failed, will retry tomorrow"
    continue
  fi
  
  PAYLOAD=$(jq -r '.payload' /tmp/pf_response)
  SIGNATURE=$(jq -r '.signature' /tmp/pf_response)
  NEW_PORT=$(echo "$PAYLOAD" | base64 -d | jq -r '.port')
  
  if [ "$NEW_PORT" != "$PORT" ]; then
    echo "  ${ylw}ℹ${nc} Port changed: $PORT → $NEW_PORT"
    PORT=$NEW_PORT
    echo "$PORT" > "${PORT_FILE:-/etc/wireguard/port}"
  fi
  
  if bind_port "$PAYLOAD" "$SIGNATURE" >/dev/null 2>&1; then
    echo "  ${grn}✓${nc} Port ${grn}${PORT}${nc} refreshed successfully"
  else
    echo "  ${ylw}⚠${nc} Bind failed, will retry tomorrow"
  fi
  echo ""
done
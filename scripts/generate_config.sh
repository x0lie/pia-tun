#!/bin/bash

set -e

# Load server details (fail if missing)
for file in server_endpoint meta_cn client_ip pia_token; do
  if [ ! -f "/tmp/${file}" ]; then
    echo "Error: Missing /tmp/${file}. Run get_token.sh and get_server_info.sh first."
    exit 1
  fi
done

ENDPOINT_IP=$(cat /tmp/server_endpoint)
META_CN=$(cat /tmp/meta_cn)  # WG CN for addKey/PF
CLIENT_IP=$(cat /tmp/client_ip)  # Fallback; override with peer_ip
TOKEN=$(cat /tmp/pia_token)

# Generate client keys
echo "Generating WireGuard keys..."
wg genkey | tee /tmp/private.key | wg pubkey > /tmp/public.key
PRIVATE_KEY=$(cat /tmp/private.key)
CLIENT_PUBKEY=$(cat /tmp/public.key)

# Fetch WG details via addKey API
echo "Fetching server details via /addKey..."
ADDKEY_RESPONSE=$(curl -s -k -G \
  --connect-to "$META_CN::$ENDPOINT_IP:" \
  --data-urlencode "pt=$TOKEN" \
  --data-urlencode "pubkey=$CLIENT_PUBKEY" \
  "https://$META_CN:1337/addKey")

if [ "$(echo "$ADDKEY_RESPONSE" | jq -r '.status')" != "OK" ]; then
  echo "Error: addKey failed. Response: $(echo "$ADDKEY_RESPONSE" | jq .)"
  exit 1
fi

SERVER_PUBKEY=$(echo "$ADDKEY_RESPONSE" | jq -r '.server_key')
PEER_IP=$(echo "$ADDKEY_RESPONSE" | jq -r '.peer_ip')
SERVER_PORT=$(echo "$ADDKEY_RESPONSE" | jq -r '.server_port // 1337')
PF_GATEWAY=$(echo "$ADDKEY_RESPONSE" | jq -r '.server_vip // empty')
DNS_SERVERS=$(echo "$ADDKEY_RESPONSE" | jq -r '.dns_servers | join(",") // empty')

# Override and save for config/PF
echo "$PEER_IP" > /tmp/client_ip
echo "$SERVER_PUBKEY" > /tmp/server_pubkey
echo "$PF_GATEWAY" > /tmp/pf_gateway

echo "addKey success: Peer IP $PEER_IP, Port $SERVER_PORT, PF Gateway $PF_GATEWAY"

# Apply killswitch based on PORT_FORWARDING setting
if [ "${PORT_FORWARDING}" = "true" ]; then
  # For port forwarding: use minimal killswitch that allows incoming connections
  FINAL_KILLSWITCH_UP="iptables -I OUTPUT -d 10.0.0.0/8 -j ACCEPT; iptables -I OUTPUT -d 172.16.0.0/12 -j ACCEPT; iptables -I OUTPUT -d 192.168.0.0/16 -j ACCEPT; iptables -I OUTPUT -d 127.0.0.0/8 -j ACCEPT; iptables -A OUTPUT ! -o %i ! -o lo -j REJECT"
  
  FINAL_KILLSWITCH_DOWN="iptables -D OUTPUT -d 10.0.0.0/8 -j ACCEPT 2>/dev/null || true; iptables -D OUTPUT -d 172.16.0.0/12 -j ACCEPT 2>/dev/null || true; iptables -D OUTPUT -d 192.168.0.0/16 -j ACCEPT 2>/dev/null || true; iptables -D OUTPUT -d 127.0.0.0/8 -j ACCEPT 2>/dev/null || true; iptables -D OUTPUT ! -o %i ! -o lo -j REJECT 2>/dev/null || true"
else
  # Full killswitch with routing table isolation
  FINAL_KILLSWITCH_UP="iptables -I OUTPUT -d 10.0.0.0/8 -j ACCEPT; iptables -I OUTPUT -d 172.16.0.0/12 -j ACCEPT; iptables -I OUTPUT -d 192.168.0.0/16 -j ACCEPT; iptables -I OUTPUT -d 127.0.0.0/8 -j ACCEPT; iptables -I OUTPUT -o %i -p udp --dport 53 -j ACCEPT; iptables -I OUTPUT -o %i -p tcp --dport 53 -j ACCEPT; iptables -I OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT; iptables -A OUTPUT ! -o %i ! -o lo -m mark ! --mark 51820 -j REJECT"
  
  FINAL_KILLSWITCH_DOWN="iptables -D OUTPUT -d 10.0.0.0/8 -j ACCEPT 2>/dev/null || true; iptables -D OUTPUT -d 172.16.0.0/12 -j ACCEPT 2>/dev/null || true; iptables -D OUTPUT -d 192.168.0.0/16 -j ACCEPT 2>/dev/null || true; iptables -D OUTPUT -d 127.0.0.0/8 -j ACCEPT 2>/dev/null || true; iptables -D OUTPUT -o %i -p udp --dport 53 -j ACCEPT 2>/dev/null || true; iptables -D OUTPUT -o %i -p tcp --dport 53 -j ACCEPT 2>/dev/null || true; iptables -D OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || true; iptables -D OUTPUT ! -o %i ! -o lo -m mark ! --mark 51820 -j REJECT 2>/dev/null || true"
fi

# Build config with improved routing and killswitch
cat > /etc/wireguard/pia.conf << EOF
[Interface]
PrivateKey = $PRIVATE_KEY
Address = $PEER_IP/32
${DNS_SERVERS:+DNS = $DNS_SERVERS}
${PIA_DNS:+DNS = 209.222.18.222, 209.222.18.218}
MTU = ${MTU:-1420}
Table = auto

# Killswitch: Prevent leaks outside tunnel
PostUp = $FINAL_KILLSWITCH_UP
PostDown = $FINAL_KILLSWITCH_DOWN

[Peer]
PublicKey = $SERVER_PUBKEY
Endpoint = $ENDPOINT_IP:$SERVER_PORT
AllowedIPs = $(if [ "${DISABLE_IPV6}" = "true" ]; then echo "0.0.0.0/0"; else echo "0.0.0.0/0, ::/0"; fi)
PersistentKeepalive = 25
EOF

# Echo Results
echo "Config generated at /etc/wireguard/pia.conf"
echo ""
cat /etc/wireguard/pia.conf
echo ""
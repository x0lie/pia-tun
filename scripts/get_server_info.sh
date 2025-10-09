#!/bin/bash

set -e

# This function allows you to check if the required tools have been installed.
check_tool() {
  cmd=$1
  if ! command -v "$cmd" >/dev/null; then
    echo "$cmd could not be found"
    echo "Please install $cmd"
    exit 1
  fi
}

# Now we call the function to make sure we can use curl, jq, and bc.
check_tool curl
check_tool jq
check_tool bc

# This allows you to set the maximum allowed latency in seconds.
# All servers that respond slower than this will be ignored.
# You can inject this with the environment variable MAX_LATENCY.
# The default value is 1 second.
MAX_LATENCY=${MAX_LATENCY:-1}
export MAX_LATENCY

serverlist_url='https://serverlist.piaservers.net/vpninfo/servers/v6'

# Get all region data
all_region_data=$(curl -s "$serverlist_url" | head -1)

# If the server list has less than 1000 characters, it means curl failed.
if [[ ${#all_region_data} -lt 1000 ]]; then
  echo "Could not get correct region data. To debug this, run:"
  echo "$ curl -v $serverlist_url"
  echo "If it works, you will get a huge JSON as a response."
  exit 1
fi

# Set the region from environment sybnau
selectedRegion="$PIA_LOCATION"

# Get data for the selected region
# Exit with code 1 if the REGION_ID provided is invalid
regionData="$( echo "$all_region_data" |
  jq --arg REGION_ID "$selectedRegion" -r \
  '.regions[] | select(.id==$REGION_ID)')"
if [[ -z $regionData ]]; then
  echo "The REGION_ID $selectedRegion is not valid."
  echo "To get a list of regions, you can run:"
  echo "curl \"$serverlist_url\" | head -1 | jq -r '.regions[].id'"
  exit 1
fi

# Check if location supports port forwarding if required
SUPPORTS_PF=$(echo "$regionData" | jq -r '.port_forward')
if [ "$PORT_FORWARDING" = "true" ] && [ "$SUPPORTS_PF" != "true" ]; then
  echo "Error: PORT_FORWARDING=true but location '$selectedRegion' has port_forward=false. Use a supported location (e.g., non-US) or set PORT_FORWARDING=false."
  exit 1
fi
if [ "$SUPPORTS_PF" != "true" ]; then
  echo "Warning: PF disabled on this server (port_forward=$SUPPORTS_PF)."
fi

# This function checks the latency you have to a specific server.
# It will print the variables to stdout
printServerLatency() {
  serverIP=$1
  serverCN=$2
  time=$(LC_NUMERIC=en_US.utf8 curl -k -o /dev/null -s \
    --connect-timeout "$MAX_LATENCY" \
    --write-out "%{time_connect}" \
    "https://$serverIP:443")
  if [[ $? -eq 0 && "$time" != "0.000"* ]]; then
    echo "$time $serverIP $serverCN"
  fi
}
export -f printServerLatency

# Get all valid WG servers for the location
ALL_WG_SERVERS="$( echo "$regionData" |
  jq -r '.servers.wg[] | .ip + " " + .cn' )"

if [[ -z $ALL_WG_SERVERS ]]; then
  echo "Error: No valid WireGuard servers found for '$selectedRegion'. Try a different PIA_LOCATION like 'ca_vancouver'."
  exit 1
fi

# Collect latencies
LATENCIES=""
echo "Pinging WG servers in $selectedRegion via TCP connect time to port 443 (<${MAX_LATENCY}s timeout)..."

echo "$ALL_WG_SERVERS" | while read -r wg_line; do
  serverIP=$(echo "$wg_line" | awk '{print $1}')
  serverCN=$(echo "$wg_line" | awk '{print $2}')
  time_line=$(bash -c "printServerLatency $serverIP $serverCN")
  if [[ -n $time_line ]]; then
    echo "  Connect to $serverIP ($serverCN): $(echo "$time_line" | awk '{print $1}')s"
    LATENCIES="${LATENCIES}${time_line}\n"
  fi
done

# Select the best
if [[ -z $LATENCIES ]]; then
  echo "Warning: No WG servers responded within ${MAX_LATENCY}s in '$selectedRegion'. Picking the first one anyway."
  BEST_WG_IP=$(echo "$regionData" | jq -r '.servers.wg[0].ip')
  BEST_WG_CN=$(echo "$regionData" | jq -r '.servers.wg[0].cn')
else
  BEST_LINE=$(echo -e "$LATENCIES" | sort -n | head -1)
  BEST_WG_IP=$(echo "$BEST_LINE" | awk '{print $2}')
  BEST_WG_CN=$(echo "$BEST_LINE" | awk '{print $3}')
  LOWEST_TIME=$(echo "$BEST_LINE" | awk '{print $1}')
  echo "Best server selected with connect time: ${LOWEST_TIME}s"
fi

# Save for config gen
echo "$BEST_WG_IP" > /tmp/server_endpoint
echo "$BEST_WG_CN" > /tmp/meta_cn
echo "${CLIENT_IP_RANGE:-10.0.0.2}" > /tmp/client_ip

echo "Server info grabbed for $selectedRegion:"
echo "  Endpoint IP: $BEST_WG_IP:1337"
echo "  WG CN (for addKey/PF): $BEST_WG_CN"

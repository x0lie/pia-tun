#!/bin/bash

# Exit on error
set -e

# Helper to export vars to child scripts
export_vars() {
    # Default DISABLE_IPV6 to true if not set (avoid kernel module issues)
    export DISABLE_IPV6=${DISABLE_IPV6:-true}
    export PIA_USER PIA_PASS PIA_LOCATION PORT_FORWARDING LOCAL_NETWORK PIA_DNS MTU
}

# Main flow
main() {
    export_vars

    # Authenticate and get token
    echo "Fetching PIA token..."
    /app/scripts/get_token.sh

    # Get Server info
    echo "Gathering Server Information"
    /app/scripts/get_server_info.sh

    # Generate WireGuard config
    echo "Generating WireGuard config for $PIA_LOCATION..."
    /app/scripts/generate_config.sh

    # DNS fallback: Set DNS before bringing up tunnel
    DNS_LINE=$(grep '^DNS =' /etc/wireguard/pia.conf | cut -d= -f2- | tr -d ' ')
    if [ -n "$DNS_LINE" ]; then
        cp /etc/resolv.conf /etc/resolv.conf.bak 2>/dev/null || true
        echo "# Set by PIA WireGuard" > /etc/resolv.conf
        IFS=',' read -ra DNS_SERVERS <<< "$DNS_LINE"
        for server in "${DNS_SERVERS[@]}"; do
            server=$(echo "$server" | xargs)  # Trim whitespace
            echo "nameserver $server" >> /etc/resolv.conf
        done
        echo "DNS configured: $DNS_LINE"
    fi

    # Bring up the tunnel
    echo "Starting WireGuard tunnel..."
    wg-quick up /etc/wireguard/pia.conf

    # Wait a moment for tunnel to stabilize
    sleep 2

    # Verify connectivity
    echo "Testing connectivity..."
    if timeout 10 curl -s ifconfig.me; then
        echo " - Connection verified!"
    else
        echo "Warning: Could not verify external IP"
    fi

    # Port Forward if =true
    if [ "${PORT_FORWARDING}" = "true" ]; then
        echo "Asking for port..."
        /app/scripts/port_forwarding.sh
    else
        # Keep container alive if no PF loop
        echo "Tunnel up; keeping container alive..."
        tail -f /dev/null
    fi
}

# Run main
main "$@"

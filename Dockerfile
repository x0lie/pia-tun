FROM alpine:latest

# Install dependencies
RUN apk update && \
    apk add --no-cache \
        jq \
        bc \
        curl \
        bash \
        iptables \
        ip6tables \
        iputils \
        bind-tools \
        wireguard-tools \
        ca-certificates \
        iproute2 \
        speedtest-cli \
        procps && \
    rm -rf /var/cache/apk/*

# Add PIA certificate
ADD https://raw.githubusercontent.com/pia-foss/desktop/master/daemon/res/ca/rsa_4096.crt /app/ca.rsa.4096.crt

# Create working directory
WORKDIR /app

# Copy scripts
COPY run.sh /app/run.sh
COPY scripts/ /app/scripts/
RUN chmod +x /app/run.sh /app/scripts/*.sh

# Create config directory
RUN mkdir -p /etc/wireguard

# Volume for configs and port file
VOLUME ["/etc/wireguard"]
ENV PORT_FILE=/etc/wireguard/port

# Health check - enhanced to verify no leaks
HEALTHCHECK --interval=60s --timeout=10s --start-period=30s --retries=3 \
    CMD wg show pia 2>/dev/null | grep -q "latest handshake" && \
        [ -z "$(timeout 3 curl -6 -s --interface pia https://api6.ipify.org 2>/dev/null || echo '')" ] || exit 1

# Environment variables
ENV KILLSWITCH_EXEMPT_PORTS=""
ENV DISABLE_IPV6=true
ENV LOCAL_NETWORK=""

# LOCAL_NETWORK usage (secure by default):
# Default (empty): All traffic through VPN, no local network access
# Allow all RFC1918: LOCAL_NETWORK="all"
# Single network: LOCAL_NETWORK="192.168.1.0/24"
# Multiple networks: LOCAL_NETWORK="192.168.1.0/24,10.0.0.0/8"
# 
# IMPORTANT: Enabling local network access means traffic to those networks
# will NOT go through the VPN. Only enable if you trust your local network.

# Performance-related sysctls should be set via docker run --sysctl
# Example: --sysctl net.core.rmem_max=26214400 --sysctl net.core.wmem_max=26214400

ENTRYPOINT ["/app/run.sh"]

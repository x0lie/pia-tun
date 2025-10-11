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
        procps \
        docker-cli && \
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

# Enhanced health check that validates VPN is working
HEALTHCHECK --interval=60s --timeout=10s --start-period=30s --retries=3 \
    CMD /app/scripts/healthcheck.sh

# Environment variables - VPN Configuration
ENV KILLSWITCH_EXEMPT_PORTS=""
ENV DISABLE_IPV6=true
ENV LOCAL_NETWORK=""
ENV DNS="pia"

# Environment variables - Auto-Reconnect Configuration
ENV HANDSHAKE_TIMEOUT=180
ENV CHECK_INTERVAL=30
ENV MAX_FAILURES=2
ENV RECONNECT_DELAY=5
ENV MAX_RECONNECT_DELAY=300
ENV RESTART_SERVICES=""
ENV MONITOR_DEBUG=false

# LOCAL_NETWORK usage (secure by default):
# Default (empty): All traffic through VPN, no local network access
# Allow all RFC1918: LOCAL_NETWORK="all"
# Single network: LOCAL_NETWORK="192.168.1.0/24"
# Multiple networks: LOCAL_NETWORK="192.168.1.0/24,10.0.0.0/8"
# 
# IMPORTANT: Enabling local network access means traffic to those networks
# will NOT go through the VPN. Only enable if you trust your local network.

# RESTART_SERVICES usage (for auto-reconnect):
# Comma-separated list of Docker container names to restart after reconnection
# Example: RESTART_SERVICES="qbittorrent,transmission"
# Note: Requires Docker socket mount (-v /var/run/docker.sock:/var/run/docker.sock)

# Performance-related sysctls should be set via docker run --sysctl
# Example: --sysctl net.core.rmem_max=26214400 --sysctl net.core.wmem_max=26214400

ENTRYPOINT ["/app/run.sh"]

# Build stage for Go proxy
FROM golang:1.21-alpine AS proxy-builder

WORKDIR /build
COPY scripts/proxy.go .

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o proxy proxy.go

# Final stage
FROM alpine:latest

# Install dependencies (no dante-server or tinyproxy needed!)
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
        docker-cli \
        net-tools && \
    rm -rf /var/cache/apk/*

# Add PIA certificate
ADD https://raw.githubusercontent.com/pia-foss/desktop/master/daemon/res/ca/rsa_4096.crt /app/ca.rsa.4096.crt

# Copy Go proxy binary from builder
COPY --from=proxy-builder /build/proxy /usr/local/bin/proxy

# Create working directory
WORKDIR /app

# Copy scripts
COPY run.sh /app/run.sh
COPY scripts/ /app/scripts/
RUN chmod +x /app/run.sh /app/scripts/*.sh /usr/local/bin/proxy

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

# Environment variables - Proxy Configuration
ENV PROXY_ENABLED=false
ENV SOCKS5_PORT=1080
ENV HTTP_PROXY_PORT=8888
ENV PROXY_USER=""
ENV PROXY_PASS=""

# Environment variables - Auto-Reconnect Configuration
ENV HANDSHAKE_TIMEOUT=180
ENV CHECK_INTERVAL=30
ENV MAX_FAILURES=2
ENV RECONNECT_DELAY=5
ENV MAX_RECONNECT_DELAY=300
ENV RESTART_SERVICES=""
ENV MONITOR_DEBUG=false

# Expose proxy ports
EXPOSE 1080 8888

# LOCAL_NETWORK usage (secure by default):
# Default (empty): All traffic through VPN, no local network access
# Allow all RFC1918: LOCAL_NETWORK="all"
# Single network: LOCAL_NETWORK="192.168.1.0/24"
# Multiple networks: LOCAL_NETWORK="192.168.1.0/24,10.0.0.0/8"
# 
# IMPORTANT: Enabling local network access means traffic to those networks
# will NOT go through the VPN. Only enable if you trust your local network.

# PROXY USAGE (NO SETUID/SETGID REQUIRED):
# Enable proxies: PROXY_ENABLED=true
# SOCKS5 proxy will be available on port 1080 (configurable via SOCKS5_PORT)
# HTTP proxy will be available on port 8888 (configurable via HTTP_PROXY_PORT)
#
# Built-in Go proxy supports authentication WITHOUT requiring SETUID/SETGID:
#   PROXY_USER=username PROXY_PASS=password
#
# Works perfectly with --cap-drop=ALL --cap-add=NET_ADMIN --cap-add=NET_RAW
# No additional capabilities required!

# RESTART_SERVICES usage (for auto-reconnect):
# Comma-separated list of Docker container names to restart after reconnection
# Example: RESTART_SERVICES="qbittorrent,transmission"
# Note: Requires Docker socket mount (-v /var/run/docker.sock:/var/run/docker.sock)

# Performance-related sysctls should be set via docker run --sysctl
# Example: --sysctl net.core.rmem_max=26214400 --sysctl net.core.wmem_max=26214400

ENTRYPOINT ["/app/run.sh"]

# Build stage for Go binaries
FROM golang:1.21-alpine AS go-builder

WORKDIR /build

# Copy go.mod first (for better caching)
COPY go.mod ./

# Copy all Go source files
COPY cmd/ ./cmd/

# Build proxy binary
RUN cd cmd/proxy && \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o /build/proxy .

# Build monitor binary
RUN cd cmd/monitor && \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o /build/monitor .

# Final stage
FROM alpine:latest

# Install dependencies
RUN apk update && \
    apk add --no-cache \
        jq \
        bc \
        curl \
        bash \
        nftables \
        iptables \
        ip6tables \
        iputils \
        bind-tools \
        wireguard-tools \
        ca-certificates \
        iproute2 \
        procps \
        docker-cli && \
    rm -rf /var/cache/apk/*

# Add PIA certificate
ADD https://raw.githubusercontent.com/pia-foss/desktop/master/daemon/res/ca/rsa_4096.crt /app/ca.rsa.4096.crt

# Copy Go binaries from builder
COPY --from=go-builder /build/proxy /usr/local/bin/proxy
COPY --from=go-builder /build/monitor /usr/local/bin/monitor

# Create working directory
WORKDIR /app

# Copy scripts
COPY run.sh /app/run.sh
COPY scripts/ /app/scripts/
RUN chmod +x /app/run.sh /app/scripts/*.sh /usr/local/bin/proxy /usr/local/bin/monitor

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

# Environment variables - Port Forwarding API
ENV PORT_API_ENABLED=false
ENV PORT_API_TYPE=""
ENV PORT_API_URL=""
ENV PORT_API_USER=""
ENV PORT_API_PASS=""

# Environment variables - Auto-Reconnect Configuration (IMPROVED DEFAULTS)
ENV HANDSHAKE_TIMEOUT=360
ENV CHECK_INTERVAL=15
ENV MAX_FAILURES=3
ENV RECONNECT_DELAY=5
ENV MAX_RECONNECT_DELAY=300
ENV RESTART_SERVICES=""
ENV MONITOR_DEBUG=false
ENV MONITOR_PARALLEL_CHECKS=true
ENV MONITOR_FAST_FAIL=false
ENV MONITOR_WATCH_HANDSHAKE=true
ENV MONITOR_STARTUP_GRACE=30
ENV METRICS=false
ENV METRICS_PORT=9090

# Expose proxy ports and metrics port
EXPOSE 1080 8888 9090

ENTRYPOINT ["/app/run.sh"]

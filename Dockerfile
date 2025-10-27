FROM golang:1.21-alpine AS go-builder

WORKDIR /build

COPY go.mod ./
COPY cmd/ ./cmd/

# Build with maximum optimization
RUN cd cmd/proxy && \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags="-w -s" \
    -trimpath \
    -o /build/proxy . && \
    cd ../monitor && \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags="-w -s" \
    -trimpath \
    -o /build/monitor . && \
    # Make them executable HERE (not after COPY)
    chmod +x /build/proxy /build/monitor

FROM alpine:3.19

# Install MINIMAL runtime dependencies
# Removed: bind-tools (1.8MB), nftables (722KB)
RUN apk update && \
    apk add --no-cache \
        bash \
        curl \
        ca-certificates \
        wireguard-tools-wg \
        iproute2 \
        iptables \
        jq \
        iputils \
        nftables \
        bind-tools \
    && \
    bash --version && \
    wg --version && \
    rm -rf \
        /var/cache/apk/* \
        /tmp/* \
        /var/tmp/* \
        /usr/share/man \
        /usr/share/doc \
        /usr/share/info \
        /usr/share/licenses \
        /usr/lib/python* \
        /root/.cache \
    && \
    find /usr/bin /usr/sbin /bin /sbin -type f -executable \
        -exec strip --strip-all {} \; 2>/dev/null || true

# Copy Go binaries (already executable from builder)
COPY --from=go-builder /build/proxy /usr/local/bin/proxy
COPY --from=go-builder /build/monitor /usr/local/bin/monitor

# Copy certificate
COPY ca/rsa_4096.crt /app/ca.rsa.4096.crt

WORKDIR /app

# Copy scripts
COPY run.sh /app/run.sh
COPY scripts/ /app/scripts/

# CRITICAL FIX: Only chmod scripts, NOT the Go binaries (prevents 10MB duplication)
RUN chmod +x /app/run.sh /app/scripts/*.sh && \
    mkdir -p /etc/wireguard && \
    ls -la /usr/local/bin/proxy /usr/local/bin/monitor

VOLUME ["/etc/wireguard"]

HEALTHCHECK --interval=60s --timeout=10s --start-period=30s --retries=3 \
    CMD /app/scripts/healthcheck.sh

ENV KILLSWITCH_EXEMPT_PORTS="" \
    DISABLE_IPV6=true \
    LOCAL_NETWORK="" \
    DNS="pia" \
    PORT_FILE=/etc/wireguard/port \
    PROXY_ENABLED=false \
    SOCKS5_PORT=1080 \
    HTTP_PROXY_PORT=8888 \
    PROXY_USER="" \
    PROXY_PASS="" \
    PORT_API_ENABLED=false \
    PORT_API_TYPE="" \
    PORT_API_URL="" \
    PORT_API_USER="" \
    PORT_API_PASS="" \
    HANDSHAKE_TIMEOUT=360 \
    CHECK_INTERVAL=15 \
    MAX_FAILURES=3 \
    RECONNECT_DELAY=5 \
    MAX_RECONNECT_DELAY=300 \
    RESTART_SERVICES="" \
    MONITOR_DEBUG=false \
    MONITOR_PARALLEL_CHECKS=true \
    MONITOR_FAST_FAIL=false \
    MONITOR_WATCH_HANDSHAKE=true \
    MONITOR_STARTUP_GRACE=30 \
    METRICS=false \
    METRICS_PORT=9090

EXPOSE 1080 8888 9090

# Use bash
ENTRYPOINT ["/app/run.sh"]

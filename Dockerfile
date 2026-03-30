FROM golang:1.26-alpine AS go-builder

WORKDIR /build

COPY go.mod go.sum ./
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
    cd ../portforward && \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags="-w -s" \
    -trimpath \
    -o /build/portforward . && \
    # Make them executable HERE (not after COPY)
    chmod +x /build/proxy /build/monitor /build/portforward

FROM alpine:3.19

# Install MINIMAL runtime dependencies
RUN apk update && \
    apk add --no-cache \
        bash \
        curl \
        jq \
        ca-certificates \
        wireguard-tools-wg \
        nftables \
        iptables \
        iproute2-minimal \
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
COPY --from=go-builder /build/portforward /usr/local/bin/portforward

# Copy certificate
COPY ca/rsa_4096.crt /app/ca.rsa.4096.crt

WORKDIR /app

# Copy scripts
COPY run.sh /app/run.sh
COPY scripts/ /app/scripts/

# CRITICAL FIX: Only chmod scripts, NOT the Go binaries (prevents 10MB duplication)
RUN chmod +x /app/run.sh /app/scripts/*.sh && \
    mkdir -p /etc/wireguard && \
    ls -la /usr/local/bin/proxy /usr/local/bin/monitor /usr/local/bin/portforward

VOLUME ["/etc/wireguard"]

ENV TZ=UTC \
    LOG_LEVEL=info \
    DISABLE_IPV6=true \
    LOCAL_NETWORK="" \
    LOCAL_PORTS="" \
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
    CHECK_INTERVAL=15 \
    MAX_FAILURES=3 \
    RESTART_SERVICES="" \
    MONITOR_PARALLEL_CHECKS=true \
    METRICS=false \
    METRICS_PORT=9090

EXPOSE 1080 8888 9090

# Use bash
ENTRYPOINT ["/app/run.sh"]

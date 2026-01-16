FROM golang:1.23-alpine AS go-builder

# Accept version as build argument (passed by GitHub Actions)
ARG VERSION=local

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

# Accept version from build stage
ARG VERSION=local

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
    mkdir -p /run/pia-tun && \
    ls -la /usr/local/bin/proxy /usr/local/bin/monitor /usr/local/bin/portforward

# Set VERSION as environment variable for runtime
ENV VERSION=${VERSION}

# OCI labels for container metadata (connects GHCR package to GitHub repo)
LABEL org.opencontainers.image.title="pia-tun" \
      org.opencontainers.image.description="Lightweight WireGuard VPN client for Private Internet Access with advanced killswitch, port forwarding, and SOCKS5/HTTP proxy support" \
      org.opencontainers.image.url="https://github.com/x0lie/pia-tun" \
      org.opencontainers.image.source="https://github.com/x0lie/pia-tun" \
      org.opencontainers.image.documentation="https://github.com/x0lie/pia-tun/blob/main/README.md" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.vendor="x0lie"

ENV TZ=UTC \
    LOG_LEVEL=info \
    IPV6_ENABLED=false \
    LOCAL_NETWORKS="" \
    LOCAL_PORTS="" \
    DNS="pia" \
    PORT_FILE=/run/pia-tun/port \
    PORT_SYNC_TYPE="" \
    PORT_SYNC_URL="" \
    PORT_SYNC_USER="" \
    PORT_SYNC_PASS="" \
    PORT_SYNC_CMD="" \
    PROXY_ENABLED=false \
    PROXY_USER="" \
    PROXY_PASS="" \
    SOCKS5_PORT=1080 \
    HTTP_PROXY_PORT=8888 \
    METRICS=true \
    METRICS_PORT=9090 \
    HC_INTERVAL=15 \
    HC_MAX_FAILURES=3

EXPOSE 1080 8888 9090

# Use bash
ENTRYPOINT ["/app/run.sh"]

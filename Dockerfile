FROM golang:1.23-alpine AS go-builder

RUN apk add --no-cache git

WORKDIR /build

COPY go.mod go.sum ./
COPY cmd/pia-tun/ ./cmd/pia-tun/
COPY internal/ ./internal/

# Build single binary with maximum optimization
RUN cd cmd/pia-tun && \
    CGO_ENABLED=0 GOOS=linux go build \
    -a -installsuffix cgo \
    -ldflags="-w -s" \
    -trimpath \
    -o /build/pia-tun . && \
    chmod +x /build/pia-tun

# Build wireguard-go for userspace fallback (pre-5.6 kernels without WireGuard module)
RUN git clone --depth 1 https://git.zx2c4.com/wireguard-go /build/wireguard-go-src && \
    cd /build/wireguard-go-src && \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags="-w -s" \
    -trimpath \
    -o /build/wireguard-go . && \
    chmod +x /build/wireguard-go

FROM alpine:3.19

# Accept version from build stage
ARG VERSION=local
ARG SHA=local

# Install MINIMAL runtime dependencies
RUN apk update && \
    apk add --no-cache \
        bash \
        curl \
        knot-utils \
        jq \
        ca-certificates \
        wireguard-tools-wg \
        iptables \
        iptables-legacy \
        iproute2-minimal \
    && \
    bash --version && \
    wg --version && \
    rm -rf \
        /var/cache/apk/* \
        /tmp/* \
        /var/tmp/* \
        /usr/share/man/* \
        /usr/share/doc/* \
        /usr/share/info/* \
        /usr/share/licenses/* \
        /usr/lib/python* \
        /root/.cache \
        /usr/lib*.a /usr/lib/*.la \
    && \
    find /usr/bin /usr/sbin /bin /sbin -type f -executable \
        -exec strip --strip-all {} \; 2>/dev/null || true

# Copy single Go binary and create symlinks (busybox-style dispatch)
COPY --from=go-builder /build/pia-tun /usr/local/bin/pia-tun
RUN ln -s pia-tun /usr/local/bin/monitor && \
    ln -s pia-tun /usr/local/bin/cacher && \
    ln -s pia-tun /usr/local/bin/portforward && \
    ln -s pia-tun /usr/local/bin/proxy
COPY --from=go-builder /build/wireguard-go /usr/local/bin/wireguard-go

# Copy certificate
COPY ca/rsa_4096.crt /app/ca.rsa.4096.crt

WORKDIR /app

# Copy scripts
COPY run.sh /app/run.sh
COPY scripts/ /app/scripts/

# CRITICAL FIX: Only chmod scripts, NOT the Go binaries (prevents 10MB duplication)
RUN chmod +x /app/run.sh /app/scripts/*.sh && \
    mkdir -p /etc/wireguard && \
    mkdir -p /run/pia-tun

# Set VERSION and SHA as environment variables for runtime
ENV VERSION=${VERSION}
ENV SHA=${SHA}

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
    MTU="1420" \
    WG_BACKEND="" \
    IPT_BACKEND="" \
    PORT_FILE=/run/pia-tun/port \
    PS_CLIENT="" \
    PS_URL="" \
    PS_USER="" \
    PS_PASS="" \
    PS_CMD="" \
    PROXY_ENABLED=false \
    PROXY_USER="" \
    PROXY_PASS="" \
    SOCKS5_PORT=1080 \
    HTTP_PROXY_PORT=8888 \
    METRICS=true \
    METRICS_PORT=9090 \
    INSTANCE_NAME="" \
    HC_INTERVAL=10 \
    HC_FAILURE_WINDOW=30

EXPOSE 1080 8888 9090

HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 CMD wget -q --spider http://127.0.0.1:9090/health || exit 1  

ENTRYPOINT ["/usr/local/bin/pia-tun"]

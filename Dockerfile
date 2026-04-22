FROM golang:1.26.2-alpine3.23@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1 AS go-builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download
COPY cmd/pia-tun/ ./cmd/pia-tun/
COPY internal/ ./internal/

RUN cd cmd/pia-tun && \
    CGO_ENABLED=0 go build \
    -ldflags="-w -s" \
    -trimpath \
    -o /build/pia-tun . 

RUN CGO_ENABLED=0 go build \
    -ldflags="-w -s" \
    -trimpath \
    -o /build/wireguard-go \
    golang.zx2c4.com/wireguard

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

ARG VERSION=local
ARG SHA=local

RUN apk upgrade --no-cache && apk add --no-cache \
        bash \
        curl \
        wireguard-tools-wg \
        iptables \
        iptables-legacy \
        iproute2-minimal && \
    wg --version && \
    find /usr/bin /usr/sbin /bin /sbin -type f -executable \
        -exec strip --strip-all {} \; 2>/dev/null || true

# Create pia-tun directory for forwarded port and PIA's CA
RUN mkdir -p /run/pia-tun /etc/pia-tun

COPY --from=go-builder /build/pia-tun /usr/local/bin/pia-tun
COPY --from=go-builder /build/wireguard-go /usr/local/bin/wireguard-go

COPY ca/rsa_4096.crt /etc/pia-tun/ca.rsa.4096.crt

LABEL org.opencontainers.image.source="https://github.com/x0lie/pia-tun"

ENV VERSION=${VERSION} \
    SHA=${SHA}

ENV PIA_USER="" \
    PIA_PASS="" \
    PIA_LOCATIONS="all" \
    LOG_LEVEL=info \
    TZ="" \
    WG_BACKEND="" \
    MTU="1420" \
    LOCAL_NETWORKS="" \
    DNS="pia" \
    IPT_BACKEND="" \
    PF_ENABLED=false \
    PS_CLIENT="" \
    PS_URL="" \
    PS_USER="" \
    PS_PASS="" \
    PS_SCRIPT="" \
    PORT_FILE=/run/pia-tun/port \
    SOCKS5_ENABLED=false \
    SOCKS5_PORT=1080 \
    HTTP_PROXY_ENABLED=false \
    HTTP_PROXY_PORT=8888 \
    PROXY_USER="" \
    PROXY_PASS="" \
    HC_INTERVAL=10 \
    HC_FAILURE_WINDOW=30 \
    METRICS_ENABLED=true \
    METRICS_PORT=9090 \
    INSTANCE_NAME=""

EXPOSE 1080 8888 9090

HEALTHCHECK --interval=5s --timeout=5s --start-period=15s --retries=2 CMD wget -q --spider http://127.0.0.1:$METRICS_PORT/ready

ENTRYPOINT ["/usr/local/bin/pia-tun"]

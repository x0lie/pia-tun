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

# Health check
HEALTHCHECK --interval=60s --timeout=10s --start-period=30s --retries=3 \
    CMD wg show pia 2>/dev/null | grep -q "latest handshake" || exit 1

# Environment variables
ENV KILLSWITCH_EXEMPT_PORTS=""

# Performance-related sysctls should be set via docker run --sysctl
# Example: --sysctl net.core.rmem_max=26214400 --sysctl net.core.wmem_max=26214400

ENTRYPOINT ["/app/run.sh"]

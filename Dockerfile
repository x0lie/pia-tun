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
        speedtest-cli \
        wireguard-tools \
        ca-certificates \
        iproute2 \
        procps && \
    rm -rf /var/cache/apk/*

# Add PIA certificate
ADD https://raw.githubusercontent.com/pia-foss/desktop/master/daemon/res/ca/rsa_4096.crt /app/ca.rsa.4096.crt

# Create working dir
WORKDIR /app

# Copy scripts
COPY run.sh /app/run.sh
COPY scripts/ /app/scripts/
RUN chmod +x /app/run.sh /app/scripts/*.sh

# Volume for configs and port file
VOLUME ["/etc/wireguard"]
ENV PORT_FILE=/etc/wireguard/port

# Health check to verify VPN is up and not leaking
HEALTHCHECK --interval=60s --timeout=10s --start-period=30s --retries=3 \
    CMD wg show pia 2>/dev/null | grep -q "latest handshake" || exit 1

# Environment variable for exempt ports (comma-separated)
# Example: KILLSWITCH_EXEMPT_PORTS=8080,9090
ENV KILLSWITCH_EXEMPT_PORTS=""

# Entrypoint
ENTRYPOINT ["/app/run.sh"]

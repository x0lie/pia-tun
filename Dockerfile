FROM alpine:latest

# Install dependencies: WireGuard tools, curl for API, bash for scripts, iptables for networking, etc.
RUN apk update && \
    apk add --no-cache \
        jq \
        bc \
        curl \
        bash \
        iptables \
        ip6tables \
        iptables-legacy \
        speedtest-cli \
        wireguard-tools \
        ca-certificates && \
    rm -rf /var/cache/apk/*

# grepcidr3 \
# libcap-utils \
# openssl \
# sed \
# tini \

ADD https://raw.githubusercontent.com/pia-foss/desktop/master/daemon/res/ca/rsa_4096.crt /app/ca.rsa.4096.crt

# Create working dir for configs and scripts
WORKDIR /app

# Copy entrypoint and scripts
COPY run.sh /app/run.sh
COPY scripts/ /app/scripts/
RUN chmod +x /app/run.sh /app/scripts/*.sh

# Expose forwarded port info (if enabled, write to this file for host access)
VOLUME ["/etc/wireguard"]
ENV PORT_FILE=/etc/wireguard/port

# Entrypoint: Validate env, apply tweaks, run setup
ENTRYPOINT ["/app/run.sh"]

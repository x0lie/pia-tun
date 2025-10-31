#!/bin/bash
# UI and display functions for PIA WireGuard Container
# Provides consistent terminal output, colors, and status indicators

set -euo pipefail

# ANSI color codes
red=$'\033[0;31m'
grn=$'\033[0;32m'
blu=$'\033[0;34m'
cyn=$'\033[0;36m'
ylw=$'\033[0;33m'
nc=$'\033[0m'
bold=$'\033[1m'

# Trim whitespace from string (no subprocess overhead)
trim() {
    local var="$1"
    var="${var#"${var%%[![:space:]]*}"}"  # Remove leading whitespace
    var="${var%"${var##*[![:space:]]}"}"  # Remove trailing whitespace
    echo "$var"
}

# Print startup banner
print_banner() {
    # Only clear screen if running in interactive terminal
    # This prevents 'docker logs' from clearing the user's terminal
    [ -t 1 ] && clear

    cat << EOF
${cyn}╔════════════════════════════════════════════════╗${nc}
${cyn}║                                                ║${nc}
${cyn}║${nc}                 ${bold}pia-tun v0.9.1${nc}                 ${cyn}║${nc}
${cyn}║${nc}                     ${grn}x0lie${nc}                      ${cyn}║${nc}
${cyn}║                                                ║${nc}
${cyn}╚════════════════════════════════════════════════╝${nc}

EOF
}

# Status indicators
show_step() {
    echo "${blu}▶${nc} $1"
}

show_success() {
    echo "  ${grn}✓${nc} $1"
}

show_warning() {
    echo "  ${ylw}⚠${nc} $1"
}

show_error() {
    echo "  ${red}✗${nc} $1"
}

# VPN status boxes
show_vpn_connected() {
    cat << EOF

${grn}╔════════════════════════════════════════════════╗${nc}
${grn}║${nc}                ${grn}✓${nc} ${bold}VPN Connected${nc}                 ${grn}║${nc}
${grn}╚════════════════════════════════════════════════╝${nc}

EOF
}

show_vpn_connected_warning() {
    cat << EOF

${ylw}╔════════════════════════════════════════════════╗${nc}
${ylw}║${nc}                ${ylw}⚠${nc} ${bold}VPN Connected${nc}                 ${ylw}║${nc}
${ylw}╚════════════════════════════════════════════════╝${nc}

EOF
}

show_reconnecting() {
    cat << EOF

${ylw}╔════════════════════════════════════════════════╗${nc}
${ylw}║${nc}               ${ylw}↻${nc} ${bold}Reconnecting VPN${nc}               ${ylw}║${nc}
${ylw}╚════════════════════════════════════════════════╝${nc}

EOF
}

# Restart Docker containers after VPN reconnection
restart_services() {
    local services="$1"
    [ -z "$services" ] && return 0

    show_step "Restarting dependent services..."
    IFS=',' read -ra SERVICES <<< "$services"
    for service in "${SERVICES[@]}"; do
        service=$(trim "$service")
        [ -n "$service" ] && {
            echo "  ${blu}↻${nc} Restarting container: $service"
            docker restart "$service" >/dev/null 2>&1 || \
                echo "  ${ylw}⚠${nc} Could not restart $service"
        }
    done
    show_success "Dependent services restarted"
}

# Get external IP address with fallback to multiple services
get_external_ip() {
    local services=(
        "http://ifconfig.me"
        "http://icanhazip.com"
        "http://api.ipify.org"
    )

    for service in "${services[@]}"; do
        local ip=$(timeout 8 curl -s "$service" 2>/dev/null)
        # Valid IP found (and not a curl error message)
        if [[ -n "$ip" && "$ip" != "curl: "* ]]; then
            echo "$ip"
            return 0
        fi
    done

    # All services failed
    return 1
}

#!/bin/bash

# UI and display functions

# Colors
red=$'\033[0;31m'
grn=$'\033[0;32m'
blu=$'\033[0;34m'
cyn=$'\033[0;36m'
ylw=$'\033[0;33m'
nc=$'\033[0m'
bold=$'\033[1m'

# Trim whitespace using bash built-ins (no subprocess)
trim() {
    local var="$1"
    var="${var#"${var%%[![:space:]]*}"}"  # Remove leading
    var="${var%"${var##*[![:space:]]}"}"  # Remove trailing
    echo "$var"
}

print_banner() {
    clear
    echo "${cyn}╔════════════════════════════════════════════════╗${nc}"
    echo "${cyn}║                                                ║${nc}"
    echo "${cyn}║${nc}    ${bold}PIA WireGuard VPN Container${nc}                 ${cyn}║${nc}"
    echo "${cyn}║${nc}    ${grn}olsonalexw${nc}                                  ${cyn}║${nc}"
    echo "${cyn}║                                                ║${nc}"
    echo "${cyn}╚════════════════════════════════════════════════╝${nc}"
    echo ""
}

show_step() { echo "${blu}▶${nc} $1"; }
show_success() { echo "  ${grn}✓${nc} $1"; }
show_warning() { echo "  ${ylw}⚠${nc} $1"; }
show_error() { echo "  ${red}✗${nc} $1"; }

show_vpn_connected() {
    echo ""
    echo "${grn}╔════════════════════════════════════════════════╗${nc}"
    echo "${grn}║${nc}                ${grn}✓${nc} ${bold}VPN Connected${nc}                 ${grn}║${nc}"
    echo "${grn}╚════════════════════════════════════════════════╝${nc}"
    echo ""
}

show_vpn_connected_warning() {
    echo ""
    echo "${ylw}╔════════════════════════════════════════════════╗${nc}"
    echo "${ylw}║${nc}                ${ylw}⚠${nc} ${bold}VPN Connected${nc}                 ${ylw}║${nc}"
    echo "${ylw}╚════════════════════════════════════════════════╝${nc}"
    echo ""
}

show_reconnecting() {
    echo ""
    echo "${ylw}╔════════════════════════════════════════════════╗${nc}"
    echo "${ylw}║${nc}               ${ylw}↻${nc} ${bold}Reconnecting VPN${nc}               ${ylw}║${nc}"
    echo "${ylw}╚════════════════════════════════════════════════╝${nc}"
    echo ""
}

restart_services() {
    local services="$1"
    [ -z "$services" ] && return 0

    show_step "Restarting dependent services..."
    IFS=',' read -ra SERVICES <<< "$services"
    for service in "${SERVICES[@]}"; do
        service=$(trim "$service")
        [ -n "$service" ] && {
            echo "  ${blu}↻${nc} Restarting container: $service"
            docker restart "$service" 2>/dev/null || echo "  ${ylw}⚠${nc} Could not restart $service"
        }
    done
    show_success "Dependent services restarted"
}

curl_with_retry() {
    local url="$1" max_retries="${2:-3}" timeout="${3:-10}" extra_args="${4:-}"
    local retry=0 result
    
    while [ $retry -lt $max_retries ]; do
        result=$(curl -s -m "$timeout" $extra_args "$url" 2>&1) && { echo "$result"; return 0; }
        retry=$((retry + 1))
        [ $retry -lt $max_retries ] && sleep $((2 * retry))
    done
    return 1
}

get_external_ip() {
    local services=("http://ifconfig.me" "http://icanhazip.com" "http://api.ipify.org")
    for service in "${services[@]}"; do
        local ip=$(timeout 8 curl -s "$service" 2>/dev/null)
        [[ -n "$ip" && "$ip" != "curl: "* ]] && { echo "$ip"; return 0; }
    done
    return 1
}

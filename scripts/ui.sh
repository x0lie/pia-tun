#!/bin/bash

# UI and display functions

# Define colors
red=$'\033[0;31m'
grn=$'\033[0;32m'
blu=$'\033[0;34m'
cyn=$'\033[0;36m'
ylw=$'\033[0;33m'
nc=$'\033[0m'
bold=$'\033[1m'

# Print startup banner
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

# Progress indicators
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

# Status boxes
show_vpn_connected() {
    echo ""
    echo "${grn}╔═══════════════════════════════════════════════╗${nc}"
    echo "${grn}║${nc}                ${grn}✓${nc} ${bold}VPN Connected${nc}                 ${grn}║${nc}"
    echo "${grn}╚═══════════════════════════════════════════════╝${nc}"
    echo ""
}

show_vpn_connected_warning() {
    echo ""
    echo "${ylw}╔═══════════════════════════════════════════════╗${nc}"
    echo "${ylw}║${nc}                ${ylw}⚠${nc} ${bold}VPN Connected${nc}                 ${ylw}║${nc}"
    echo "${ylw}╚═══════════════════════════════════════════════╝${nc}"
    echo ""
}

show_reconnecting() {
    echo ""
    echo "${ylw}╔═══════════════════════════════════════════════╗${nc}"
    echo "${ylw}║${nc}               ${ylw}↻${nc} ${bold}Reconnecting VPN${nc}               ${ylw}║${nc}"
    echo "${ylw}╚═══════════════════════════════════════════════╝${nc}"
    echo ""
}

# Common service restart function
restart_services() {
    local services="$1"
    
    if [ -z "$services" ]; then
        return 0
    fi

    show_step "Restarting dependent services..."

    IFS=',' read -ra SERVICES <<< "$services"
    for service in "${SERVICES[@]}"; do
        service=$(echo "$service" | xargs)  # Trim whitespace
        if [ -n "$service" ]; then
            echo "  ${blu}↻${nc} Restarting container: $service"
            docker restart "$service" 2>/dev/null || echo "  ${ylw}⚠${nc} Could not restart $service (not found or no access)"
        fi
    done

    show_success "Dependent services restarted"
}

# Common curl retry function
curl_with_retry() {
    local url="$1"
    local max_retries="${2:-3}"
    local timeout="${3:-10}"
    local extra_args="${4:-}"
    local retry=0
    local result
    
    while [ $retry -lt $max_retries ]; do
        if result=$(curl -s -m "$timeout" $extra_args "$url" 2>&1); then
            echo "$result"
            return 0
        fi
        
        retry=$((retry + 1))
        if [ $retry -lt $max_retries ]; then
            sleep $((2 * retry))
        fi
    done
    
    return 1
}

# Common external IP check function
get_external_ip() {
    local services=("http://ifconfig.me" "http://icanhazip.com" "http://api.ipify.org")
    local ip=""
    
    for service in "${services[@]}"; do
        ip=$(timeout 8 curl -s "$service" 2>/dev/null || echo "")
        if [ -n "$ip" ] && [ "$ip" != "curl: "* ]; then
            echo "$ip"
            return 0
        fi
    done
    
    return 1
}

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

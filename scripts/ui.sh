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

# ============================================================================
# LOG LEVEL CONFIGURATION
# ============================================================================
# Log levels: 0=error, 1=info (default), 2=debug
LOG_LEVEL=${LOG_LEVEL:-info}

# Normalize to lowercase and convert to numeric level
case "${LOG_LEVEL,,}" in
    error|0) _LOG_LEVEL=0 ;;
    info|1)  _LOG_LEVEL=1 ;;
    debug|2) _LOG_LEVEL=2 ;;
    trace|3) _LOG_LEVEL=3 ;;
    *)       _LOG_LEVEL=1 ;; # default to info
esac

# Export for child processes
export LOG_LEVEL _LOG_LEVEL

# ============================================================================
# CORE LOGGING FUNCTIONS
# ============================================================================

# log_error: Always shown (even at error level)
log_error() {
    [[ $_LOG_LEVEL -ge 0 ]] && echo "$@" || true
}

# log_info: Shown at info and debug levels
log_info() {
    [[ $_LOG_LEVEL -ge 1 ]] && echo -e "$@" || true
}

# log_debug: Only shown at debug level
log_debug() {
    [[ $_LOG_LEVEL -ge 2 ]] && echo "$@" || true
}

# silence output
silent() {
    "$@" > /dev/null 2>&1
}

# ============================================================================
# UTILITY FUNCTIONS
# ============================================================================

# Trim whitespace from string (no subprocess overhead)
trim() {
    local var="$1"
    var="${var#"${var%%[![:space:]]*}"}"  # Remove leading whitespace
    var="${var%"${var##*[![:space:]]}"}"  # Remove trailing whitespace
    echo "$var"
}

# ============================================================================
# STATUS INDICATORS (respect log level)
# ============================================================================

# Print startup banner (info level and above)
print_banner() {
    [ $_LOG_LEVEL -lt 1 ] && return 0

    # Only clear screen if running in interactive terminal
    # This prevents 'docker logs' from clearing the user's terminal
    [ -t 1 ] && clear

    # Get version (default to "dev" if not set)
    local version="${VERSION:-dev}"

    # Add "v" prefix only for semantic versions (starting with a digit)
    if [[ $version =~ ^[0-9] ]]; then
        local version_text="pia-tun v${version}"
    else
        local version_text="pia-tun ${version}"
    fi

    local author_text="x0lie"

    # Fixed box width to match other banners (50 characters total)
    local box_width=50

    # Calculate padding for centering
    local version_len=${#version_text}
    local author_len=${#author_text}
    local version_padding=$(( (box_width - version_len - 2) / 2 ))
    local author_padding=$(( (box_width - author_len - 2) / 2 ))

    echo ""
    # Generate top border
    printf "${cyn}╔"
    printf '═%.0s' $(seq 1 $((box_width - 2)))
    printf "╗${nc}\n"

    # Empty line
    printf "${cyn}║"
    printf ' %.0s' $(seq 1 $((box_width - 2)))
    printf "║${nc}\n"

    # Version line (centered)
    printf "${cyn}║${nc}"
    printf ' %.0s' $(seq 1 $version_padding)
    printf "${bold}%s${nc}" "$version_text"
    printf ' %.0s' $(seq 1 $((box_width - version_len - version_padding - 2)))
    printf "${cyn}║${nc}\n"

    # Author line (centered)
    printf "${cyn}║${nc}"
    printf ' %.0s' $(seq 1 $author_padding)
    printf "${grn}%s${nc}" "$author_text"
    printf ' %.0s' $(seq 1 $((box_width - author_len - author_padding - 2)))
    printf "${cyn}║${nc}\n"

    # Empty line
    printf "${cyn}║"
    printf ' %.0s' $(seq 1 $((box_width - 2)))
    printf "║${nc}\n"

    # Bottom border
    printf "${cyn}╚"
    printf '═%.0s' $(seq 1 $((box_width - 2)))
    printf "╝${nc}\n"

    # Display commit SHA before banner for develop builds
    if [[ "$version" == "develop" && -n "${SHA:-}" && "$SHA" != "local" ]]; then
        echo "commit $SHA"
    fi
}

# Status indicators - info level
show_step() {
    log_info "\n${blu}▶${nc} $1"
}

show_success() {
    log_info "  ${grn}✓${nc} $1"
}

show_warning() {
    log_info "  ${ylw}⚠${nc} $1"
}

show_info() {
    log_info "${1:-}"
}

show_error() {
    log_error "  ${red}✗${nc} $1"
}

# Debug indicator - debug level only
show_debug() {
    log_debug "    ${blu}[DEBUG]${nc} killswitch: $1" >&2
}

# ============================================================================
# VPN STATUS BOXES (info level and above)
# ============================================================================

show_vpn_connected() {
    [ $_LOG_LEVEL -lt 1 ] && return 0
    
    cat << EOF

${grn}╔════════════════════════════════════════════════╗${nc}
${grn}║${nc}                ${grn}✓${nc} ${bold}VPN Connected${nc}                 ${grn}║${nc}
${grn}╚════════════════════════════════════════════════╝${nc}
EOF
}

show_vpn_connected_warning() {
    [ $_LOG_LEVEL -lt 1 ] && return 0
    
    cat << EOF

${ylw}╔════════════════════════════════════════════════╗${nc}
${ylw}║${nc}                ${ylw}⚠${nc} ${bold}VPN Connected${nc}                 ${ylw}║${nc}
${ylw}╚════════════════════════════════════════════════╝${nc}
EOF
}

show_reconnecting() {
    [ $_LOG_LEVEL -lt 1 ] && return 0
    
    cat << EOF

${ylw}╔════════════════════════════════════════════════╗${nc}
${ylw}║${nc}               ${ylw}↻${nc} ${bold}Reconnecting VPN${nc}               ${ylw}║${nc}
${ylw}╚════════════════════════════════════════════════╝${nc}
EOF
}

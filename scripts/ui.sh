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
    *)       _LOG_LEVEL=1 ;; # default to info
esac

# Export for child processes
export LOG_LEVEL _LOG_LEVEL

# ============================================================================
# CORE LOGGING FUNCTIONS
# ============================================================================

# log_error: Always shown (even at error level)
log_error() {
    [[ $_LOG_LEVEL -ge 0 ]] && echo "$@" >&2 || true
}

# log_info: Shown at info and debug levels
log_info() {
    [[ $_LOG_LEVEL -ge 1 ]] && echo "$@" || true
}

# log_debug: Only shown at debug level
log_debug() {
    [[ $_LOG_LEVEL -ge 2 ]] && echo "$@" >&2 || true
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

    cat << EOF
${cyn}╔════════════════════════════════════════════════╗${nc}
${cyn}║                                                ║${nc}
${cyn}║${nc}                 ${bold}pia-tun v0.9.1${nc}                 ${cyn}║${nc}
${cyn}║${nc}                     ${grn}x0lie${nc}                      ${cyn}║${nc}
${cyn}║                                                ║${nc}
${cyn}╚════════════════════════════════════════════════╝${nc}

EOF
}

# Status indicators - info level
show_step() {
    log_info "${blu}▶${nc} $1"
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
    log_error "  ${red}✗${nc} $1" >&2
}

# Debug indicator - debug level only
show_debug() {
    log_debug "    ${blu}[DEBUG]${nc} $1" >&2
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

# ============================================================================
# SERVICE MANAGEMENT
# ============================================================================

# Restart Docker containers after VPN reconnection
restart_services() {
    local services="$1"
    [ -z "$services" ] && return 0

    show_step "Restarting dependent services..."
    IFS=',' read -ra SERVICES <<< "$services"
    for service in "${SERVICES[@]}"; do
        service=$(trim "$service")
        [ -n "$service" ] && {
            log_info "  ${blu}↻${nc} Restarting container: $service"
            
            show_debug "Executing: docker restart $service"
            if docker restart "$service" >/dev/null 2>&1; then
                show_debug "Successfully restarted: $service"
            else
                log_info "  ${ylw}⚠${nc} Could not restart $service"
                show_debug "Docker restart failed for: $service"
            fi
        }
    done
    show_success "Dependent services restarted"
}

# ============================================================================
# NETWORK UTILITIES
# ============================================================================

# Get external IP address with fallback to multiple services
get_external_ip() {
    local services=(
        "http://ifconfig.me"
        "http://icanhazip.com"
        "http://api.ipify.org"
    )
    
    for service in "${services[@]}"; do
        show_debug "Trying service: $service"
        local ip=$(timeout 8 curl -s "$service" 2>/dev/null)
        
        # Valid IP found (and not a curl error message)
        if [[ -n "$ip" && "$ip" != "curl: "* ]]; then
            echo "$ip"
            return 0
        fi
        show_debug "Failed to get IP from $service"
    done

    # All services failed
    show_debug "All external IP services failed"
    return 1
}
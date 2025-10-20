#!/bin/bash
# Docker healthcheck script
# Returns 0 if VPN is healthy, 1 if unhealthy
#
# Healthcheck strategy:
# 1. If metrics enabled: Use the Go monitor's health endpoint (most reliable)
# 2. Fallback: Basic interface and connectivity checks

set -euo pipefail

# Check if metrics endpoint is available
check_metrics_endpoint() {
    [ "$METRICS" != "true" ] && return 1
    
    # Query the health endpoint (returns 200 if healthy, 503 if not)
    curl -f -s "http://localhost:${METRICS_PORT:-9090}/health" >/dev/null 2>&1
    return $?
}

# Fallback: Basic VPN health checks
check_basic_health() {
    # Check 1: Interface exists and is up
    if ! ip link show pia >/dev/null 2>&1; then
        return 1
    fi
    
    # Check 2: Basic connectivity test
    if ! ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1; then
        return 1
    fi
    
    return 0
}

# Main healthcheck logic
main() {
    # Try metrics endpoint first (most reliable)
    if check_metrics_endpoint; then
        exit 0
    fi
    
    # Fallback to basic checks
    if check_basic_health; then
        exit 0
    fi
    
    # Unhealthy
    exit 1
}

main

#!/bin/bash
# Docker healthcheck script
# Returns 0 if VPN is healthy, 1 if unhealthy
#
# Strategy: Delegate to Go monitor's /health endpoint when available
# The Go monitor is the authoritative source for health status

set -euo pipefail

# Primary: Use Go monitor's health endpoint (always available when METRICS=true)
if [ "${METRICS:-false}" = "true" ]; then
    curl -f -s "http://localhost:${METRICS_PORT:-9090}/health" >/dev/null 2>&1
    exit $?
fi

# Fallback: Lightweight checks for when metrics are disabled
# Check interface exists, has IP, and has basic connectivity
ip link show pia >/dev/null 2>&1 && \
  ip addr show pia | grep -q "inet " && \
  ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1
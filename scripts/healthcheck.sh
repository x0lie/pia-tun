#!/bin/bash
# Docker healthcheck script
# Uses our existing health endpoint if metrics are enabled, otherwise does basic checks

# If metrics endpoint is available, use it
if [ "$METRICS" = "true" ]; then
    # Check the health endpoint (returns 200 if healthy, 503 if not)
    curl -f -s http://localhost:${METRICS_PORT:-9090}/health >/dev/null 2>&1
    exit $?
fi

# Fallback: Basic checks if metrics aren't enabled
# 1. Check if interface exists and is up
if ! ip link show pia >/dev/null 2>&1; then
    exit 1
fi

# 2. Check if we can ping (basic connectivity test)
if ! ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1; then
    exit 1
fi

exit 0

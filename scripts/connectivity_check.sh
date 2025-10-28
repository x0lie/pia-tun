#!/bin/bash

# Pre-connection internet connectivity verification
# Checks if we have basic internet before attempting VPN connection

source /app/scripts/ui.sh

# Test multiple reliable endpoints
check_internet() {
    local max_attempts="${1:-3}"
    local attempt=1
    
    while [ $attempt -le $max_attempts ]; do
        # Try multiple DNS servers and HTTP endpoints
        # Using both methods increases reliability
        
        # Test 1: DNS resolution
        if timeout 5 nslookup google.com 1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        # Test 2: HTTP connectivity to Cloudflare
        if timeout 5 curl -s --max-time 5 http://1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        # Test 3: HTTP connectivity to Google DNS
        if timeout 5 curl -s --max-time 5 http://8.8.8.8 >/dev/null 2>&1; then
            return 0
        fi
        
        attempt=$((attempt + 1))
        [ $attempt -le $max_attempts ] && sleep 2
    done
    
    return 1
}

# Wait for internet with visual feedback
wait_for_internet() {
    local quiet="${1:-false}"
    local max_wait="${2:-300}"  # 5 minutes default
    local waited=0
    local check_interval=10
    
    [ "$quiet" != "true" ] && show_step "Checking internet connectivity..."
    
    # First quick check
    if check_internet 1; then
        [ "$quiet" != "true" ] && show_success "Internet connection available"
        return 0
    fi
    
    # Internet not available, wait for it
    [ "$quiet" != "true" ] && show_warning "No internet connection detected, waiting..."
    
    while [ $waited -lt $max_wait ]; do
        if check_internet 1; then
            [ "$quiet" != "true" ] && {
                echo ""
                show_success "Internet connection restored"
            }
            return 0
        fi
        
        # Show waiting indicator every 30 seconds
        if [ $((waited % 30)) -eq 0 ] && [ "$quiet" != "true" ]; then
            echo "  ${ylw}⏳${nc} Still waiting for internet... (${waited}s elapsed)"
        fi
        
        sleep $check_interval
        waited=$((waited + check_interval))
    done
    
    [ "$quiet" != "true" ] && show_error "Internet connection timeout after ${max_wait}s"
    return 1
}

# Export functions
export -f check_internet
export -f wait_for_internet

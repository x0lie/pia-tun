#!/bin/bash

# Pre-connection internet connectivity verification
# Checks if we have basic internet before attempting VPN connection

source /app/scripts/ui.sh

# Test multiple reliable endpoints
check_internet() {
    local max_attempts="${1:-3}"
    local attempt=1
    
    show_debug "check_internet: Starting (max_attempts: $max_attempts)"
    
    while [ $attempt -le $max_attempts ]; do
        show_debug "Internet connectivity check attempt $attempt/$max_attempts"
        
        # Try multiple DNS servers and HTTP endpoints
        # Using both methods increases reliability
        
        # Test 1: DNS resolution
        show_debug "Test 1: DNS resolution (google.com via 1.1.1.1)"
        if timeout 5 nslookup google.com 1.1.1.1 >/dev/null 2>&1; then
            show_debug "DNS resolution successful"
            return 0
        fi
        show_debug "DNS resolution failed"
        
        # Test 2: HTTP connectivity to Cloudflare
        show_debug "Test 2: HTTP connectivity to 1.1.1.1"
        if timeout 5 curl -s --max-time 5 http://1.1.1.1 >/dev/null 2>&1; then
            show_debug "Cloudflare HTTP check successful"
            return 0
        fi
        show_debug "Cloudflare HTTP check failed"
        
        # Test 3: HTTP connectivity to Google DNS
        show_debug "Test 3: HTTP connectivity to 8.8.8.8"
        if timeout 5 curl -s --max-time 5 http://8.8.8.8 >/dev/null 2>&1; then
            show_debug "Google DNS HTTP check successful"
            return 0
        fi
        show_debug "Google DNS HTTP check failed"
        
        attempt=$((attempt + 1))
        if [ $attempt -le $max_attempts ]; then
            show_debug "All tests failed, waiting 2s before retry..."
            sleep 2
        fi
    done
    
    show_debug "All internet connectivity checks failed after $max_attempts attempts"
    return 1
}

# Wait for internet with visual feedback
wait_for_internet() {
    local quiet="${1:-false}"
    local max_wait="${2:-300}"  # 5 minutes default
    local waited=0
    local check_interval=10
    
    show_debug "wait_for_internet: Starting (quiet=$quiet, max_wait=${max_wait}s)"
    
    [ "$quiet" != "true" ] && show_step "Checking internet connectivity..."
    
    # First quick check
    show_debug "Performing initial quick connectivity check"
    if check_internet 1; then
        [ "$quiet" != "true" ] && show_success "Internet connection available"
        show_debug "Internet available on first check"
        return 0
    fi
    
    # Internet not available, wait for it
    [ "$quiet" != "true" ] && show_warning "No internet connection detected, waiting..."
    show_debug "No internet detected, entering wait loop (interval: ${check_interval}s)"
    
    local loop_count=0
    while [ $waited -lt $max_wait ]; do
        loop_count=$((loop_count + 1))
        show_debug "Wait loop iteration $loop_count: checking connectivity (waited: ${waited}s/$max_wait)"
        
        if check_internet 1; then
            [ "$quiet" != "true" ] && {
                show_info
                show_success "Internet connection restored"
            }
            show_debug "Internet connection restored after ${waited}s"
            return 0
        fi
        
        # Show waiting indicator every 30 seconds
        if [ $((waited % 30)) -eq 0 ] && [ "$quiet" != "true" ]; then
            echo "  ${ylw}⏳${nc} Still waiting for internet... (${waited}s elapsed)"
            show_debug "Wait status: ${waited}s elapsed, continuing..."
        fi
        
        sleep $check_interval
        waited=$((waited + check_interval))
    done
    
    [ "$quiet" != "true" ] && show_error "Internet connection timeout after ${max_wait}s"
    show_debug "Internet wait timeout after ${max_wait}s (${loop_count} iterations)"
    return 1
}

# Export functions
export -f check_internet
export -f wait_for_internet
#!/bin/bash
# Connectivity utilities for distinguishing internet vs tunnel failures
# Allows checking internet connectivity without going through the VPN tunnel

set -euo pipefail

# Get the physical interface (non-VPN, non-loopback)
get_physical_interface() {
    # Get default route interface (before VPN takes over)
    # Look in main routing table, excluding VPN interface
    local phys_iface=$(ip route show table main | grep "^default" | grep -v pia | awk '{print $5}' | head -1)
    
    if [ -z "$phys_iface" ]; then
        # Fallback: try common interface names
        for iface in eth0 ens18 enp0s3 wlan0 enp1s0; do
            if ip link show "$iface" >/dev/null 2>&1; then
                phys_iface="$iface"
                break
            fi
        done
    fi
    
    if [ -z "$phys_iface" ]; then
        # Last resort: use any non-VPN interface
        phys_iface=$(ip -o link show | grep -v "lo\|pia" | awk -F': ' '{print $2}' | head -1)
    fi
    
    echo "$phys_iface"
}

# Get the default gateway (before VPN)
get_physical_gateway() {
    ip route show table main | grep "^default" | grep -v pia | awk '{print $3}' | head -1
}

# Check if internet is available (bypassing VPN tunnel)
# Returns 0 if internet is working, 1 if not
# Uses fwmark to bypass killswitch routing rules
check_internet_direct() {
    local max_attempts="${1:-3}"
    local attempt=1
    
    # Get physical interface
    local phys_iface=$(get_physical_interface)
    
    if [ -z "$phys_iface" ]; then
        return 1
    fi
    
    # Use a special fwmark to identify internet check traffic
    # This allows the killswitch to exempt it
    local check_mark="0x9999"
    
    while [ $attempt -le $max_attempts ]; do
        # Method 1: Ping via physical interface with fwmark
        # The fwmark tells the killswitch to allow this traffic
        if timeout 5 ping -c 1 -W 3 -m $check_mark -I "$phys_iface" 1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        # Method 2: Try gateway ping (more direct, less likely to be blocked upstream)
        local gateway=$(get_physical_gateway)
        if [ -n "$gateway" ]; then
            if timeout 5 ping -c 1 -W 3 -m $check_mark -I "$phys_iface" "$gateway" >/dev/null 2>&1; then
                return 0
            fi
        fi
        
        # Method 3: Try 8.8.8.8 as fallback
        if timeout 5 ping -c 1 -W 3 -m $check_mark -I "$phys_iface" 8.8.8.8 >/dev/null 2>&1; then
            return 0
        fi
        
        attempt=$((attempt + 1))
        [ $attempt -le $max_attempts ] && sleep 2
    done
    
    return 1
}

# Check tunnel connectivity (expects to go through VPN)
check_tunnel_connectivity() {
    local max_attempts="${1:-2}"
    local attempt=1
    
    # Check if VPN interface exists first
    if ! ip link show pia >/dev/null 2>&1; then
        return 1
    fi
    
    while [ $attempt -le $max_attempts ]; do
        # These should go through the tunnel
        if timeout 5 ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        if timeout 5 curl -s --max-time 5 http://1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        attempt=$((attempt + 1))
        [ $attempt -le $max_attempts ] && sleep 2
    done
    
    return 1
}

# Smart connectivity check: Determines what's down
# Returns:
#   0 - Everything working (tunnel + internet)
#   1 - Tunnel down, internet up (RECONNECT)
#   2 - Internet down (WAIT)
#   3 - Both down (WAIT)
smart_connectivity_check() {
    local tunnel_ok=false
    local internet_ok=false
    
    # Check tunnel first (fast fail)
    if check_tunnel_connectivity 1; then
        tunnel_ok=true
    fi
    
    # If tunnel is ok, we're done
    if $tunnel_ok; then
        return 0
    fi
    
    # Tunnel is down - check if internet is available
    if check_internet_direct 2; then
        internet_ok=true
    fi
    
    # Determine status
    if $internet_ok; then
        # Internet works, tunnel doesn't → Reconnect VPN
        return 1
    else
        # Internet is down → Wait for internet
        return 2
    fi
}

# Wait for internet to come back (with timeout)
wait_for_internet_recovery() {
    local max_wait="${1:-300}"  # 5 minutes default
    local waited=0
    local check_interval=10
    
    echo "  ${ylw}⏳${nc} Internet connection lost, waiting for recovery..."
    
    while [ $waited -lt $max_wait ]; do
        if check_internet_direct 1; then
            echo "  ${grn}✓${nc} Internet connection restored after ${waited}s"
            return 0
        fi
        
        # Show waiting indicator every 30 seconds
        if [ $((waited % 30)) -eq 0 ] && [ $waited -gt 0 ]; then
            echo "  ${ylw}⏳${nc} Still waiting for internet... (${waited}s elapsed)"
        fi
        
        sleep $check_interval
        waited=$((waited + check_interval))
    done
    
    echo "  ${red}✗${nc} Internet did not recover after ${max_wait}s"
    return 1
}

# Export functions for use in other scripts
export -f get_physical_interface
export -f get_physical_gateway
export -f check_internet_direct
export -f check_tunnel_connectivity
export -f smart_connectivity_check
export -f wait_for_internet_recovery

# Load colors if not already loaded
if [ -z "${grn:-}" ]; then
    red=

# Check tunnel connectivity (expects to go through VPN)
check_tunnel_connectivity() {
    local max_attempts="${1:-2}"
    local attempt=1
    
    while [ $attempt -le $max_attempts ]; do
        # These should go through the tunnel
        if timeout 5 ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        if timeout 5 curl -s --max-time 5 http://1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        attempt=$((attempt + 1))
        [ $attempt -le $max_attempts ] && sleep 2
    done
    
    return 1
}

# Smart connectivity check: Determines what's down
# Returns:
#   0 - Everything working (tunnel + internet)
#   1 - Tunnel down, internet up (RECONNECT)
#   2 - Internet down (WAIT)
#   3 - Both down (WAIT)
smart_connectivity_check() {
    local tunnel_ok=false
    local internet_ok=false
    
    # Check tunnel first (fast fail)
    if check_tunnel_connectivity 1; then
        tunnel_ok=true
    fi
    
    # If tunnel is ok, we're done
    if $tunnel_ok; then
        return 0
    fi
    
    # Tunnel is down - check if internet is available
    if check_internet_direct 2; then
        internet_ok=true
    fi
    
    # Determine status
    if $internet_ok; then
        # Internet works, tunnel doesn't → Reconnect VPN
        return 1
    else
        # Internet is down → Wait for internet
        return 2
    fi
}

# Wait for internet to come back (with timeout)
wait_for_internet_recovery() {
    local max_wait="${1:-300}"  # 5 minutes default
    local waited=0
    local check_interval=10
    
    echo "  ${ylw}⏳${nc} Internet connection lost, waiting for recovery..."
    
    while [ $waited -lt $max_wait ]; do
        if check_internet_direct 1; then
            echo "  ${grn}✓${nc} Internet connection restored after ${waited}s"
            return 0
        fi
        
        # Show waiting indicator every 30 seconds
        if [ $((waited % 30)) -eq 0 ] && [ $waited -gt 0 ]; then
            echo "  ${ylw}⏳${nc} Still waiting for internet... (${waited}s elapsed)"
        fi
        
        sleep $check_interval
        waited=$((waited + check_interval))
    done
    
    echo "  ${red}✗${nc} Internet did not recover after ${max_wait}s"
    return 1
}

# Export functions for use in other scripts
export -f get_physical_interface
export -f get_physical_gateway
export -f check_internet_direct
export -f check_tunnel_connectivity
export -f smart_connectivity_check
export -f wait_for_internet_recovery

# Load colors if not already loaded
if [ -z "${grn:-}" ]; then
    red=$'\033[0;31m'
    grn=$'\033[0;32m'
    ylw=$'\033[0;33m'
    nc=$'\033[0m'
fi
\033[0;31m'
    grn=

# Check tunnel connectivity (expects to go through VPN)
check_tunnel_connectivity() {
    local max_attempts="${1:-2}"
    local attempt=1
    
    while [ $attempt -le $max_attempts ]; do
        # These should go through the tunnel
        if timeout 5 ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        if timeout 5 curl -s --max-time 5 http://1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        attempt=$((attempt + 1))
        [ $attempt -le $max_attempts ] && sleep 2
    done
    
    return 1
}

# Smart connectivity check: Determines what's down
# Returns:
#   0 - Everything working (tunnel + internet)
#   1 - Tunnel down, internet up (RECONNECT)
#   2 - Internet down (WAIT)
#   3 - Both down (WAIT)
smart_connectivity_check() {
    local tunnel_ok=false
    local internet_ok=false
    
    # Check tunnel first (fast fail)
    if check_tunnel_connectivity 1; then
        tunnel_ok=true
    fi
    
    # If tunnel is ok, we're done
    if $tunnel_ok; then
        return 0
    fi
    
    # Tunnel is down - check if internet is available
    if check_internet_direct 2; then
        internet_ok=true
    fi
    
    # Determine status
    if $internet_ok; then
        # Internet works, tunnel doesn't → Reconnect VPN
        return 1
    else
        # Internet is down → Wait for internet
        return 2
    fi
}

# Wait for internet to come back (with timeout)
wait_for_internet_recovery() {
    local max_wait="${1:-300}"  # 5 minutes default
    local waited=0
    local check_interval=10
    
    echo "  ${ylw}⏳${nc} Internet connection lost, waiting for recovery..."
    
    while [ $waited -lt $max_wait ]; do
        if check_internet_direct 1; then
            echo "  ${grn}✓${nc} Internet connection restored after ${waited}s"
            return 0
        fi
        
        # Show waiting indicator every 30 seconds
        if [ $((waited % 30)) -eq 0 ] && [ $waited -gt 0 ]; then
            echo "  ${ylw}⏳${nc} Still waiting for internet... (${waited}s elapsed)"
        fi
        
        sleep $check_interval
        waited=$((waited + check_interval))
    done
    
    echo "  ${red}✗${nc} Internet did not recover after ${max_wait}s"
    return 1
}

# Export functions for use in other scripts
export -f get_physical_interface
export -f get_physical_gateway
export -f check_internet_direct
export -f check_tunnel_connectivity
export -f smart_connectivity_check
export -f wait_for_internet_recovery

# Load colors if not already loaded
if [ -z "${grn:-}" ]; then
    red=$'\033[0;31m'
    grn=$'\033[0;32m'
    ylw=$'\033[0;33m'
    nc=$'\033[0m'
fi
\033[0;32m'
    ylw=

# Check tunnel connectivity (expects to go through VPN)
check_tunnel_connectivity() {
    local max_attempts="${1:-2}"
    local attempt=1
    
    while [ $attempt -le $max_attempts ]; do
        # These should go through the tunnel
        if timeout 5 ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        if timeout 5 curl -s --max-time 5 http://1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        attempt=$((attempt + 1))
        [ $attempt -le $max_attempts ] && sleep 2
    done
    
    return 1
}

# Smart connectivity check: Determines what's down
# Returns:
#   0 - Everything working (tunnel + internet)
#   1 - Tunnel down, internet up (RECONNECT)
#   2 - Internet down (WAIT)
#   3 - Both down (WAIT)
smart_connectivity_check() {
    local tunnel_ok=false
    local internet_ok=false
    
    # Check tunnel first (fast fail)
    if check_tunnel_connectivity 1; then
        tunnel_ok=true
    fi
    
    # If tunnel is ok, we're done
    if $tunnel_ok; then
        return 0
    fi
    
    # Tunnel is down - check if internet is available
    if check_internet_direct 2; then
        internet_ok=true
    fi
    
    # Determine status
    if $internet_ok; then
        # Internet works, tunnel doesn't → Reconnect VPN
        return 1
    else
        # Internet is down → Wait for internet
        return 2
    fi
}

# Wait for internet to come back (with timeout)
wait_for_internet_recovery() {
    local max_wait="${1:-300}"  # 5 minutes default
    local waited=0
    local check_interval=10
    
    echo "  ${ylw}⏳${nc} Internet connection lost, waiting for recovery..."
    
    while [ $waited -lt $max_wait ]; do
        if check_internet_direct 1; then
            echo "  ${grn}✓${nc} Internet connection restored after ${waited}s"
            return 0
        fi
        
        # Show waiting indicator every 30 seconds
        if [ $((waited % 30)) -eq 0 ] && [ $waited -gt 0 ]; then
            echo "  ${ylw}⏳${nc} Still waiting for internet... (${waited}s elapsed)"
        fi
        
        sleep $check_interval
        waited=$((waited + check_interval))
    done
    
    echo "  ${red}✗${nc} Internet did not recover after ${max_wait}s"
    return 1
}

# Export functions for use in other scripts
export -f get_physical_interface
export -f get_physical_gateway
export -f check_internet_direct
export -f check_tunnel_connectivity
export -f smart_connectivity_check
export -f wait_for_internet_recovery

# Load colors if not already loaded
if [ -z "${grn:-}" ]; then
    red=$'\033[0;31m'
    grn=$'\033[0;32m'
    ylw=$'\033[0;33m'
    nc=$'\033[0m'
fi
\033[0;33m'
    nc=

# Check tunnel connectivity (expects to go through VPN)
check_tunnel_connectivity() {
    local max_attempts="${1:-2}"
    local attempt=1
    
    while [ $attempt -le $max_attempts ]; do
        # These should go through the tunnel
        if timeout 5 ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        if timeout 5 curl -s --max-time 5 http://1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        attempt=$((attempt + 1))
        [ $attempt -le $max_attempts ] && sleep 2
    done
    
    return 1
}

# Smart connectivity check: Determines what's down
# Returns:
#   0 - Everything working (tunnel + internet)
#   1 - Tunnel down, internet up (RECONNECT)
#   2 - Internet down (WAIT)
#   3 - Both down (WAIT)
smart_connectivity_check() {
    local tunnel_ok=false
    local internet_ok=false
    
    # Check tunnel first (fast fail)
    if check_tunnel_connectivity 1; then
        tunnel_ok=true
    fi
    
    # If tunnel is ok, we're done
    if $tunnel_ok; then
        return 0
    fi
    
    # Tunnel is down - check if internet is available
    if check_internet_direct 2; then
        internet_ok=true
    fi
    
    # Determine status
    if $internet_ok; then
        # Internet works, tunnel doesn't → Reconnect VPN
        return 1
    else
        # Internet is down → Wait for internet
        return 2
    fi
}

# Wait for internet to come back (with timeout)
wait_for_internet_recovery() {
    local max_wait="${1:-300}"  # 5 minutes default
    local waited=0
    local check_interval=10
    
    echo "  ${ylw}⏳${nc} Internet connection lost, waiting for recovery..."
    
    while [ $waited -lt $max_wait ]; do
        if check_internet_direct 1; then
            echo "  ${grn}✓${nc} Internet connection restored after ${waited}s"
            return 0
        fi
        
        # Show waiting indicator every 30 seconds
        if [ $((waited % 30)) -eq 0 ] && [ $waited -gt 0 ]; then
            echo "  ${ylw}⏳${nc} Still waiting for internet... (${waited}s elapsed)"
        fi
        
        sleep $check_interval
        waited=$((waited + check_interval))
    done
    
    echo "  ${red}✗${nc} Internet did not recover after ${max_wait}s"
    return 1
}

# Export functions for use in other scripts
export -f get_physical_interface
export -f get_physical_gateway
export -f check_internet_direct
export -f check_tunnel_connectivity
export -f smart_connectivity_check
export -f wait_for_internet_recovery

# Load colors if not already loaded
if [ -z "${grn:-}" ]; then
    red=$'\033[0;31m'
    grn=$'\033[0;32m'
    ylw=$'\033[0;33m'
    nc=$'\033[0m'
fi
\033[0m'
fi

# Check tunnel connectivity (expects to go through VPN)
check_tunnel_connectivity() {
    local max_attempts="${1:-2}"
    local attempt=1
    
    while [ $attempt -le $max_attempts ]; do
        # These should go through the tunnel
        if timeout 5 ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        if timeout 5 curl -s --max-time 5 http://1.1.1.1 >/dev/null 2>&1; then
            return 0
        fi
        
        attempt=$((attempt + 1))
        [ $attempt -le $max_attempts ] && sleep 2
    done
    
    return 1
}

# Smart connectivity check: Determines what's down
# Returns:
#   0 - Everything working (tunnel + internet)
#   1 - Tunnel down, internet up (RECONNECT)
#   2 - Internet down (WAIT)
#   3 - Both down (WAIT)
smart_connectivity_check() {
    local tunnel_ok=false
    local internet_ok=false
    
    # Check tunnel first (fast fail)
    if check_tunnel_connectivity 1; then
        tunnel_ok=true
    fi
    
    # If tunnel is ok, we're done
    if $tunnel_ok; then
        return 0
    fi
    
    # Tunnel is down - check if internet is available
    if check_internet_direct 2; then
        internet_ok=true
    fi
    
    # Determine status
    if $internet_ok; then
        # Internet works, tunnel doesn't → Reconnect VPN
        return 1
    else
        # Internet is down → Wait for internet
        return 2
    fi
}

# Wait for internet to come back (with timeout)
wait_for_internet_recovery() {
    local max_wait="${1:-300}"  # 5 minutes default
    local waited=0
    local check_interval=10
    
    echo "  ${ylw}⏳${nc} Internet connection lost, waiting for recovery..."
    
    while [ $waited -lt $max_wait ]; do
        if check_internet_direct 1; then
            echo "  ${grn}✓${nc} Internet connection restored after ${waited}s"
            return 0
        fi
        
        # Show waiting indicator every 30 seconds
        if [ $((waited % 30)) -eq 0 ] && [ $waited -gt 0 ]; then
            echo "  ${ylw}⏳${nc} Still waiting for internet... (${waited}s elapsed)"
        fi
        
        sleep $check_interval
        waited=$((waited + check_interval))
    done
    
    echo "  ${red}✗${nc} Internet did not recover after ${max_wait}s"
    return 1
}

# Export functions for use in other scripts
export -f get_physical_interface
export -f get_physical_gateway
export -f check_internet_direct
export -f check_tunnel_connectivity
export -f smart_connectivity_check
export -f wait_for_internet_recovery

# Load colors if not already loaded
if [ -z "${grn:-}" ]; then
    red=$'\033[0;31m'
    grn=$'\033[0;32m'
    ylw=$'\033[0;33m'
    nc=$'\033[0m'
fi

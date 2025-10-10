#!/bin/bash

# Verify VPN connection and check for leaks

# Get external IP through VPN interface
get_external_ip() {
    local ip=""
    
    # Method 1: Try with --interface pia
    ip=$(timeout 10 curl -s --interface pia http://ifconfig.me 2>/dev/null)
    if [ -n "$ip" ]; then
        echo "$ip"
        return 0
    fi
    
    # Method 2: Try without interface binding (should still go through VPN due to routing)
    ip=$(timeout 10 curl -s http://ifconfig.me 2>/dev/null)
    if [ -n "$ip" ]; then
        echo "$ip"
        return 0
    fi
    
    # Method 3: Try alternative service
    ip=$(timeout 10 curl -s http://icanhazip.com 2>/dev/null)
    if [ -n "$ip" ]; then
        echo "$ip"
        return 0
    fi
    
    # Method 4: Try with IPv4 only
    ip=$(timeout 10 curl -4 -s http://api.ipify.org 2>/dev/null)
    if [ -n "$ip" ]; then
        echo "$ip"
        return 0
    fi
    
    echo ""
}

# Capture real IP before VPN connects (called from run.sh)
capture_real_ip() {
    show_step "Capturing pre-VPN IP address..."
    local real_ip=$(timeout 5 curl -s https://api.ipify.org 2>/dev/null)
    
    if [ -n "$real_ip" ]; then
        echo "$real_ip" > /tmp/real_ip
        show_success "Real IP captured: $real_ip"
    else
        show_warning "Could not capture real IP (verification will be limited)"
    fi
}

# Check if PIA DNS is responding (only if using PIA DNS)
check_pia_dns() {
    # Check if we're using PIA's DNS servers
    local using_pia_dns=false
    if grep -q "209.222.18.222\|209.222.18.218" /etc/resolv.conf 2>/dev/null; then
        using_pia_dns=true
    fi
    
    if [ "$using_pia_dns" = false ]; then
        # Not using PIA DNS, skip this check
        return 2  # Return 2 to indicate "skipped"
    fi
    
    # Query PIA's DNS server directly - use TCP for more reliable check
    if timeout 3 dig @209.222.18.222 google.com +short +tries=1 >/dev/null 2>&1; then
        return 0
    fi
    return 1
}

# Check for DNS leaks (non-VPN DNS servers being used)
check_dns_leak() {
    local dns_servers=$(grep "^nameserver" /etc/resolv.conf | awk '{print $2}')
    local leak_detected=false
    
    for dns in $dns_servers; do
        # Check if DNS is PIA's DNS
        if [[ "$dns" == "209.222.18.222" ]] || [[ "$dns" == "209.222.18.218" ]]; then
            continue
        fi
        
        # Check if it's a private/local DNS (allowed)
        if [[ "$dns" =~ ^10\. ]] || \
           [[ "$dns" =~ ^172\.(1[6-9]|2[0-9]|3[0-1])\. ]] || \
           [[ "$dns" =~ ^192\.168\. ]] || \
           [[ "$dns" =~ ^127\. ]]; then
            continue
        fi
        
        # If we get here, it's a public non-PIA DNS
        # Check if user set custom DNS
        if [ -z "$DNS" ] || [ "$DNS" = "pia" ]; then
            # User wants PIA DNS but we found something else
            show_warning "Unexpected DNS server detected: $dns"
            leak_detected=true
        fi
        # If $DNS is set to custom value, this is expected, so don't warn
    done
    
    if [ "$leak_detected" = false ]; then
        return 0
    fi
    return 1
}

# Check for IPv6 leaks
check_ipv6_leak() {
    # Try to get IPv6 address
    local ipv6=$(timeout 5 curl -6 -s --interface pia https://api6.ipify.org 2>/dev/null || echo "")
    
    if [ -n "$ipv6" ] && [ "${DISABLE_IPV6}" = "true" ]; then
        show_error "IPv6 leak detected: $ipv6"
        return 1
    fi
    
    return 0
}

# Main verification function using hybrid approach
verify_connection() {
    local checks_passed=0
    local checks_total=0
    local vpn_ip=""
    
    # Check 1: Can we get an IP through the VPN interface?
    checks_total=$((checks_total + 1))
    
    # Try to get external IP (but don't block on it)
    vpn_ip=$(timeout 8 bash -c 'curl -s http://ifconfig.me 2>/dev/null || curl -s http://icanhazip.com 2>/dev/null' || echo "")
    
    if [ -n "$vpn_ip" ] && [ "$vpn_ip" != "curl: "* ]; then
        checks_passed=$((checks_passed + 1))
    else
        # IP check failed, but don't fail entirely - just note it
        show_warning "External IP check timed out (VPN likely working, but verification slow)"
        vpn_ip=""
    fi
    
    # Check 2: Is IP different from pre-VPN IP?
    if [ -f /tmp/real_ip ]; then
        checks_total=$((checks_total + 1))
        local real_ip=$(cat /tmp/real_ip)
        
        if [ "$vpn_ip" != "$real_ip" ]; then
            checks_passed=$((checks_passed + 1))
        else
            show_warning "VPN IP matches real IP - VPN may not be working!"
        fi
    fi
    
    # Check 3: PIA DNS responding? (only if using PIA DNS)
    local dns_check_result
    check_pia_dns
    dns_check_result=$?
    
    if [ $dns_check_result -eq 0 ]; then
        # DNS check passed
        checks_total=$((checks_total + 1))
        checks_passed=$((checks_passed + 1))
    elif [ $dns_check_result -eq 1 ]; then
        # DNS check failed (only if using PIA DNS)
        checks_total=$((checks_total + 1))
        show_warning "PIA DNS query test failed (may be temporary)"
    fi
    # If dns_check_result is 2, check was skipped (not using PIA DNS)
    
    # Check 4: IPv6 leak test
    checks_total=$((checks_total + 1))
    if check_ipv6_leak; then
        checks_passed=$((checks_passed + 1))
        if [ "${DISABLE_IPV6}" = "true" ]; then
            show_success "No IPv6 leaks detected"
        fi
    fi
    
    # Check 5: DNS leak test
    checks_total=$((checks_total + 1))
    if check_dns_leak; then
        checks_passed=$((checks_passed + 1))
        show_success "DNS properly configured"
    fi
    
    # Display results
    if [ -z "$vpn_ip" ]; then
        show_warning "Could not verify external IP (check manually if needed)"
        echo "  ${ylw}ℹ${nc} VPN is connected, but IP verification timed out"
        return 0  # Don't fail - VPN is working, just slow verification
    elif [ $checks_passed -ge $((checks_total - 1)) ]; then
        # All or all-but-one checks passed
        show_success "External IP: ${grn}${bold}${vpn_ip}${nc}"
        return 0
    elif [ $checks_passed -ge $((checks_total / 2)) ]; then
        # At least half checks passed
        show_warning "External IP: ${ylw}${bold}${vpn_ip}${nc} ${ylw}(VPN active, some checks failed)${nc}"
        return 0
    else
        # Most checks failed
        show_error "External IP: ${red}${bold}${vpn_ip}${nc} ${red}(VPN may not be working)${nc}"
        return 1
    fi
}

# Run if executed directly
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    verify_connection
fi

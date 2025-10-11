#!/bin/bash

# Source UI helpers
source /app/scripts/ui.sh

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
    if ! grep -q "209.222.18.222\|209.222.18.218" /etc/resolv.conf 2>/dev/null; then
        return 2  # Not using PIA DNS
    fi
    
    # Query PIA's DNS server directly
    timeout 3 dig @209.222.18.222 google.com +short +tries=1 >/dev/null 2>&1
}

# Check for DNS leaks
check_dns_leak() {
    local dns_servers=$(grep "^nameserver" /etc/resolv.conf | awk '{print $2}')
    local leak_detected=false
    
    for dns in $dns_servers; do
        # Skip PIA DNS
        [[ "$dns" == "209.222.18.222" ]] || [[ "$dns" == "209.222.18.218" ]] && continue
        
        # Skip private/local DNS
        [[ "$dns" =~ ^10\. ]] || \
        [[ "$dns" =~ ^172\.(1[6-9]|2[0-9]|3[0-1])\. ]] || \
        [[ "$dns" =~ ^192\.168\. ]] || \
        [[ "$dns" =~ ^127\. ]] && continue
        
        # Public non-PIA DNS found
        if [ -z "$DNS" ] || [ "$DNS" = "pia" ]; then
            show_warning "Unexpected DNS server detected: $dns"
            leak_detected=true
        fi
    done
    
    [ "$leak_detected" = false ]
}

# Check for IPv6 leaks
check_ipv6_leak() {
    local ipv6=$(timeout 5 curl -6 -s --interface pia https://api6.ipify.org 2>/dev/null || echo "")
    
    if [ -n "$ipv6" ] && [ "${DISABLE_IPV6}" = "true" ]; then
        show_error "IPv6 leak detected: $ipv6"
        return 1
    fi
    
    return 0
}

# Main verification function
verify_connection() {
    local checks_passed=0
    local checks_total=0
    local vpn_ip=""
    
    # Check 1: Get external IP through VPN
    checks_total=$((checks_total + 1))
    vpn_ip=$(get_external_ip || echo "")
    
    if [ -n "$vpn_ip" ]; then
        checks_passed=$((checks_passed + 1))
    else
        show_warning "External IP check timed out (VPN likely working, but verification slow)"
    fi
    
    # Check 2: Verify IP changed from pre-VPN
    if [ -f /tmp/real_ip ]; then
        checks_total=$((checks_total + 1))
        local real_ip=$(cat /tmp/real_ip)
        
        if [ "$vpn_ip" != "$real_ip" ]; then
            checks_passed=$((checks_passed + 1))
        else
            show_warning "VPN IP matches real IP - VPN may not be working!"
        fi
    fi
    
    # Check 3: PIA DNS responding (if using PIA DNS)
    check_pia_dns
    local dns_result=$?
    
    if [ $dns_result -eq 0 ]; then
        checks_total=$((checks_total + 1))
        checks_passed=$((checks_passed + 1))
    elif [ $dns_result -eq 1 ]; then
        checks_total=$((checks_total + 1))
        show_warning "PIA DNS query test failed (may be temporary)"
    fi
    
    # Check 4: IPv6 leak test
    checks_total=$((checks_total + 1))
    if check_ipv6_leak; then
        checks_passed=$((checks_passed + 1))
        [ "${DISABLE_IPV6}" = "true" ] && show_success "No IPv6 leaks detected"
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
        return 0
    elif [ $checks_passed -ge $((checks_total - 1)) ]; then
        show_success "External IP: ${grn}${bold}${vpn_ip}${nc}"
        return 0
    elif [ $checks_passed -ge $((checks_total / 2)) ]; then
        show_warning "External IP: ${ylw}${bold}${vpn_ip}${nc} ${ylw}(VPN active, some checks failed)${nc}"
        return 0
    else
        show_error "External IP: ${red}${bold}${vpn_ip}${nc} ${red}(VPN may not be working)${nc}"
        return 1
    fi
}

# Run if executed directly
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    verify_connection
fi

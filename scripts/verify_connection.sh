#!/bin/bash

source /app/scripts/ui.sh

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

# Get the current DNS server from resolv.conf
get_current_dns() {
    local dns=$(grep "^nameserver" /etc/resolv.conf | head -1 | awk '{print $2}')
    
    # Check if it's PIA DNS
    if [[ "$dns" == "10.0.0.243" || "$dns" == "10.0.0.242" ]]; then
        echo "PIA ($dns)"
    elif [[ "$dns" == "209.222.18.222" || "$dns" == "209.222.18.218" ]]; then
        echo "PIA ($dns)"
    else
        echo "$dns"
    fi
}

# OPTIMIZED: Consolidated DNS validation function
validate_dns() {
    local leak_detected=false
    local pia_dns_found=false
    
    # Early exit optimization: Check each nameserver
    while read -r _ dns; do
        # Check if it's PIA DNS
        if [[ "$dns" =~ ^(209\.222\.18\.(222|218)|10\.0\.0\.(243|242))$ ]]; then
            pia_dns_found=true
            continue
        fi
        
        # Skip private/local DNS (early exit on match)
        [[ "$dns" =~ ^(10\.|172\.(1[6-9]|2[0-9]|3[0-1])\.|192\.168\.|127\.) ]] && continue
        
        # Public non-PIA DNS found = potential leak (unless custom DNS configured)
        if [[ -z "$DNS" || "$DNS" = "pia" ]]; then
            show_warning "Unexpected DNS server detected: $dns"
            leak_detected=true
            break  # Early exit on first leak
        fi
    done < <(grep "^nameserver" /etc/resolv.conf)
    
    # Return results
    if $leak_detected; then
        return 2  # DNS leak detected
    elif $pia_dns_found; then
        return 0  # PIA DNS working
    else
        return 1  # DNS not verified
    fi
}

# OPTIMIZED: Check IPv6 with early exit
check_ipv6_leak() {
    [ "${DISABLE_IPV6}" != "true" ] && return 0  # Early exit if IPv6 is allowed
    
    local ipv6=$(timeout 5 curl -6 -s --interface pia https://api6.ipify.org 2>/dev/null)
    
    if [ -n "$ipv6" ]; then
        show_error "IPv6 leak detected: $ipv6"
        return 1
    fi
    
    return 0
}

verify_connection() {
    local checks_passed=0
    local checks_total=0
    local vpn_ip=""
    
    # Check 1: Get external IP (most important check)
    checks_total=$((checks_total + 1))
    vpn_ip=$(get_external_ip)
    
    if [ -z "$vpn_ip" ]; then
        # OPTIMIZED: Early exit path for timeout
        show_warning "Could not verify external IP (check manually if needed)"
        echo "  ${ylw}ℹ${nc} VPN is connected, but IP verification timed out"
        return 0
    fi
    
    checks_passed=$((checks_passed + 1))
    
    # Check 2: Verify IP changed (if we have the real IP)
    if [ -f /tmp/real_ip ]; then
        checks_total=$((checks_total + 1))
        local real_ip=$(cat /tmp/real_ip)
        
        if [ "$vpn_ip" != "$real_ip" ]; then
            checks_passed=$((checks_passed + 1))
        else
            show_warning "VPN IP matches real IP - VPN may not be working!"
        fi
    fi
    
    # Check 3: Validate DNS (consolidated function)
    checks_total=$((checks_total + 1))
    validate_dns
    local dns_result=$?
    
    if [ $dns_result -eq 0 ]; then
        # PIA DNS working properly
        checks_passed=$((checks_passed + 1))
        local current_dns=$(get_current_dns)
        show_success "DNS: ${current_dns}"
    elif [ $dns_result -eq 1 ]; then
        # DNS not verified but not leaked
        checks_passed=$((checks_passed + 1))
        local current_dns=$(get_current_dns)
        show_success "DNS: ${current_dns}"
    fi
    # If dns_result == 2, warning already shown by validate_dns()
    
    # Check 4: IPv6 leak (with early exit in function)
    checks_total=$((checks_total + 1))
    if check_ipv6_leak; then
        checks_passed=$((checks_passed + 1))
        [ "${DISABLE_IPV6}" = "true" ] && show_success "No IPv6 leaks detected"
    fi
    
    # OPTIMIZED: Display results with early success paths
    # Most common case: All checks passed
    if [ $checks_passed -eq $checks_total ]; then
        show_success "External IP: ${grn}${bold}${vpn_ip}${nc}"
        return 0
    fi
    
    # Second most common: Nearly all passed
    if [ $checks_passed -ge $((checks_total - 1)) ]; then
        show_success "External IP: ${grn}${bold}${vpn_ip}${nc}"
        return 0
    fi
    
    # Partial success
    if [ $checks_passed -ge $((checks_total / 2)) ]; then
        show_warning "External IP: ${ylw}${bold}${vpn_ip}${nc} ${ylw}(VPN active, some checks failed)${nc}"
        return 0
    fi
    
    # Failure case
    show_error "External IP: ${red}${bold}${vpn_ip}${nc} ${red}(VPN may not be working)${nc}"
    return 1
}

# Only run if executed directly (not sourced)
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    verify_connection
fi

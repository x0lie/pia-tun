#!/bin/bash

source /app/scripts/ui.sh
source /app/scripts/killswitch.sh

capture_real_ip() {
    show_step "Capturing pre-VPN IP address..."
    show_debug "Attempting to fetch real IP from api.ipify.org (timeout: 5s)"
    
    # Resolve ipify.org
    show_debug "Resolving ipify.org"
    local ipify_ip=$(resolve_hostname "api.ipify.org")
    if [ -z "$ipify_ip" ]; then
        show_error "Cannot resolve ipify.org"
        return 1
    fi

    add_temporary_exemption "$ipify_ip" "443" "tcp" "ipify"

    show_debug "retrieving real IP"
    local real_ip=$(curl -s --connect-to "api.ipify.org::$ipify_ip:" \
        'https://api.ipify.org' | head -1)
    show_debug "real IP retrieved: $real_ip"

    remove_temporary_exemption "ipify"
    
    if [ -n "$real_ip" ]; then
        echo "$real_ip" > /tmp/real_ip
        show_success "Real IP captured: $real_ip"
    else
        show_warning "Could not capture real IP (verification will be limited)"
        show_debug "Failed to retrieve real IP (timeout or network error)"
    fi
}

# Get the current DNS server from resolv.conf
get_current_dns() {
    local dns=$(grep "^nameserver" /etc/resolv.conf | head -1 | awk '{print $2}')
    show_debug "Primary DNS from resolv.conf: ${dns:-none}"
    
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
    local nameserver_count=0
    while read -r _ dns; do
        nameserver_count=$((nameserver_count + 1))
        show_debug "Checking nameserver #$nameserver_count: $dns"
        
        # Check if it's PIA DNS
        if [[ "$dns" =~ ^(209\.222\.18\.(222|218)|10\.0\.0\.(243|242))$ ]]; then
            show_debug "  -> PIA DNS detected: $dns"
            pia_dns_found=true
            continue
        fi
        
        # Skip private/local DNS (early exit on match)
        if [[ "$dns" =~ ^(10\.|172\.(1[6-9]|2[0-9]|3[0-1])\.|192\.168\.|127\.) ]]; then
            show_debug "  -> Private/local DNS (allowed): $dns"
            continue
        fi
        
        # Public non-PIA DNS found = potential leak (unless custom DNS configured)
        if [[ -z "$DNS" || "$DNS" = "pia" ]]; then
            show_warning "Unexpected DNS server detected: $dns"
            show_debug "  -> Public non-PIA DNS leak detected: $dns"
            leak_detected=true
            break  # Early exit on first leak
        else
            show_debug "  -> Custom DNS (expected): $dns"
        fi
    done < <(grep "^nameserver" /etc/resolv.conf)
    
    show_debug "DNS validation complete: nameservers=$nameserver_count, pia_found=$pia_dns_found, leak=$leak_detected"
    
    # Return results
    if $leak_detected; then
        show_debug "Returning 2 (DNS leak detected)"
        return 2  # DNS leak detected
    elif $pia_dns_found; then
        show_debug "Returning 0 (PIA DNS working)"
        return 0  # PIA DNS working
    else
        show_debug "Returning 1 (DNS not verified but no leak)"
        return 1  # DNS not verified
    fi
}

# OPTIMIZED: Check IPv6 with early exit
check_ipv6_leak() {
    show_debug "Checking for IPv6 leaks (IPV6_ENABLED=${IPV6_ENABLED})"
    
    if [ "${IPV6_ENABLED}" != "false" ]; then
        show_debug "IPv6 is allowed, skipping leak check"
        return 0  # Early exit if IPv6 is allowed
    fi
    
    show_debug "Attempting IPv6 connection test via api6.ipify.org (timeout: 5s)"
    local ipv6=$(timeout 5 curl -6 -s --interface pia0 https://api6.ipify.org 2>/dev/null)
    
    if [ -n "$ipv6" ]; then
        show_error "IPv6 leak detected: $ipv6"
        show_debug "IPv6 leak found despite IPV6_ENABLED=false"
        return 1
    fi
    
    show_debug "No IPv6 leak detected (good)"
    return 0
}

verify_connection() {
    local checks_passed=0
    local checks_total=0
    local vpn_ip=""
    
    show_debug "Starting connection verification"
    show_debug "Configuration: DNS=${DNS:-pia}, IPV6_ENABLED=${IPV6_ENABLED}"
    
    # Check 1: Get external IP (most important check)
    checks_total=$((checks_total + 1))
    show_debug "Check 1/$checks_total: Fetching external IP via VPN"
    vpn_ip=$(get_external_ip)
    
    if [ -z "$vpn_ip" ]; then
        # OPTIMIZED: Early exit path for timeout
        show_warning "Could not verify external IP (DNS misconfigured?)\n      DNS: $DNS"
        echo "  ${ylw}ℹ${nc} VPN is connected, but IP verification timed out"
        show_debug "External IP check failed (timeout), exiting verification early"
        show_debug "Final score: $checks_passed/$checks_total checks passed"
        return 1
    fi
    
    show_debug "External IP retrieved: $vpn_ip"
    checks_passed=$((checks_passed + 1))
    
    # Check 2: Verify IP changed (if we have the real IP)
    if [ -f /tmp/real_ip ]; then
        checks_total=$((checks_total + 1))
        local real_ip=$(cat /tmp/real_ip)
        show_debug "Check 2/$checks_total: Comparing VPN IP to real IP"
        show_debug "  Real IP: $real_ip"
        show_debug "  VPN IP:  $vpn_ip"
        
        if [ "$vpn_ip" != "$real_ip" ]; then
            show_debug "IPs differ - VPN is working correctly"
            checks_passed=$((checks_passed + 1))
        else
            show_warning "VPN IP matches real IP - VPN may not be working!"
            show_debug "WARNING: IPs match - potential VPN failure"
        fi
    else
        show_debug "Skipping IP comparison (no real IP captured)"
    fi
    
    # Check 3: Validate DNS (consolidated function)
    checks_total=$((checks_total + 1))
    show_debug "Check 3/$checks_total: Validating DNS configuration"
    validate_dns
    local dns_result=$?
    
    show_debug "DNS validation result code: $dns_result"
    
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
    show_debug "Check 4/$checks_total: Checking for IPv6 leaks"
    
    if check_ipv6_leak; then
        checks_passed=$((checks_passed + 1))
        [ "${IPV6_ENABLED}" = "false" ] && show_success "No IPv6 leaks detected"
    else
        show_debug "IPv6 leak check failed"
    fi
    
    show_debug "Verification complete: $checks_passed/$checks_total checks passed"
    
    # OPTIMIZED: Display results with early success paths
    # Most common case: All checks passed
    if [ $checks_passed -eq $checks_total ]; then
        show_debug "All checks passed (perfect score)"
        show_success "External IP: ${grn}${bold}${vpn_ip}${nc}"
        return 0
    fi
    
    # Second most common: Nearly all passed
    if [ $checks_passed -ge $((checks_total - 1)) ]; then
        show_debug "Nearly all checks passed ($checks_passed/$checks_total)"
        show_success "External IP: ${grn}${bold}${vpn_ip}${nc}"
        return 0
    fi
    
    # Partial success
    if [ $checks_passed -ge $((checks_total / 2)) ]; then
        show_debug "Partial success ($checks_passed/$checks_total)"
        show_warning "External IP: ${ylw}${bold}${vpn_ip}${nc} ${ylw}(VPN active, some checks failed)${nc}"
        return 0
    fi
    
    # Failure case
    show_debug "Verification failed ($checks_passed/$checks_total)"
    show_error "External IP: ${red}${bold}${vpn_ip}${nc} ${red}(VPN may not be working)${nc}"
    return 1
}

# Only run if executed directly (not sourced)
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    verify_connection
fi
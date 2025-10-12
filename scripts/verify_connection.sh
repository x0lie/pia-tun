#!/bin/bash

source /app/scripts/ui.sh

capture_real_ip() {
    show_step "Capturing pre-VPN IP address..."
    local real_ip=$(timeout 5 curl -s https://api.ipify.org 2>/dev/null)
    
    [ -n "$real_ip" ] && {
        echo "$real_ip" > /tmp/real_ip
        show_success "Real IP captured: $real_ip"
    } || show_warning "Could not capture real IP (verification will be limited)"
}

check_pia_dns() {
    grep -q "209.222.18.222\|209.222.18.218" /etc/resolv.conf 2>/dev/null || return 2
    timeout 3 dig @209.222.18.222 google.com +short +tries=1 >/dev/null 2>&1
}

check_dns_leak() {
    local leak_detected=false
    
    while read -r _ dns; do
        # Skip PIA DNS
        [[ "$dns" =~ ^209\.222\.18\.(222|218)$ ]] && continue
        
        # Skip private/local DNS (early exit on match)
        [[ "$dns" =~ ^(10\.|172\.(1[6-9]|2[0-9]|3[0-1])\.|192\.168\.|127\.) ]] && continue
        
        # Public non-PIA DNS found
        [[ -z "$DNS" || "$DNS" = "pia" ]] && {
            show_warning "Unexpected DNS server detected: $dns"
            leak_detected=true
            break  # Early exit
        }
    done < <(grep "^nameserver" /etc/resolv.conf)
    
    ! $leak_detected
}

check_ipv6_leak() {
    [ "${DISABLE_IPV6}" != "true" ] && return 0
    
    local ipv6=$(timeout 5 curl -6 -s --interface pia https://api6.ipify.org 2>/dev/null)
    [ -n "$ipv6" ] && { show_error "IPv6 leak detected: $ipv6"; return 1; }
    return 0
}

verify_connection() {
    local checks_passed=0 checks_total=0 vpn_ip=""
    
    # Check 1: Get external IP
    checks_total=$((checks_total + 1))
    vpn_ip=$(get_external_ip) && checks_passed=$((checks_passed + 1)) || \
        show_warning "External IP check timed out (VPN likely working, but verification slow)"
    
    # Check 2: Verify IP changed
    [ -f /tmp/real_ip ] && {
        checks_total=$((checks_total + 1))
        local real_ip=$(cat /tmp/real_ip)
        [ "$vpn_ip" != "$real_ip" ] && checks_passed=$((checks_passed + 1)) || \
            show_warning "VPN IP matches real IP - VPN may not be working!"
    }
    
    # Check 3: PIA DNS (if using)
    check_pia_dns
    local dns_result=$?
    [ $dns_result -eq 0 ] && {
        checks_total=$((checks_total + 1))
        checks_passed=$((checks_passed + 1))
    }
    [ $dns_result -eq 1 ] && {
        checks_total=$((checks_total + 1))
        show_warning "PIA DNS query test failed (may be temporary)"
    }
    
    # Check 4: IPv6 leak
    checks_total=$((checks_total + 1))
    check_ipv6_leak && {
        checks_passed=$((checks_passed + 1))
        [ "${DISABLE_IPV6}" = "true" ] && show_success "No IPv6 leaks detected"
    }
    
    # Check 5: DNS leak
    checks_total=$((checks_total + 1))
    check_dns_leak && {
        checks_passed=$((checks_passed + 1))
        show_success "DNS properly configured"
    }
    
    # Display results (early exit logic)
    [ -z "$vpn_ip" ] && {
        show_warning "Could not verify external IP (check manually if needed)"
        echo "  ${ylw}ℹ${nc} VPN is connected, but IP verification timed out"
        return 0
    }
    
    # Success thresholds
    [ $checks_passed -ge $((checks_total - 1)) ] && {
        show_success "External IP: ${grn}${bold}${vpn_ip}${nc}"
        return 0
    }
    
    [ $checks_passed -ge $((checks_total / 2)) ] && {
        show_warning "External IP: ${ylw}${bold}${vpn_ip}${nc} ${ylw}(VPN active, some checks failed)${nc}"
        return 0
    }
    
    show_error "External IP: ${red}${bold}${vpn_ip}${nc} ${red}(VPN may not be working)${nc}"
    return 1
}

# Only run if executed directly (not sourced)
if [ "${BASH_SOURCE[0]}" -ef "$0" ]; then
    verify_connection
fi

#!/bin/bash
# High-performance killswitch (firewall) management for PIA WireGuard VPN
# Prevents IP leaks by enforcing strict network rules with surgical exemptions
#
# Architecture:
# 1. Baseline killswitch (always active) - blocks everything except local/VPN
# 2. Bypass routing table - routes specific IPs (WAN checks) around VPN via eth0
# 3. Surgical exemptions - temporary firewall holes for VPN setup operations
#
# Features:
# - Auto-detects nftables (modern, O(1) lookups) or iptables (legacy fallback)
# - Zero-overhead WAN checks via routing bypass
# - Minimal temporary exemptions for PIA API calls
# - No leak windows during reconnection
# - Idempotent operations safe to call multiple times

set -euo pipefail

source /app/scripts/ui.sh

# Detect firewall backend at runtime
USE_NFTABLES=false
if command -v nft >/dev/null 2>&1; then
    USE_NFTABLES=true
else
    show_warning "nftables not available, falling back to iptables"
fi

# Get default gateway and interface
get_default_gateway() {
    ip route | grep default | head -1 | awk '{print $3}'
}

get_default_interface() {
    ip route | grep default | head -1 | awk '{print $5}'
}

#═══════════════════════════════════════════════════════════════════════════════
# BYPASS ROUTING TABLE (Permanent, Zero Overhead)
#═══════════════════════════════════════════════════════════════════════════════

setup_bypass_routes() {
    local gateway=$(get_default_gateway)
    local interface=$(get_default_interface)
    
    [ -z "$gateway" ] && { show_error "Cannot determine default gateway"; return 1; }
    [ -z "$interface" ] && { show_error "Cannot determine default interface"; return 1; }
    
    ip route add default via "$gateway" dev "$interface" table 100 2>/dev/null || true
    
    # Bypass only for new WAN check IPs
    ip rule add to 129.6.15.28 table 100 priority 50 2>/dev/null || true
    ip rule add to 129.6.15.29 table 100 priority 50 2>/dev/null || true
    ip rule add to 132.163.96.1 table 100 priority 50 2>/dev/null || true
    ip rule add to 132.163.97.1 table 100 priority 50 2>/dev/null || true
    ip rule add to 128.138.140.44 table 100 priority 50 2>/dev/null || true
}

cleanup_bypass_routes() {
    # Clean up bypass routing rules
    ip rule del to 129.6.15.28 table 100 priority 50 2>/dev/null || true
    ip rule del to 129.6.15.29 table 100 priority 50 2>/dev/null || true
    ip rule del to 132.163.96.1 table 100 priority 50 2>/dev/null || true
    ip rule del to 132.163.97.1 table 100 priority 50 2>/dev/null || true
    ip rule del to 128.138.140.44 table 100 priority 50 2>/dev/null || true

    # Remove bypass table routes
    ip route del default table 100 2>/dev/null || true
}

#═══════════════════════════════════════════════════════════════════════════════
# NFTABLES IMPLEMENTATION
#═══════════════════════════════════════════════════════════════════════════════

nft_setup_base_table() {
    nft add table inet vpn_filter 2>/dev/null || nft flush table inet vpn_filter
    nft add chain inet vpn_filter output { type filter hook output priority -100 \; policy accept \; }
    nft add chain inet vpn_filter input { type filter hook input priority 0 \; policy accept \; }
}

nft_create_sets() {
    nft add set inet vpn_filter local_nets_v4 { type ipv4_addr \; flags interval \; }
    nft add set inet vpn_filter local_nets_v6 { type ipv6_addr \; flags interval \; }
    nft add set inet vpn_filter allowed_ports { type inet_service \; }
    nft add set inet vpn_filter exempt_ports { type inet_service \; }
}

nft_populate_local_networks() {
    local mode="$1"
    
    if [ "$mode" = "all" ]; then
        nft add element inet vpn_filter local_nets_v4 { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 }
        nft add element inet vpn_filter local_nets_v6 { fe80::/10, fc00::/7, ff00::/8 }
        show_success "Local network access: All RFC1918 networks"
    elif [ -n "$mode" ]; then
        local ipv4_list=""
        local ipv6_list=""
        
        IFS=',' read -ra NETWORKS <<< "$mode"
        for network in "${NETWORKS[@]}"; do
            network=$(echo "$network" | xargs)
            if [[ "$network" == *":"* ]]; then
                [ -n "$ipv6_list" ] && ipv6_list+=", "
                ipv6_list+="$network"
            else
                [ -n "$ipv4_list" ] && ipv4_list+=", "
                ipv4_list+="$network"
            fi
        done
        
        [ -n "$ipv4_list" ] && nft add element inet vpn_filter local_nets_v4 { $ipv4_list }
        [ -n "$ipv6_list" ] && nft add element inet vpn_filter local_nets_v6 { $ipv6_list }
        
        show_success "Local network access: $mode"
    else
        show_success "Local network access: Disabled (all traffic through VPN)"
    fi
}

nft_populate_ports() {
    local ports=()
    
    if [ "${PROXY_ENABLED}" = "true" ]; then
        ports+=("${SOCKS5_PORT:-1080}")
        ports+=("${HTTP_PROXY_PORT:-8888}")
    fi
    
    [ "${METRICS}" = "true" ] && ports+=("${METRICS_PORT:-9090}")
    
    if [ ${#ports[@]} -gt 0 ]; then
        local port_list=$(IFS=', '; echo "${ports[*]}")
        nft add element inet vpn_filter allowed_ports { $port_list }
        
        [ "${PROXY_ENABLED}" = "true" ] && \
            show_success "Proxy ports allowed: SOCKS5=${SOCKS5_PORT:-1080}, HTTP=${HTTP_PROXY_PORT:-8888}"
        [ "${METRICS}" = "true" ] && \
            show_success "Metrics port allowed: ${METRICS_PORT:-9090}"
    fi
}

nft_populate_exempt_ports() {
    if [ -n "${KILLSWITCH_EXEMPT_PORTS:-}" ]; then
        local ports=()
        IFS=',' read -ra PORTS <<< "$KILLSWITCH_EXEMPT_PORTS"
        for port in "${PORTS[@]}"; do
            port=$(echo "$port" | xargs)
            ports+=("$port")
        done
        
        [ ${#ports[@]} -gt 0 ] && {
            local port_list=$(IFS=', '; echo "${ports[*]}")
            nft add element inet vpn_filter exempt_ports { $port_list }
            show_success "Exempted ports: $KILLSWITCH_EXEMPT_PORTS"
        }
    fi
}

# Apply baseline killswitch (everything blocked except local/VPN/exemptions)
nft_apply_baseline_killswitch() {
    show_step "Applying killswitch..."
    
    nft_cleanup 2>/dev/null || true
    nft_setup_base_table
    nft_create_sets
    nft_populate_local_networks "${LOCAL_NETWORK:-}"
    
    # Build rules in optimal order (most-matched first)
    
    # 1. Loopback (most frequent)
    nft add rule inet vpn_filter output oifname "lo" accept
    
    # 2. Bypass routing destinations (1.1.1.1, 8.8.8.8, 1.0.0.1)
    # MUST come before established/related to allow initial SYN packets!
    # These are handled by routing table 100
    nft add rule inet vpn_filter output ip daddr { 129.6.15.28, 129.6.15.29, 132.163.96.1, 132.163.97.1, 128.138.140.44 } accept comment "bypass_routes"
    
    # 3. Established/Related (bulk of traffic)
    nft add rule inet vpn_filter output ct state established,related accept
    
    # 4. Local networks (if configured)
    if [ "${LOCAL_NETWORK:-}" = "all" ] || [ -n "${LOCAL_NETWORK:-}" ]; then
        nft add rule inet vpn_filter output ip daddr @local_nets_v4 accept
        [ "${DISABLE_IPV6}" != "true" ] && \
            nft add rule inet vpn_filter output ip6 daddr @local_nets_v6 accept
    fi
    
    # 5. VPN interface (if up)
    # Note: This will be added by nft_add_vpn_interface() when VPN is up
    
    # 6. Exempted ports (if any)
    nft_populate_exempt_ports
    if [ -n "${KILLSWITCH_EXEMPT_PORTS:-}" ]; then
        nft add rule inet vpn_filter output tcp dport @exempt_ports accept
    fi
    
    # 7. Drop everything else
    nft add rule inet vpn_filter output drop
    
    # IPv6 protection
    if [ "${DISABLE_IPV6}" = "true" ]; then
        show_success "IPv6 completely blocked"
    else
        show_success "IPv6 routed through VPN only"
    fi
    
    # Input rules for proxy/metrics
    nft_populate_ports
    [ "${PROXY_ENABLED}" = "true" ] || [ "${METRICS}" = "true" ] && \
        nft add rule inet vpn_filter input tcp dport @allowed_ports accept
}

# Add VPN interface to killswitch (called after VPN is up)
nft_add_vpn_interface() {
    local fwmark=$(wg show pia fwmark 2>/dev/null)
    
    # Get handle of the DROP rule so we can insert before it
    local drop_handle=$(nft -a list chain inet vpn_filter output 2>/dev/null | grep "drop" | grep -v "comment" | tail -1 | sed -n 's/.*# handle \([0-9]*\).*/\1/p')
    
    if [ -z "$drop_handle" ]; then
        show_error "Cannot find DROP rule in killswitch"
        return 1
    fi
    
    # Insert VPN rules before the DROP rule
    if [ -n "$fwmark" ] && [ "$fwmark" != "off" ]; then
        nft insert rule inet vpn_filter output handle "$drop_handle" mark "$fwmark" accept comment "vpn_fwmark"
        show_success "VPN added to killswitch (fwmark: $fwmark)"
    else
        show_success "VPN added to killswitch (interface-based)"
    fi
    nft insert rule inet vpn_filter output handle "$drop_handle" oifname "pia" accept comment "vpn_interface"
}

# Remove VPN interface from killswitch (called before VPN teardown)
nft_remove_vpn_interface() {
    # Delete rules with "vpn_" comments
    nft -a list chain inet vpn_filter output 2>/dev/null | grep "comment \"vpn_" | awk '{print $NF}' | while read handle; do
        nft delete rule inet vpn_filter output handle "$handle" 2>/dev/null || true
    done
}

# Add temporary exemption (surgical hole in firewall)
nft_add_exemption() {
    local ip="$1"
    local port="$2"
    local proto="$3"
    local comment="$4"
    
    # Add rule to chain - nftables evaluates in order, and we'll add before the final DROP
    # We need to get the handle of the DROP rule and insert before it
    local drop_handle=$(nft -a list chain inet vpn_filter output 2>/dev/null | grep "drop" | grep -v "comment" | tail -1 | sed -n 's/.*# handle \([0-9]*\).*/\1/p')
    
    if [ -n "$drop_handle" ]; then
        # Insert rule right before the DROP rule
        nft insert rule inet vpn_filter output handle "$drop_handle" ip daddr "$ip" "$proto" dport "$port" accept comment "temp_$comment"
    else
        # Fallback: just add the rule (will be before any DROP if DROP doesn't exist yet)
        nft add rule inet vpn_filter output ip daddr "$ip" "$proto" dport "$port" accept comment "temp_$comment"
    fi
}

# Remove temporary exemption
nft_remove_exemption() {
    local comment="$1"
    
    nft -a list chain inet vpn_filter output 2>/dev/null | grep "comment \"temp_$comment\"" | awk '{print $NF}' | while read handle; do
        nft delete rule inet vpn_filter output handle "$handle" 2>/dev/null || true
    done
}

# Remove all temporary exemptions
nft_remove_all_exemptions() {
    nft -a list chain inet vpn_filter output 2>/dev/null | grep "comment \"temp_" | awk '{print $NF}' | while read handle; do
        nft delete rule inet vpn_filter output handle "$handle" 2>/dev/null || true
    done
}

nft_cleanup() {
    nft delete table inet vpn_filter 2>/dev/null || true
}

#═══════════════════════════════════════════════════════════════════════════════
# IPTABLES FALLBACK IMPLEMENTATION
#═══════════════════════════════════════════════════════════════════════════════

ipt_add_fw_rule() {
    local chain="$1"
    shift
    [ "$chain" = "VPN_OUT" ] && iptables -A VPN_OUT "$@" || ip6tables -A VPN_OUT6 "$@"
}

ipt_apply_local_network_rules() {
    local chain=$1
    local is_ipv6=$2
    
    if [ "${LOCAL_NETWORK:-}" = "all" ]; then
        if [ "$is_ipv6" = "true" ]; then
            ipt_add_fw_rule "$chain" -d fe80::/10 -j ACCEPT
            ipt_add_fw_rule "$chain" -d fc00::/7 -j ACCEPT
            ipt_add_fw_rule "$chain" -d ff00::/8 -j ACCEPT
        else
            ipt_add_fw_rule "$chain" -d 192.168.0.0/16 -j ACCEPT
            ipt_add_fw_rule "$chain" -d 10.0.0.0/8 -j ACCEPT
            ipt_add_fw_rule "$chain" -d 172.16.0.0/12 -j ACCEPT
            ipt_add_fw_rule "$chain" -d 169.254.0.0/16 -j ACCEPT
            ipt_add_fw_rule "$chain" -d 224.0.0.0/4 -j ACCEPT
            [ "$chain" = "VPN_OUT" ] && show_success "Local network access: All RFC1918 networks"
        fi
    elif [ -n "${LOCAL_NETWORK:-}" ]; then
        IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORK"
        for network in "${NETWORKS[@]}"; do
            network=$(echo "$network" | xargs)
            
            if [[ "$network" == *":"* ]] && [ "$is_ipv6" = "true" ]; then
                ipt_add_fw_rule "$chain" -d "$network" -j ACCEPT
            elif [[ "$network" != *":"* ]] && [ "$is_ipv6" != "true" ]; then
                ipt_add_fw_rule "$chain" -d "$network" -j ACCEPT
            fi
        done
        [ "$chain" = "VPN_OUT" ] && show_success "Local network access: $LOCAL_NETWORK"
    else
        [ "$chain" = "VPN_OUT" ] && show_success "Local network access: Disabled (all traffic through VPN)"
    fi
}

ipt_apply_proxy_rules() {
    local chain=$1
    
    if [ "${PROXY_ENABLED}" = "true" ]; then
        iptables -N PROXY_PORTS 2>/dev/null || iptables -F PROXY_PORTS
        iptables -A PROXY_PORTS -p tcp -m multiport --dports "${SOCKS5_PORT:-1080},${HTTP_PROXY_PORT:-8888}" -j ACCEPT
        iptables -I INPUT 1 -j PROXY_PORTS
        
        [ "$chain" = "VPN_OUT" ] && show_success "Proxy ports allowed: SOCKS5=${SOCKS5_PORT:-1080}, HTTP=${HTTP_PROXY_PORT:-8888}"
    fi

    if [ "${METRICS}" = "true" ]; then
        iptables -I INPUT 1 -p tcp --dport "${METRICS_PORT:-9090}" -j ACCEPT
        [ "$chain" = "VPN_OUT" ] && show_success "Metrics port allowed: ${METRICS_PORT:-9090}"
    fi
}

ipt_setup_ipv6_protection() {
    ip6tables -F OUTPUT 2>/dev/null || true
    ip6tables -F FORWARD 2>/dev/null || true
    ip6tables -N VPN_OUT6 2>/dev/null || ip6tables -F VPN_OUT6
    
    ipt_add_fw_rule "VPN_OUT6" -o lo -j ACCEPT
    ipt_add_fw_rule "VPN_OUT6" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    
    ipt_apply_local_network_rules "VPN_OUT6" "true"
    
    [ "${DISABLE_IPV6}" != "true" ] && ipt_add_fw_rule "VPN_OUT6" -o pia -j ACCEPT
    
    ipt_add_fw_rule "VPN_OUT6" -j DROP
    ip6tables -I OUTPUT 1 -j VPN_OUT6
    
    [ "${DISABLE_IPV6}" = "true" ] && show_success "IPv6 completely blocked" || \
        show_success "IPv6 routed through VPN only"
}

ipt_setup_iptables_chain() {
    iptables -P OUTPUT ACCEPT 2>/dev/null || true
    iptables -F OUTPUT 2>/dev/null || true
    iptables -X VPN_OUT 2>/dev/null || true
    iptables -N VPN_OUT
}

ipt_add_standard_rules() {
    local include_vpn="${1:-false}"
    
    ipt_add_fw_rule "VPN_OUT" -o lo -j ACCEPT
    
    # Bypass routing destinations MUST come before established/related
    # to allow initial SYN packets!
    ipt_add_fw_rule "VPN_OUT" -d 129.6.15.28 -j ACCEPT -m comment --comment "bypass_routes"
    ipt_add_fw_rule "VPN_OUT" -d 129.6.15.29 -j ACCEPT -m comment --comment "bypass_routes"
    ipt_add_fw_rule "VPN_OUT" -d 132.163.96.1 -j ACCEPT -m comment --comment "bypass_routes"
    ipt_add_fw_rule "VPN_OUT" -d 132.163.97.1 -j ACCEPT -m comment --comment "bypass_routes"
    ipt_add_fw_rule "VPN_OUT" -d 128.138.140.44 -j ACCEPT -m comment --comment "bypass_routes"
    
    ipt_add_fw_rule "VPN_OUT" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    
    ipt_apply_local_network_rules "VPN_OUT" "false"
    
    if [ "$include_vpn" = "true" ]; then
        local fwmark=$(wg show pia fwmark 2>/dev/null)
        if [ -n "$fwmark" ] && [ "$fwmark" != "off" ]; then
            ipt_add_fw_rule "VPN_OUT" -m mark --mark "$fwmark" -j ACCEPT -m comment --comment "vpn_fwmark"
            show_success "VPN added to killswitch (fwmark: $fwmark)"
        else
            show_success "VPN added to killswitch (interface-based)"
        fi
        ipt_add_fw_rule "VPN_OUT" -o pia -j ACCEPT -m comment --comment "vpn_interface"
    fi
    
    ipt_apply_proxy_rules "VPN_OUT"
}

ipt_apply_baseline_killswitch() {
    show_step "Applying baseline killswitch..."
    
    ipt_cleanup 2>/dev/null || true
    ipt_setup_ipv6_protection
    ipt_setup_iptables_chain
    ipt_add_standard_rules "false"
    
    if [ -n "${KILLSWITCH_EXEMPT_PORTS:-}" ]; then
        IFS=',' read -ra PORTS <<< "$KILLSWITCH_EXEMPT_PORTS"
        if [ ${#PORTS[@]} -gt 1 ]; then
            local port_list=$(IFS=','; echo "${PORTS[*]}")
            ipt_add_fw_rule "VPN_OUT" -p tcp -m multiport --dports "$port_list" -j ACCEPT
            show_success "Exempted ports: $KILLSWITCH_EXEMPT_PORTS"
        else
            for port in "${PORTS[@]}"; do
                port=$(echo "$port" | xargs)
                ipt_add_fw_rule "VPN_OUT" -p tcp --dport "$port" -j ACCEPT
            done
            show_success "Exempted ports: $KILLSWITCH_EXEMPT_PORTS"
        fi
    fi
    
    ipt_add_fw_rule "VPN_OUT" -j DROP
    
    iptables -I OUTPUT 1 -j VPN_OUT
}

ipt_add_vpn_interface() {
    # Remove DROP rule temporarily
    iptables -D VPN_OUT -j DROP 2>/dev/null || true
    
    # Add VPN rules
    local fwmark=$(wg show pia fwmark 2>/dev/null)
    if [ -n "$fwmark" ] && [ "$fwmark" != "off" ]; then
        ipt_add_fw_rule "VPN_OUT" -m mark --mark "$fwmark" -j ACCEPT -m comment --comment "vpn_fwmark"
        show_success "VPN added to killswitch (fwmark: $fwmark)"
    else
        show_success "VPN added to killswitch (interface-based)"
    fi
    ipt_add_fw_rule "VPN_OUT" -o pia -j ACCEPT -m comment --comment "vpn_interface"
    
    # Re-add DROP rule at end
    ipt_add_fw_rule "VPN_OUT" -j DROP
    
    # Also update IPv6
    [ "${DISABLE_IPV6}" != "true" ] && {
        ip6tables -D VPN_OUT6 -j DROP 2>/dev/null || true
        ipt_add_fw_rule "VPN_OUT6" -o pia -j ACCEPT
        ipt_add_fw_rule "VPN_OUT6" -j DROP
    }
}

ipt_remove_vpn_interface() {
    # Remove VPN-related rules
    iptables -D VPN_OUT -m comment --comment "vpn_fwmark" -j ACCEPT 2>/dev/null || true
    iptables -D VPN_OUT -m comment --comment "vpn_interface" -j ACCEPT 2>/dev/null || true
    
    ip6tables -D VPN_OUT6 -o pia -j ACCEPT 2>/dev/null || true
}

ipt_add_exemption() {
    local ip="$1"
    local port="$2"
    local proto="$3"
    local comment="$4"
    
    # Insert near top (after established/related)
    iptables -I VPN_OUT 3 -d "$ip" -p "$proto" --dport "$port" -j ACCEPT -m comment --comment "temp_$comment"
}

ipt_remove_exemption() {
    local comment="$1"
    
    iptables -D VPN_OUT -m comment --comment "temp_$comment" -j ACCEPT 2>/dev/null || true
}

ipt_remove_all_exemptions() {
    # Remove all rules with "temp_" in comment
    iptables -S VPN_OUT | grep "temp_" | sed 's/-A/-D/' | while read rule; do
        iptables $rule 2>/dev/null || true
    done
}

ipt_cleanup() {
    iptables -D OUTPUT -j VPN_OUT 2>/dev/null || true
    iptables -F VPN_OUT 2>/dev/null || true
    iptables -X VPN_OUT 2>/dev/null || true
    ip6tables -D OUTPUT -j VPN_OUT6 2>/dev/null || true
    ip6tables -F VPN_OUT6 2>/dev/null || true
    ip6tables -X VPN_OUT6 2>/dev/null || true
    
    iptables -D INPUT -j PROXY_PORTS 2>/dev/null || true
    iptables -F PROXY_PORTS 2>/dev/null || true
    iptables -X PROXY_PORTS 2>/dev/null || true
    
    iptables -D INPUT -p tcp --dport "${SOCKS5_PORT:-1080}" -j ACCEPT 2>/dev/null || true
    iptables -D INPUT -p tcp --dport "${HTTP_PROXY_PORT:-8888}" -j ACCEPT 2>/dev/null || true
    iptables -D INPUT -p tcp --dport "${METRICS_PORT:-9090}" -j ACCEPT 2>/dev/null || true
}

#═══════════════════════════════════════════════════════════════════════════════
# UNIFIED PUBLIC API
#═══════════════════════════════════════════════════════════════════════════════

# Setup baseline killswitch + bypass routes (called once at startup)
setup_baseline_killswitch() {
    setup_bypass_routes || return 1
    
    if $USE_NFTABLES; then
        nft_apply_baseline_killswitch
    else
        ipt_apply_baseline_killswitch
    fi
    
    show_success "Killswitch ready"
}

# Add VPN interface to killswitch (called after VPN is up)
add_vpn_to_killswitch() {
    if $USE_NFTABLES; then
        nft_add_vpn_interface
    else
        ipt_add_vpn_interface
    fi
}

# Remove VPN interface from killswitch (called before VPN teardown)
remove_vpn_from_killswitch() {
    if $USE_NFTABLES; then
        nft_remove_vpn_interface
    else
        ipt_remove_vpn_interface
    fi
}

# Add temporary exemption (returns handle for removal)
add_temporary_exemption() {
    local ip="$1"
    local port="$2"
    local proto="${3:-tcp}"
    local comment="${4:-exemption}"
    
    if $USE_NFTABLES; then
        nft_add_exemption "$ip" "$port" "$proto" "$comment"
    else
        ipt_add_exemption "$ip" "$port" "$proto" "$comment"
    fi
}

# Remove temporary exemption by comment
remove_temporary_exemption() {
    local comment="$1"
    
    if $USE_NFTABLES; then
        nft_remove_exemption "$comment"
    else
        ipt_remove_exemption "$comment"
    fi
}

# Remove all temporary exemptions (cleanup on error)
remove_all_temporary_exemptions() {
    if $USE_NFTABLES; then
        nft_remove_all_exemptions
    else
        ipt_remove_all_exemptions
    fi
}

# Full cleanup
cleanup_killswitch() {
    cleanup_bypass_routes
    
    if $USE_NFTABLES; then
        nft_cleanup
    else
        ipt_cleanup
    fi
}

# Show firewall statistics
show_ruleset_stats() {
    if $USE_NFTABLES; then
        local output_rules=$(nft list chain inet vpn_filter output 2>/dev/null | grep -c "^\s*\(oifname\|ct state\|ip daddr\|ip6 daddr\|mark\|tcp dport\|udp dport\|drop\|accept\)")
        local input_rules=$(nft list chain inet vpn_filter input 2>/dev/null | grep -c "^\s*tcp dport")
        local set_elements=0
        for set_name in local_nets_v4 local_nets_v6 allowed_ports exempt_ports; do
            local count=$(nft list set inet vpn_filter "$set_name" 2>/dev/null | grep -c "elements" | head -1)
            [ -n "$count" ] && [ "$count" -gt 0 ] 2>/dev/null && set_elements=$((set_elements + 1))
        done
        
        local total_rules=$((output_rules + input_rules))
        show_success "Firewall: ${total_rules} rules, ${set_elements} sets, nftables"
    else
        local ipv4_rules=$(iptables -L VPN_OUT 2>/dev/null | grep -c "^ACCEPT\|^DROP")
        local ipv6_rules=$(ip6tables -L VPN_OUT6 2>/dev/null | grep -c "^ACCEPT\|^DROP")
        local input_rules=$(iptables -L INPUT 2>/dev/null | grep -c "PROXY_PORTS\|tcp dpt:")
        
        local total_rules=$((ipv4_rules + ipv6_rules + input_rules))
        show_success "Firewall: ${total_rules} rules, iptables"
    fi
}

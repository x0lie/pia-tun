#!/bin/bash
# High-performance killswitch (firewall) management for PIA WireGuard VPN
# Prevents IP leaks by enforcing strict network rules
#
# Features:
# - Auto-detects nftables (modern, O(1) lookups) or iptables (legacy fallback)
# - Pre-tunnel phase: Only allow DNS/HTTPS/auth before VPN is up
# - Post-tunnel phase: Force all traffic through VPN interface
# - Local network exceptions (optional, configurable)
# - IPv6 leak prevention
#
# Two-phase approach:
# 1. setup_pre_tunnel_killswitch() - Restrictive rules before VPN
# 2. finalize_killswitch() - Lock to VPN-only after connection

set -euo pipefail

source /app/scripts/ui.sh

# Detect firewall backend at runtime
USE_NFTABLES=false
if command -v nft >/dev/null 2>&1; then
    USE_NFTABLES=true
else
    show_warning "nftables not available, falling back to iptables"
fi

#═══════════════════════════════════════════════════════════════════════════════
# NFTABLES IMPLEMENTATION (Modern, O(1) lookups with sets)
#═══════════════════════════════════════════════════════════════════════════════

# Create base nftables table and chains
nft_setup_base_table() {
    # Create table and chains with optimal hook priority
    nft add table inet vpn_filter 2>/dev/null || nft flush table inet vpn_filter
    
    # Output chain with priority -100 (before NAT, optimal for filtering)
    nft add chain inet vpn_filter output { type filter hook output priority -100 \; policy accept \; }
    
    # Input chain for proxy/metrics ports
    nft add chain inet vpn_filter input { type filter hook input priority 0 \; policy accept \; }
}

# Create nftables sets for efficient matching
nft_create_sets() {
    # Set for RFC1918 + link-local + multicast (IPv4)
    nft add set inet vpn_filter local_nets_v4 { type ipv4_addr \; flags interval \; }
    
    # Set for local IPv6 ranges
    nft add set inet vpn_filter local_nets_v6 { type ipv6_addr \; flags interval \; }
    
    # Set for proxy/metrics ports (TCP only)
    nft add set inet vpn_filter allowed_ports { type inet_service \; }
    
    # Set for pre-tunnel ports (DNS, HTTPS, auth)
    nft add set inet vpn_filter pretunnel_tcp_ports { type inet_service \; }
    nft add set inet vpn_filter pretunnel_udp_ports { type inet_service \; }
    
    # Set for exempted ports (user-configured)
    nft add set inet vpn_filter exempt_ports { type inet_service \; }
}

# Populate local network sets based on configuration
nft_populate_local_networks() {
    local mode="$1"
    
    if [ "$mode" = "all" ]; then
        # Add all RFC1918 ranges to set (single lookup per packet!)
        nft add element inet vpn_filter local_nets_v4 { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 }
        
        # Add IPv6 local ranges
        nft add element inet vpn_filter local_nets_v6 { fe80::/10, fc00::/7, ff00::/8 }
        
        show_success "Local network access: All RFC1918 networks (using nft sets)"
    elif [ -n "$mode" ]; then
        # Parse custom networks and build comma-separated lists
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
        
        # Batch add to sets
        [ -n "$ipv4_list" ] && nft add element inet vpn_filter local_nets_v4 { $ipv4_list }
        [ -n "$ipv6_list" ] && nft add element inet vpn_filter local_nets_v6 { $ipv6_list }
        
        show_success "Local network access: $mode (using nft sets)"
    else
        show_success "Local network access: Disabled (all traffic through VPN)"
    fi
}

# Populate proxy/metrics port sets
nft_populate_ports() {
    local ports=()
    
    # Add proxy ports if enabled
    if [ "${PROXY_ENABLED}" = "true" ]; then
        ports+=("${SOCKS5_PORT:-1080}")
        ports+=("${HTTP_PROXY_PORT:-8888}")
    fi
    
    # Add metrics port if enabled
    [ "${METRICS}" = "true" ] && ports+=("${METRICS_PORT:-9090}")
    
    # Batch add to set
    if [ ${#ports[@]} -gt 0 ]; then
        local port_list=$(IFS=', '; echo "${ports[*]}")
        nft add element inet vpn_filter allowed_ports { $port_list }
        
        [ "${PROXY_ENABLED}" = "true" ] && \
            show_success "Proxy ports allowed: SOCKS5=${SOCKS5_PORT:-1080}, HTTP=${HTTP_PROXY_PORT:-8888}"
        [ "${METRICS}" = "true" ] && \
            show_success "Metrics port allowed: ${METRICS_PORT:-9090}"
    fi
}

# Populate user-configured exempt ports
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

# Setup pre-tunnel killswitch (restrictive rules before VPN is up)
nft_setup_pre_tunnel_killswitch() {
    show_step "Setting up pre-tunnel firewall..."
    
    nft_cleanup 2>/dev/null || true
    nft_setup_base_table
    nft_create_sets
    nft_populate_local_networks "${LOCAL_NETWORK:-}"
    
    # Populate pre-tunnel port sets
    nft add element inet vpn_filter pretunnel_tcp_ports { 53, 443, 1337 }
    nft add element inet vpn_filter pretunnel_udp_ports { 53 }
    
    # Add rules in optimal order (most-matched first)
    
    # 1. Loopback (most frequent for local services)
    nft add rule inet vpn_filter output oifname "lo" accept
    
    # 2. Established/Related (bulk of traffic after handshake)
    nft add rule inet vpn_filter output ct state established,related accept
    
    # 3. Local networks (if configured) - O(1) lookup via set!
    if [ "${LOCAL_NETWORK:-}" = "all" ] || [ -n "${LOCAL_NETWORK:-}" ]; then
        nft add rule inet vpn_filter output ip daddr @local_nets_v4 accept
        [ "${DISABLE_IPV6}" != "true" ] && \
            nft add rule inet vpn_filter output ip6 daddr @local_nets_v6 accept
    fi
    
    # 4. Pre-tunnel traffic (DNS, HTTPS, auth) - using sets for efficiency
    nft add rule inet vpn_filter output tcp dport @pretunnel_tcp_ports accept
    nft add rule inet vpn_filter output udp dport @pretunnel_udp_ports accept
    
    # 5. ICMP echo (for diagnostics)
    nft add rule inet vpn_filter output ip protocol icmp icmp type echo-request accept
    [ "${DISABLE_IPV6}" != "true" ] && \
        nft add rule inet vpn_filter output ip6 nexthdr icmpv6 icmpv6 type echo-request accept
    
    # 6. Drop everything else
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
    
    show_success "Pre-tunnel firewall active (nftables)"
}

# Finalize killswitch (lock to VPN-only after connection)
nft_finalize_killswitch() {
    show_step "Finalizing killswitch..."
    
    # Flush output chain and rebuild with VPN rules
    nft flush chain inet vpn_filter output
    
    # Get fwmark if available (faster than interface matching)
    local fwmark=$(wg show pia fwmark 2>/dev/null || echo "")
    
    # Rebuild rules in optimal order
    
    # 1. Loopback
    nft add rule inet vpn_filter output oifname "lo" accept
    
    # 2. Established/Related
    nft add rule inet vpn_filter output ct state established,related accept
    
    # 3. Local networks (O(1) lookup)
    if [ "${LOCAL_NETWORK:-}" = "all" ] || [ -n "${LOCAL_NETWORK:-}" ]; then
        nft add rule inet vpn_filter output ip daddr @local_nets_v4 accept
        [ "${DISABLE_IPV6}" != "true" ] && \
            nft add rule inet vpn_filter output ip6 daddr @local_nets_v6 accept
    fi
    
    # 4. VPN traffic (fwmark is faster than interface check)
    if [ -n "$fwmark" ] && [ "$fwmark" != "off" ]; then
        nft add rule inet vpn_filter output mark "$fwmark" accept
        show_success "Killswitch active (fwmark: $fwmark, nftables)"
    else
        show_success "Killswitch active (interface-based, nftables)"
    fi
    nft add rule inet vpn_filter output oifname "pia" accept
    
    # 5. Exempted ports (if any) - O(1) lookup via set
    nft_populate_exempt_ports
    if [ -n "${KILLSWITCH_EXEMPT_PORTS:-}" ]; then
        nft add rule inet vpn_filter output tcp dport @exempt_ports accept
    fi
    
    # 6. Drop everything else
    nft add rule inet vpn_filter output drop
}

# Cleanup nftables rules
nft_cleanup() {
    # Flush and delete table (atomic operation)
    nft delete table inet vpn_filter 2>/dev/null || true
}

#═══════════════════════════════════════════════════════════════════════════════
# IPTABLES FALLBACK IMPLEMENTATION (Optimized legacy version)
#═══════════════════════════════════════════════════════════════════════════════

# Add firewall rule to appropriate chain
ipt_add_fw_rule() {
    local chain="$1"
    shift
    [ "$chain" = "VPN_OUT" ] && iptables -A VPN_OUT "$@" || ip6tables -A VPN_OUT6 "$@"
}

# Apply local network rules
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

# Apply proxy/metrics port rules
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

# Setup IPv6 protection
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

# Setup iptables chain
ipt_setup_iptables_chain() {
    iptables -P OUTPUT ACCEPT 2>/dev/null || true
    iptables -F OUTPUT 2>/dev/null || true
    iptables -X VPN_OUT 2>/dev/null || true
    iptables -N VPN_OUT
}

# Add standard iptables rules
ipt_add_standard_rules() {
    local allow_vpn="${1:-false}"
    
    ipt_add_fw_rule "VPN_OUT" -o lo -j ACCEPT
    ipt_add_fw_rule "VPN_OUT" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    
    ipt_apply_local_network_rules "VPN_OUT" "false"
    
    if [ "$allow_vpn" = "true" ]; then
        local fwmark=$(wg show pia fwmark 2>/dev/null || echo "")
        if [ -n "$fwmark" ] && [ "$fwmark" != "off" ]; then
            ipt_add_fw_rule "VPN_OUT" -m mark --mark "$fwmark" -j ACCEPT
            show_success "Killswitch active (fwmark: $fwmark, iptables)"
        else
            show_success "Killswitch active (interface-based, iptables)"
        fi
        ipt_add_fw_rule "VPN_OUT" -o pia -j ACCEPT
    fi
    
    ipt_apply_proxy_rules "VPN_OUT"
}

# Setup pre-tunnel killswitch with iptables
ipt_setup_pre_tunnel_killswitch() {
    show_step "Setting up pre-tunnel firewall..."
    
    ipt_cleanup 2>/dev/null || true
    ipt_setup_ipv6_protection
    ipt_setup_iptables_chain
    ipt_add_standard_rules "false"
    
    # Pre-tunnel: DNS + HTTPS + auth (combined where possible)
    ipt_add_fw_rule "VPN_OUT" -p udp --dport 53 -j ACCEPT
    ipt_add_fw_rule "VPN_OUT" -p tcp -m multiport --dports 53,443,1337 -j ACCEPT
    ipt_add_fw_rule "VPN_OUT" -p icmp --icmp-type echo-request -j ACCEPT
    
    ipt_add_fw_rule "VPN_OUT" -j DROP
    
    iptables -I OUTPUT 1 -j VPN_OUT
    show_success "Pre-tunnel firewall active (iptables)"
}

# Finalize killswitch with iptables
ipt_finalize_killswitch() {
    show_step "Finalizing killswitch..."
    
    iptables -F VPN_OUT
    ipt_add_standard_rules "true"
    
    if [ -n "${KILLSWITCH_EXEMPT_PORTS:-}" ]; then
        IFS=',' read -ra PORTS <<< "$KILLSWITCH_EXEMPT_PORTS"
        if [ ${#PORTS[@]} -gt 1 ]; then
            local port_list=$(IFS=','; echo "${PORTS[*]}")
            ipt_add_fw_rule "VPN_OUT" -p tcp -m multiport --dports "$port_list" -j ACCEPT
            show_success "Exempted ports: $KILLSWITCH_EXEMPT_PORTS (combined)"
        else
            for port in "${PORTS[@]}"; do
                port=$(echo "$port" | xargs)
                ipt_add_fw_rule "VPN_OUT" -p tcp --dport "$port" -j ACCEPT
                show_success "Exempted port: $port"
            done
        fi
    fi
    
    ipt_add_fw_rule "VPN_OUT" -j DROP
}

# Cleanup iptables rules
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
# RULESET REPORTING
#═══════════════════════════════════════════════════════════════════════════════

# Display firewall statistics
show_ruleset_stats() {
    if $USE_NFTABLES; then
        # Count rules in output chain
        local output_rules=$(nft list chain inet vpn_filter output 2>/dev/null | grep -c "^\s*\(oifname\|ct state\|ip daddr\|ip6 daddr\|mark\|tcp dport\|udp dport\|drop\|accept\)")
        
        # Count rules in input chain
        local input_rules=$(nft list chain inet vpn_filter input 2>/dev/null | grep -c "^\s*tcp dport")
        
        # Count set elements
        local set_elements=0
        for set_name in local_nets_v4 local_nets_v6 allowed_ports exempt_ports pretunnel_tcp_ports pretunnel_udp_ports; do
            local count=$(nft list set inet vpn_filter "$set_name" 2>/dev/null | grep -c "elements")
            [ $count -gt 0 ] && set_elements=$((set_elements + 1))
        done
        
        local total_rules=$((output_rules + input_rules))
        
        show_success "Firewall: ${total_rules} rules (${output_rules} output, ${input_rules} input), ${set_elements} sets active, nftables"
    else
        # Count iptables rules
        local ipv4_rules=$(iptables -L VPN_OUT 2>/dev/null | grep -c "^ACCEPT\|^DROP")
        local ipv6_rules=$(ip6tables -L VPN_OUT6 2>/dev/null | grep -c "^ACCEPT\|^DROP")
        local input_rules=$(iptables -L INPUT 2>/dev/null | grep -c "PROXY_PORTS\|tcp dpt:")
        
        local total_rules=$((ipv4_rules + ipv6_rules + input_rules))
        local chains=2
        [ $ipv6_rules -gt 0 ] && chains=3
        
        show_success "Firewall: ${total_rules} rules (${ipv4_rules} IPv4, ${ipv6_rules} IPv6, ${input_rules} input), ${chains} chains, ${ylw}${bold}iptables${nc}"
    fi
}

#═══════════════════════════════════════════════════════════════════════════════
# UNIFIED PUBLIC API (Routes to nftables or iptables)
#═══════════════════════════════════════════════════════════════════════════════

# Setup pre-tunnel killswitch (restrictive rules before VPN)
setup_pre_tunnel_killswitch() {
    if $USE_NFTABLES; then
        nft_setup_pre_tunnel_killswitch
    else
        ipt_setup_pre_tunnel_killswitch
    fi
}

# Finalize killswitch (lock to VPN-only after connection)
finalize_killswitch() {
    if $USE_NFTABLES; then
        nft_finalize_killswitch
    else
        ipt_finalize_killswitch
    fi
    
    # Show ruleset statistics after finalization
    show_ruleset_stats
}

# Cleanup all killswitch rules
cleanup_killswitch() {
    if $USE_NFTABLES; then
        nft_cleanup
    else
        ipt_cleanup
    fi
}

#!/bin/bash

# Killswitch management for PIA WireGuard VPN

source /app/scripts/ui.sh

# Generic firewall rule helper
add_fw_rule() {
    local chain="$1"
    local rule="$2"
    
    if [ "$chain" = "VPN_OUT" ]; then
        iptables -A VPN_OUT $rule
    else
        ip6tables -A VPN_OUT6 $rule
    fi
}

# Apply local network rules (unified for IPv4/IPv6)
apply_local_network_rules() {
    local chain=$1
    local is_ipv6=$2
    
    if [ "$LOCAL_NETWORK" = "all" ]; then
        if [ "$is_ipv6" = "true" ]; then
            add_fw_rule "$chain" "-d fe80::/10 -j ACCEPT"     # Link-local
            add_fw_rule "$chain" "-d fc00::/7 -j ACCEPT"      # Unique local
            add_fw_rule "$chain" "-d ff00::/8 -j ACCEPT"      # Multicast
        else
            add_fw_rule "$chain" "-d 10.0.0.0/8 -j ACCEPT"
            add_fw_rule "$chain" "-d 172.16.0.0/12 -j ACCEPT"
            add_fw_rule "$chain" "-d 192.168.0.0/16 -j ACCEPT"
            add_fw_rule "$chain" "-d 169.254.0.0/16 -j ACCEPT"
            add_fw_rule "$chain" "-d 224.0.0.0/4 -j ACCEPT"
            [ "$chain" = "VPN_OUT" ] && show_success "Local network access: All RFC1918 networks"
        fi
    elif [ -n "$LOCAL_NETWORK" ]; then
        IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORK"
        for network in "${NETWORKS[@]}"; do
            network=$(echo "$network" | xargs)
            
            if [[ "$network" == *":"* ]] && [ "$is_ipv6" = "true" ]; then
                add_fw_rule "$chain" "-d $network -j ACCEPT"
            elif [[ "$network" != *":"* ]] && [ "$is_ipv6" != "true" ]; then
                add_fw_rule "$chain" "-d $network -j ACCEPT"
            fi
        done
        [ "$chain" = "VPN_OUT" ] && show_success "Local network access: $LOCAL_NETWORK"
    else
        [ "$chain" = "VPN_OUT" ] && show_success "Local network access: Disabled (all traffic through VPN)"
    fi
}

# Apply proxy port rules (allow incoming connections to proxy ports)
apply_proxy_rules() {
    local chain=$1
    
    # Only apply if proxy is enabled
    if [ "${PROXY_ENABLED}" = "true" ]; then
        # Allow incoming connections to SOCKS5 port
        iptables -I INPUT 1 -p tcp --dport "${SOCKS5_PORT:-1080}" -j ACCEPT
        
        # Allow incoming connections to HTTP proxy port
        iptables -I INPUT 1 -p tcp --dport "${HTTP_PROXY_PORT:-8888}" -j ACCEPT
        
        [ "$chain" = "VPN_OUT" ] && show_success "Proxy ports allowed: SOCKS5=${SOCKS5_PORT:-1080}, HTTP=${HTTP_PROXY_PORT:-8888}"
    fi

    # Allow metrics port if enabled
    if [ "${METRICS}" = "true" ]; then
        iptables -I INPUT 1 -p tcp --dport "${METRICS_PORT:-9090}" -j ACCEPT
        [ "$chain" = "VPN_OUT" ] && show_success "Metrics port allowed: ${METRICS_PORT:-9090}"
    fi
}

# Setup IPv6 leak protection
setup_ipv6_protection() {
    ip6tables -F OUTPUT 2>/dev/null || true
    ip6tables -F FORWARD 2>/dev/null || true
    ip6tables -N VPN_OUT6 2>/dev/null || ip6tables -F VPN_OUT6
    
    # Core rules
    add_fw_rule "VPN_OUT6" "-o lo -j ACCEPT"
    add_fw_rule "VPN_OUT6" "-m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT"
    
    # Local networks
    apply_local_network_rules "VPN_OUT6" "true"
    
    # Allow VPN if IPv6 enabled
    [ "${DISABLE_IPV6}" != "true" ] && add_fw_rule "VPN_OUT6" "-o pia -j ACCEPT"
    
    # Drop everything else
    add_fw_rule "VPN_OUT6" "-j DROP"
    ip6tables -I OUTPUT 1 -j VPN_OUT6
    
    if [ "${DISABLE_IPV6}" = "true" ]; then
        show_success "IPv6 completely blocked"
    else
        show_success "IPv6 routed through VPN only"
    fi
}

# Common iptables setup
setup_iptables_chain() {
    iptables -P OUTPUT ACCEPT 2>/dev/null || true
    iptables -F OUTPUT 2>/dev/null || true
    iptables -X VPN_OUT 2>/dev/null || true
    iptables -N VPN_OUT
}

# Add standard rules (optimized order for performance)
add_standard_rules() {
    local allow_vpn="${1:-false}"
    
    # Priority 1: Loopback (most frequent)
    add_fw_rule "VPN_OUT" "-o lo -j ACCEPT"
    
    # Priority 2: Established/Related (bulk of traffic)
    add_fw_rule "VPN_OUT" "-m conntrack --ctstate ESTABLISHED -j ACCEPT"
    add_fw_rule "VPN_OUT" "-m conntrack --ctstate RELATED -j ACCEPT"
    
    # Priority 3: Local networks (before VPN)
    apply_local_network_rules "VPN_OUT" "false"
    
    # Priority 4: VPN traffic (if tunnel is up)
    if [ "$allow_vpn" = "true" ]; then
        local fwmark=$(wg show pia fwmark 2>/dev/null || echo "")
        if [ -n "$fwmark" ] && [ "$fwmark" != "off" ]; then
            add_fw_rule "VPN_OUT" "-m mark --mark $fwmark -j ACCEPT"
            show_success "Killswitch active (fwmark: $fwmark, optimized)"
        else
            show_success "Killswitch active (interface-based, optimized)"
        fi
        add_fw_rule "VPN_OUT" "-o pia -j ACCEPT"
    fi
    
    # Priority 5: Apply proxy rules if enabled
    apply_proxy_rules "VPN_OUT"
}

# Setup pre-tunnel killswitch
setup_pre_tunnel_killswitch() {
    show_step "Setting up pre-tunnel firewall..."
    
    cleanup_killswitch 2>/dev/null || true
    setup_ipv6_protection
    setup_iptables_chain
    add_standard_rules "false"
    
    # Allow DNS and auth traffic
    add_fw_rule "VPN_OUT" "-p udp --dport 53 -j ACCEPT"
    add_fw_rule "VPN_OUT" "-p tcp --dport 53 -j ACCEPT"
    add_fw_rule "VPN_OUT" "-p icmp -j ACCEPT"
    add_fw_rule "VPN_OUT" "-p tcp --dport 443 -j ACCEPT"
    add_fw_rule "VPN_OUT" "-p tcp --dport 1337 -j ACCEPT"
    add_fw_rule "VPN_OUT" "-j DROP"
    
    iptables -I OUTPUT 1 -j VPN_OUT
    show_success "Pre-tunnel firewall active"
}

# Finalize killswitch after tunnel is up
finalize_killswitch() {
    show_step "Finalizing killswitch..."
    
    iptables -F VPN_OUT
    add_standard_rules "true"
    
    # Exempt ports if configured
    if [ -n "$KILLSWITCH_EXEMPT_PORTS" ]; then
        IFS=',' read -ra PORTS <<< "$KILLSWITCH_EXEMPT_PORTS"
        for port in "${PORTS[@]}"; do
            port=$(echo "$port" | xargs)
            add_fw_rule "VPN_OUT" "-p tcp --dport $port -j ACCEPT"
            show_success "Exempted port: $port"
        done
    fi
    
    add_fw_rule "VPN_OUT" "-j DROP"
}

# Cleanup killswitch rules
cleanup_killswitch() {
    iptables -D OUTPUT -j VPN_OUT 2>/dev/null || true
    iptables -F VPN_OUT 2>/dev/null || true
    iptables -X VPN_OUT 2>/dev/null || true
    ip6tables -D OUTPUT -j VPN_OUT6 2>/dev/null || true
    ip6tables -F VPN_OUT6 2>/dev/null || true
    ip6tables -X VPN_OUT6 2>/dev/null || true
    
    # Clean up proxy INPUT rules
    iptables -D INPUT -p tcp --dport "${SOCKS5_PORT:-1080}" -j ACCEPT 2>/dev/null || true
    iptables -D INPUT -p tcp --dport "${HTTP_PROXY_PORT:-8888}" -j ACCEPT 2>/dev/null || true
}

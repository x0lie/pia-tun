#!/bin/bash

# Killswitch management for PIA WireGuard VPN

# Apply local network rules based on LOCAL_NETWORK or defaults
apply_local_network_rules() {
    local chain=$1
    
    if [ "$LOCAL_NETWORK" = "all" ]; then
        # User explicitly requested all RFC1918 private networks
        if [ "$chain" = "iptables" ]; then
            iptables -A VPN_OUT -d 10.0.0.0/8 -j ACCEPT
            iptables -A VPN_OUT -d 172.16.0.0/12 -j ACCEPT
            iptables -A VPN_OUT -d 192.168.0.0/16 -j ACCEPT
            iptables -A VPN_OUT -d 169.254.0.0/16 -j ACCEPT  # Link-local
            iptables -A VPN_OUT -d 224.0.0.0/4 -j ACCEPT     # Multicast
            show_success "Local network access: All RFC1918 networks allowed"
        else
            ip6tables -A VPN_OUT6 -d fe80::/10 -j ACCEPT     # Link-local
            ip6tables -A VPN_OUT6 -d fc00::/7 -j ACCEPT      # Unique local
            ip6tables -A VPN_OUT6 -d ff00::/8 -j ACCEPT      # Multicast
        fi
    elif [ -n "$LOCAL_NETWORK" ]; then
        # User specified custom local networks
        IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORK"
        for network in "${NETWORKS[@]}"; do
            network=$(echo "$network" | xargs)  # Trim whitespace
            
            # Determine if this is IPv4 or IPv6
            if [[ "$network" == *":"* ]]; then
                # IPv6 address
                if [ "$chain" = "ip6tables" ]; then
                    ip6tables -A VPN_OUT6 -d "$network" -j ACCEPT
                fi
            else
                # IPv4 address
                if [ "$chain" = "iptables" ]; then
                    iptables -A VPN_OUT -d "$network" -j ACCEPT
                fi
            fi
        done
        
        if [ "$chain" = "iptables" ]; then
            show_success "Local network access: $LOCAL_NETWORK"
        fi
    else
        # Default: No local network access - all traffic through VPN
        if [ "$chain" = "iptables" ]; then
            show_success "Local network access: Disabled (all traffic through VPN)"
        fi
    fi
}

# Setup IPv6 leak protection
setup_ipv6_protection() {
    
    # Flush existing IPv6 rules
    ip6tables -F OUTPUT 2>/dev/null || true
    ip6tables -F FORWARD 2>/dev/null || true
    
    # Create custom chain for IPv6
    ip6tables -N VPN_OUT6 2>/dev/null || ip6tables -F VPN_OUT6
    
    # Allow loopback
    ip6tables -A VPN_OUT6 -o lo -j ACCEPT
    
    # Allow established/related connections
    ip6tables -A VPN_OUT6 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    
    # Apply local network rules
    apply_local_network_rules "ip6tables"
    
    # If IPv6 is enabled, allow through VPN interface
    if [ "${DISABLE_IPV6}" != "true" ]; then
        ip6tables -A VPN_OUT6 -o pia -j ACCEPT
    fi
    
    # DROP everything else (prevents IPv6 leaks)
    ip6tables -A VPN_OUT6 -j DROP
    
    # Jump to chain from OUTPUT
    ip6tables -I OUTPUT 1 -j VPN_OUT6
    
    if [ "${DISABLE_IPV6}" = "true" ]; then
        show_success "IPv6 completely blocked (leak protection active)"
    else
        show_success "IPv6 routed through VPN only"
    fi
}

# Setup initial killswitch BEFORE tunnel comes up
setup_pre_tunnel_killswitch() {
    show_step "Setting up pre-tunnel firewall..."
    
    # IMPORTANT: Completely flush existing rules first
    # This is critical for reconnection to work
    cleanup_killswitch 2>/dev/null || true
    
    # Setup IPv6 protection first
    setup_ipv6_protection
    
    # Ensure OUTPUT policy is ACCEPT before we start
    iptables -P OUTPUT ACCEPT 2>/dev/null || true
    iptables -P FORWARD ACCEPT 2>/dev/null || true
    
    # Flush existing OUTPUT rules to start completely clean
    iptables -F OUTPUT 2>/dev/null || true
    iptables -F FORWARD 2>/dev/null || true
    
    # Create a custom chain for VPN rules (delete if exists)
    iptables -X VPN_OUT 2>/dev/null || true
    iptables -N VPN_OUT
    
    # OPTIMIZATION: Allow loopback first (most frequent for container internals)
    iptables -A VPN_OUT -o lo -j ACCEPT
    
    # OPTIMIZATION: Established connections BEFORE new connection checks
    # This handles the bulk of traffic with minimal processing
    iptables -A VPN_OUT -m conntrack --ctstate ESTABLISHED -j ACCEPT
    iptables -A VPN_OUT -m conntrack --ctstate RELATED -j ACCEPT
    
    # Allow local/private networks (Docker/K8s/home networks)
    apply_local_network_rules "iptables"
    
    # Allow DNS to ANY server during setup (critical for reconnection)
    iptables -A VPN_OUT -p udp --dport 53 -j ACCEPT
    iptables -A VPN_OUT -p tcp --dport 53 -j ACCEPT
    
    # Allow ICMP (for ping health checks and connectivity tests)
    iptables -A VPN_OUT -p icmp -j ACCEPT
    
    # Allow HTTPS to PIA endpoints for auth/setup (0.0.0.0/0 allows reconnection)
    iptables -A VPN_OUT -p tcp --dport 443 -j ACCEPT
    iptables -A VPN_OUT -p tcp --dport 1337 -j ACCEPT
    
    # OPTIMIZATION: Use DROP instead of REJECT (no ICMP overhead)
    iptables -A VPN_OUT -j DROP
    
    # Jump to our chain from OUTPUT
    iptables -I OUTPUT 1 -j VPN_OUT
    
    show_success "Pre-tunnel firewall active"
}

# Update killswitch after tunnel is established
finalize_killswitch() {
    show_step "Finalizing killswitch..."
    
    # Get WireGuard fwmark
    FWMARK=$(wg show pia fwmark 2>/dev/null || echo "")
    
    if [ -z "$FWMARK" ] || [ "$FWMARK" = "off" ]; then
        show_warning "No fwmark detected, using interface-based rules"
        USE_INTERFACE=true
    else
        USE_INTERFACE=false
    fi
    
    # Flush and rebuild our custom chain
    iptables -F VPN_OUT
    
    # OPTIMIZATION: Order rules by frequency of match for best performance
    
    # Priority 1: Loopback (always allow, frequently used)
    iptables -A VPN_OUT -o lo -j ACCEPT
    
    # Priority 2: ESTABLISHED first, then RELATED (handles 90%+ of traffic)
    # Separating these allows faster matching for bulk data transfers
    iptables -A VPN_OUT -m conntrack --ctstate ESTABLISHED -j ACCEPT
    iptables -A VPN_OUT -m conntrack --ctstate RELATED -j ACCEPT
    
    # Priority 3: Local/private networks BEFORE VPN (critical for local DNS/services)
    apply_local_network_rules "iptables"
    
    # Priority 4: VPN traffic (will be most new connections after tunnel is up)
    if [ "$USE_INTERFACE" = false ]; then
        # OPTIMIZATION: fwmark matching is faster than interface matching
        iptables -A VPN_OUT -m mark --mark "$FWMARK" -j ACCEPT
    fi
    iptables -A VPN_OUT -o pia -j ACCEPT
    
    # Priority 5: Exempt ports (if any)
    if [ -n "$KILLSWITCH_EXEMPT_PORTS" ]; then
        IFS=',' read -ra PORTS <<< "$KILLSWITCH_EXEMPT_PORTS"
        for port in "${PORTS[@]}"; do
            port=$(echo "$port" | xargs)
            iptables -A VPN_OUT -p tcp --dport "$port" -j ACCEPT
            show_success "Exempted port: $port"
        done
    fi
    
    # OPTIMIZATION: Use DROP instead of REJECT
    # - No ICMP packet generation = less CPU and bandwidth usage
    # - Better for security (no information leakage)
    # - Slightly faster packet processing
    iptables -A VPN_OUT -j DROP
    
    if [ "$USE_INTERFACE" = false ]; then
        show_success "Killswitch active (fwmark: $FWMARK, optimized)"
    else
        show_success "Killswitch active (interface-based, optimized)"
    fi
}

# Cleanup killswitch rules
cleanup_killswitch() {
    # Clean up IPv4
    iptables -D OUTPUT -j VPN_OUT 2>/dev/null || true
    iptables -F VPN_OUT 2>/dev/null || true
    iptables -X VPN_OUT 2>/dev/null || true
    
    # Clean up IPv6
    ip6tables -D OUTPUT -j VPN_OUT6 2>/dev/null || true
    ip6tables -F VPN_OUT6 2>/dev/null || true
    ip6tables -X VPN_OUT6 2>/dev/null || true
}

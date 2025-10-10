#!/bin/bash

# Killswitch management for PIA WireGuard VPN

# Setup initial killswitch BEFORE tunnel comes up
setup_pre_tunnel_killswitch() {
    show_step "Setting up pre-tunnel firewall..."
    
    # Flush existing rules to start clean
    iptables -F OUTPUT 2>/dev/null || true
    iptables -F FORWARD 2>/dev/null || true
    
    # Create a custom chain for VPN rules
    iptables -N VPN_OUT 2>/dev/null || iptables -F VPN_OUT
    
    # OPTIMIZATION: Allow loopback first (most frequent for container internals)
    iptables -A VPN_OUT -o lo -j ACCEPT
    
    # OPTIMIZATION: Established connections BEFORE new connection checks
    # This handles the bulk of traffic with minimal processing
    iptables -A VPN_OUT -m conntrack --ctstate ESTABLISHED -j ACCEPT
    iptables -A VPN_OUT -m conntrack --ctstate RELATED -j ACCEPT
    
    # Allow local/private networks (Docker/K8s/Cilium)
    iptables -A VPN_OUT -d 10.0.0.0/8 -j ACCEPT
    iptables -A VPN_OUT -d 172.16.0.0/12 -j ACCEPT
    iptables -A VPN_OUT -d 192.168.0.0/16 -j ACCEPT
    iptables -A VPN_OUT -d 169.254.0.0/16 -j ACCEPT  # Link-local
    iptables -A VPN_OUT -d 224.0.0.0/4 -j ACCEPT     # Multicast
    
    # Allow DNS to PIA servers during setup
    iptables -A VPN_OUT -p udp --dport 53 -j ACCEPT
    iptables -A VPN_OUT -p tcp --dport 53 -j ACCEPT
    
    # Allow HTTPS to PIA endpoints for auth/setup
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
    
    # Priority 3: VPN traffic (will be most new connections after tunnel is up)
    if [ "$USE_INTERFACE" = false ]; then
        # OPTIMIZATION: fwmark matching is faster than interface matching
        iptables -A VPN_OUT -m mark --mark "$FWMARK" -j ACCEPT
    fi
    iptables -A VPN_OUT -o pia -j ACCEPT
    
    # Priority 4: Local/private networks (less frequent than VPN traffic)
    iptables -A VPN_OUT -d 10.0.0.0/8 -j ACCEPT
    iptables -A VPN_OUT -d 172.16.0.0/12 -j ACCEPT
    iptables -A VPN_OUT -d 192.168.0.0/16 -j ACCEPT
    iptables -A VPN_OUT -d 169.254.0.0/16 -j ACCEPT
    iptables -A VPN_OUT -d 224.0.0.0/4 -j ACCEPT
    
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
    iptables -D OUTPUT -j VPN_OUT 2>/dev/null || true
    iptables -F VPN_OUT 2>/dev/null || true
    iptables -X VPN_OUT 2>/dev/null || true
}

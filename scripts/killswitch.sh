#!/bin/bash
# Prevents IP leaks by enforcing strict iptables rules
#
# Architecture:
# 1. Baseline killswitch - DROP-first chains (VPN_IN, VPN_OUT) block everything except specified local and loopback
# 2. Bypass routing table - routes WAN check IPs around VPN via default gateway
# 3. MSS clamping - prevents fragmentation in VPN tunnel
#
# IPT_CMD and IP6T_CMD are set by Go (internal/firewall) before this script is sourced.
# Idempotent — safe to call multiple times.

set -euo pipefail

source /app/scripts/ui.sh

# Get default gateway and interface
get_default_gateway() {
    local gateway=$(ip route | grep default | head -1 | awk '{print $3}')
    show_debug "Default gateway: ${gateway:-none}"
    echo "$gateway"
}

get_default_interface() {
    local interface=$(ip route | grep default | head -1 | awk '{print $5}')
    show_debug "Default interface: ${interface:-none}"
    echo "$interface"
}

#═══════════════════════════════════════════════════════════════════════════════
# BYPASS ROUTING TABLE (Permanent, Zero Overhead)
#═══════════════════════════════════════════════════════════════════════════════

setup_bypass_routes() {
    show_debug "Setting up bypass routing table"
    local gateway=$(get_default_gateway)
    local interface=$(get_default_interface)

    [ -z "$gateway" ] && { show_error "Cannot determine default gateway"; return 1; }
    [ -z "$interface" ] && { show_error "Cannot determine default interface"; return 1; }

    show_debug "Adding default route to table 100: $gateway via $interface"
    ip route add default via "$gateway" dev "$interface" table 100 2>/dev/null || true

    # Bypass only for new WAN check IPs
    show_debug "Adding bypass rules for WAN check IPs (priority 50)"
    ip rule add to 129.6.15.28 table 100 priority 50 2>/dev/null || true
    ip rule add to 129.6.15.29 table 100 priority 50 2>/dev/null || true
    ip rule add to 132.163.96.1 table 100 priority 50 2>/dev/null || true
    ip rule add to 132.163.97.1 table 100 priority 50 2>/dev/null || true
    ip rule add to 128.138.140.44 table 100 priority 50 2>/dev/null || true

    show_debug "Bypass routing table configured"
}

cleanup_bypass_routes() {
    show_debug "Cleaning up bypass routing rules"

    # Clean up bypass routing rules
    ip rule del to 129.6.15.28 table 100 priority 50 2>/dev/null || true
    ip rule del to 129.6.15.29 table 100 priority 50 2>/dev/null || true
    ip rule del to 132.163.96.1 table 100 priority 50 2>/dev/null || true
    ip rule del to 132.163.97.1 table 100 priority 50 2>/dev/null || true
    ip rule del to 128.138.140.44 table 100 priority 50 2>/dev/null || true

    # Remove bypass table routes
    show_debug "Removing bypass table default route"
    ip route del default table 100 2>/dev/null || true
}

#═══════════════════════════════════════════════════════════════════════════════
# CHAIN SETUP (VPN_IN, VPN_OUT, VPN_IN6, VPN_OUT6)
#═══════════════════════════════════════════════════════════════════════════════

setup_input_chain() {
    show_debug "Setting up iptables INPUT chain (policy drop)"

    # Set up VPN_IN chain with DROP policy
    $IPT_CMD -P INPUT ACCEPT 2>/dev/null || true
    $IPT_CMD -F INPUT 2>/dev/null || true
    $IPT_CMD -X VPN_IN 2>/dev/null || true
    $IPT_CMD -N VPN_IN

    # 1. Established/Related (responses to our outgoing connections - most INPUT traffic)
    show_debug "INPUT Rule 1: Allow established/related"
    $IPT_CMD -A VPN_IN -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

    # 2. Loopback (localhost access to proxy/metrics/dependents)
    show_debug "INPUT Rule 2: Allow loopback"
    $IPT_CMD -A VPN_IN -i lo -j ACCEPT

    # 3. Local networks (if configured - allows LAN access to proxy/metrics/dependents on specific ports only)
    if [ -n "${LOCAL_NETWORKS:-}" ]; then
        show_debug "INPUT Rule 3: Allow from local networks to specific ports"

        # Build list of allowed ports
        local allowed_ports=""

        # Add user-specified LOCAL_PORTS (for dependent services like qBittorrent)
        if [ -n "${LOCAL_PORTS:-}" ]; then
            # Clean up LOCAL_PORTS (convert spaces to commas, remove duplicate separators)
            allowed_ports=$(echo "$LOCAL_PORTS" | tr -s ' ,' ',' | sed 's/^,//;s/,$//')
        fi

        # Only add rules if there are ports to allow
        if [ -n "$allowed_ports" ]; then
            show_debug "Allowing LAN access to ports: $allowed_ports (TCP+UDP)"
            IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORKS"
            for network in "${NETWORKS[@]}"; do
                network=$(echo "$network" | xargs)
                if [[ "$network" != *":"* ]]; then
                    $IPT_CMD -A VPN_IN -s "$network" -p tcp -m multiport --dports "$allowed_ports" -j ACCEPT
                    $IPT_CMD -A VPN_IN -s "$network" -p udp -m multiport --dports "$allowed_ports" -j ACCEPT
                fi
            done
        else
            show_debug "No LOCAL_PORTS configured, skipping LAN port rules"
        fi
    else
        show_debug "INPUT Rule 3: Skipped (no local networks)"
    fi

    # Port forwarding rules are inserted before DROP by Go (internal/firewall/portforward.go)

    # DROP everything else
    show_debug "Adding final INPUT DROP rule"
    $IPT_CMD -A VPN_IN -j DROP

    # Insert into INPUT chain
    show_debug "Inserting VPN_IN into INPUT chain"
    $IPT_CMD -I INPUT 1 -j VPN_IN
}

setup_ipv6_protection() {
    show_debug "Setting up IPv6 protection"

    $IP6T_CMD -F OUTPUT 2>/dev/null || true
    $IP6T_CMD -F FORWARD 2>/dev/null || true
    $IP6T_CMD -F INPUT 2>/dev/null || true
    $IP6T_CMD -X VPN_IN6 2>/dev/null || true
    $IP6T_CMD -X VPN_OUT6 2>/dev/null || true

    # IPv6 INPUT chain
    $IP6T_CMD -N VPN_IN6 2>/dev/null || $IP6T_CMD -F VPN_IN6
    $IP6T_CMD -A VPN_IN6 -i lo -j ACCEPT
    $IP6T_CMD -A VPN_IN6 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

    # Allow ICMPv6 (essential for IPv6 functionality)
    if [ "${IPV6_ENABLED}" != "false" ]; then
        show_debug "Adding ICMPv6 rule to VPN_IN6"
        $IP6T_CMD -A VPN_IN6 -p ipv6-icmp -j ACCEPT
    fi

    # Build list of allowed ports for IPv6
    if [ -n "${LOCAL_NETWORKS:-}" ]; then
        local allowed_ports=""

        # Add user-specified LOCAL_PORTS
        if [ -n "${LOCAL_PORTS:-}" ]; then
            allowed_ports=$(echo "$LOCAL_PORTS" | tr -s ' ,' ',' | sed 's/^,//;s/,$//')
        fi

        if [ -n "$allowed_ports" ]; then
            IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORKS"
            for network in "${NETWORKS[@]}"; do
                network=$(echo "$network" | xargs)
                if [[ "$network" == *":"* ]]; then
                    $IP6T_CMD -A VPN_IN6 -s "$network" -p tcp -m multiport --dports "$allowed_ports" -j ACCEPT
                    $IP6T_CMD -A VPN_IN6 -s "$network" -p udp -m multiport --dports "$allowed_ports" -j ACCEPT
                fi
            done
        fi
    fi

    $IP6T_CMD -A VPN_IN6 -j DROP
    $IP6T_CMD -I INPUT 1 -j VPN_IN6

    # IPv6 OUTPUT chain
    $IP6T_CMD -N VPN_OUT6 2>/dev/null || $IP6T_CMD -F VPN_OUT6

    # DROP-FIRST: Add DROP immediately
    show_debug "Adding IPv6 DROP rule first (always present for security)"
    $IP6T_CMD -A VPN_OUT6 -j DROP

    # Build permanent rules in REVERSE order by inserting at position 1
    # Final order will be: [Est/Rel, Loopback, Local?, ICMPv6?, DROP]

    # Step 1: Insert ICMPv6 (if IPv6 enabled) - will be pushed down
    if [ "${IPV6_ENABLED}" != "false" ]; then
        show_debug "Inserting ICMPv6 rule for VPN_OUT6"
        $IP6T_CMD -I VPN_OUT6 1 -p ipv6-icmp -j ACCEPT
    fi

    # Step 2: Insert local networks (if configured) - pushes ICMPv6 down
    insert_local_network_rules "VPN_OUT6" "true" "$IP6T_CMD"

    # Step 3: Insert loopback at position 1 (pushes local networks and ICMPv6 down)
    show_debug "Inserting IPv6 loopback rule at position 1"
    $IP6T_CMD -I VPN_OUT6 1 -o lo -j ACCEPT

    # Step 4: Insert established/related at position 1 (pushes everything down)
    show_debug "Inserting IPv6 established/related rule at position 1"
    $IP6T_CMD -I VPN_OUT6 1 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

    # Final order: [Est/Rel, Loopback, Local?, ICMPv6?, DROP]
    # VPN will be inserted at position 3 later (after loopback)

    $IP6T_CMD -I OUTPUT 1 -j VPN_OUT6

    [ "${IPV6_ENABLED}" = "false" ] && show_success "IPv6 through tunnel disabled" || \
        show_success "IPv6 through tunnel enabled"
}

setup_iptables_chain() {
    show_debug "Setting up iptables VPN_OUT chain"

    $IPT_CMD -P OUTPUT ACCEPT 2>/dev/null || true
    $IPT_CMD -F OUTPUT 2>/dev/null || true
    $IPT_CMD -X VPN_OUT 2>/dev/null || true
    $IPT_CMD -N VPN_OUT
}

# Insert bypass route rules before DROP position
# Restricted to: eth0 interface + TCP + port 13 (DAYTIME) only
insert_bypass_routes() {
    local chain="$1"
    local cmd="$2"

    show_debug "Inserting bypass route rules before DROP (TCP/13 via default gateway)"

    # Find DROP position (match DROP in target column, works across all iptables variants)
    local drop_pos=$($cmd -L "$chain" --line-numbers -n 2>/dev/null | awk '$2 == "DROP" {print $1; exit}')

    if [ -z "$drop_pos" ]; then
        show_error "Cannot find DROP rule in $chain"
        return 1
    fi

    # Insert all bypass routes before DROP (in reverse order to maintain correct order)
    # Each insert at same position pushes previous down
    $cmd -I "$chain" "$drop_pos" -o eth0 -p tcp --dport 13 -d 128.138.140.44 -j ACCEPT -m comment --comment "bypass_routes"
    $cmd -I "$chain" "$drop_pos" -o eth0 -p tcp --dport 13 -d 132.163.97.1 -j ACCEPT -m comment --comment "bypass_routes"
    $cmd -I "$chain" "$drop_pos" -o eth0 -p tcp --dport 13 -d 132.163.96.1 -j ACCEPT -m comment --comment "bypass_routes"
    $cmd -I "$chain" "$drop_pos" -o eth0 -p tcp --dport 13 -d 129.6.15.29 -j ACCEPT -m comment --comment "bypass_routes"
    $cmd -I "$chain" "$drop_pos" -o eth0 -p tcp --dport 13 -d 129.6.15.28 -j ACCEPT -m comment --comment "bypass_routes"
}

# Insert local network rules at position 1 (will be pushed down by loopback and est/rel)
insert_local_network_rules() {
    local chain="$1"
    local is_ipv6="$2"
    local cmd="$3"

    show_debug "Inserting local network rules at position 1 (chain: $chain, ipv6: $is_ipv6)"

    if [ -n "${LOCAL_NETWORKS:-}" ]; then
        IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORKS"
        # Insert in reverse order so they appear in correct order after all inserts
        for ((i=${#NETWORKS[@]}-1; i>=0; i--)); do
            local network=$(echo "${NETWORKS[$i]}" | xargs)

            if [[ "$network" == *":"* ]] && [ "$is_ipv6" = "true" ]; then
                $cmd -I "$chain" 1 -d "$network" -j ACCEPT
            elif [[ "$network" != *":"* ]] && [ "$is_ipv6" != "true" ]; then
                $cmd -I "$chain" 1 -d "$network" -j ACCEPT
            fi
        done

        # Show success message with port information (only for IPv4 VPN_OUT chain)
        if [ "$chain" = "VPN_OUT" ] && [ "$is_ipv6" != "true" ]; then
            # Build port information
            local port_info=""
            if [ -n "${LOCAL_PORTS:-}" ]; then
                # Clean up and format ports
                local ports=$(echo "$LOCAL_PORTS" | tr -s ' ,' ',' | sed 's/^,//;s/,$//')
                port_info="(ports: $ports)"
            else
                port_info="(outbound only, no listening ports)"
            fi
            show_success "Local network: $LOCAL_NETWORKS $port_info"
        fi
    else
        [ "$chain" = "VPN_OUT" ] && show_success "Local network: Disabled (all traffic through VPN)"
    fi
}

apply_baseline_killswitch() {
    show_debug "Cleaning up existing iptables configuration"
    cleanup_chains 2>/dev/null || true

    setup_input_chain
    setup_iptables_chain
    setup_ipv6_protection

    # DROP-FIRST ARCHITECTURE: Add DROP immediately (security first, no leak windows)
    show_debug "Adding DROP rule first (always present for security)"
    $IPT_CMD -A VPN_OUT -j DROP

    # Add bypass routes before DROP
    insert_bypass_routes "VPN_OUT" "$IPT_CMD"

    # Build permanent rules in REVERSE order by inserting at position 1
    # Each insert pushes previous rules down, so we insert in reverse order
    # Final order will be: [Est/Rel, Loopback, Local?, Bypass routes, DROP]

    # Step 1: Insert local networks (if configured)
    insert_local_network_rules "VPN_OUT" "false" "$IPT_CMD"

    # Step 2: Insert loopback at position 1
    show_debug "Inserting loopback rule at position 1"
    $IPT_CMD -I VPN_OUT 1 -o lo -j ACCEPT

    # Step 3: Insert established/related at position 1
    show_debug "Inserting established/related rule at position 1"
    $IPT_CMD -I VPN_OUT 1 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

    # Step 4: Insert VPN_OUT into OUTPUT chain
    show_debug "Inserting VPN_OUT into OUTPUT chain"
    $IPT_CMD -I OUTPUT 1 -j VPN_OUT
}

# Verify baseline killswitch is actually active
verify_baseline_killswitch() {
    show_debug "Verifying baseline killswitch is active"

    # Check that VPN_OUT chain exists and has DROP rule (-n flag avoids slow DNS lookups)
    if ! $IPT_CMD -L VPN_OUT -n 2>/dev/null | grep -q "DROP"; then
        show_error "Killswitch verification failed: No DROP rule found in iptables"
        return 1
    fi

    # Check that VPN_OUT is actually in OUTPUT chain
    if ! $IPT_CMD -L OUTPUT -n 2>/dev/null | grep -q "VPN_OUT"; then
        show_error "Killswitch verification failed: VPN_OUT not in OUTPUT chain"
        return 1
    fi

    show_debug "Baseline killswitch verification passed"
    return 0
}

cleanup_chains() {
    show_debug "Cleaning up iptables configuration"

    # OUTPUT chains
    $IPT_CMD -D OUTPUT -j VPN_OUT 2>/dev/null || true
    $IPT_CMD -F VPN_OUT 2>/dev/null || true
    $IPT_CMD -X VPN_OUT 2>/dev/null || true
    $IP6T_CMD -D OUTPUT -j VPN_OUT6 2>/dev/null || true
    $IP6T_CMD -F VPN_OUT6 2>/dev/null || true
    $IP6T_CMD -X VPN_OUT6 2>/dev/null || true

    # INPUT chains
    $IPT_CMD -D INPUT -j VPN_IN 2>/dev/null || true
    $IPT_CMD -F VPN_IN 2>/dev/null || true
    $IPT_CMD -X VPN_IN 2>/dev/null || true
    $IP6T_CMD -D INPUT -j VPN_IN6 2>/dev/null || true
    $IP6T_CMD -F VPN_IN6 2>/dev/null || true
    $IP6T_CMD -X VPN_IN6 2>/dev/null || true
}

#═══════════════════════════════════════════════════════════════════════════════
# TCP MSS CLAMPING (Prevents fragmentation issues in VPN tunnels)
#═══════════════════════════════════════════════════════════════════════════════

# MSS clamping automatically adjusts TCP Maximum Segment Size to account for
# VPN tunnel overhead, preventing packet fragmentation and "black hole" connections
# where large packets silently disappear due to blocked ICMP.
setup_mss_clamping() {
    show_debug "Setting up TCP MSS clamping for VPN tunnel"

    local ipv4_ok=0
    local ipv6_ok=0

    # Clamp MSS on forwarded traffic (other containers routing through us)
    if $IPT_CMD -t mangle -A FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null; then
        show_debug "IPv4 FORWARD MSS clamping enabled"
        ipv4_ok=1
    else
        show_debug "IPv4 FORWARD MSS clamping failed (TCPMSS target may not be available)"
    fi

    # Clamp MSS on outgoing traffic (from this container)
    if $IPT_CMD -t mangle -A OUTPUT -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null; then
        show_debug "IPv4 OUTPUT MSS clamping enabled"
        ipv4_ok=1
    else
        show_debug "IPv4 OUTPUT MSS clamping failed (TCPMSS target may not be available)"
    fi

    # IPv6 equivalent (if enabled)
    if [ "${IPV6_ENABLED}" != "false" ]; then
        if $IP6T_CMD -t mangle -A FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null; then
            ipv6_ok=1
        fi
        if $IP6T_CMD -t mangle -A OUTPUT -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null; then
            ipv6_ok=1
        fi
        [ $ipv6_ok -eq 1 ] && show_debug "IPv6 MSS clamping enabled"
    fi

    # Report status accurately
    if [ $ipv4_ok -eq 1 ] || [ $ipv6_ok -eq 1 ]; then
        show_success "TCP MSS clamping enabled"
    else
        show_debug "TCP MSS clamping unavailable (kernel may lack xt_TCPMSS module)"
    fi
}

cleanup_mss_clamping() {
    show_debug "Cleaning up TCP MSS clamping rules"

    # Remove IPv4 mangle rules (suppress errors if they don't exist)
    $IPT_CMD -t mangle -D FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null || true
    $IPT_CMD -t mangle -D OUTPUT -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null || true

    # Remove IPv6 mangle rules
    $IP6T_CMD -t mangle -D FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null || true
    $IP6T_CMD -t mangle -D OUTPUT -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null || true
}

#═══════════════════════════════════════════════════════════════════════════════
# PUBLIC API (called from Go via shellFunc)
#═══════════════════════════════════════════════════════════════════════════════

# Setup baseline killswitch + bypass routes (called once at startup)
setup_baseline_killswitch() {
    show_step "Applying killswitch..."
    show_debug "Setting up baseline killswitch"

    # Remove any stale flag file
    rm -f /tmp/killswitch_up

    # Log which backend was detected at script load
    show_debug "Using firewall backend: $IPT_CMD"
    show_success "Firewall backend: $IPT_CMD"

    # DEFENSIVE: Clean up any orphaned rules from previous container runs
    # This handles cases where cleanup didn't run due to crash, OOM kill, etc.
    # Critical for Kubernetes where network namespaces may be reused
    show_debug "Cleaning up any orphaned killswitch rules from previous runs"
    cleanup_killswitch 2>/dev/null || true

    setup_bypass_routes || {
        show_error "Failed to setup bypass routes"
        return 1
    }

    apply_baseline_killswitch || {
        show_error "Failed to apply iptables killswitch"
        return 1
    }
    sleep 0.5

    # CRITICAL: Verify killswitch is actually active before proceeding
    verify_baseline_killswitch || {
        show_error "Killswitch verification failed - this is a critical security issue"
        return 1
    }

    # Enable MSS clamping to prevent fragmentation issues in VPN tunnel
    setup_mss_clamping

    # Create flag file for health checks ONLY after verification passes
    touch /tmp/killswitch_up
    show_debug "Created /tmp/killswitch_up flag file"

    show_success "Killswitch ready"
}

# Full cleanup
cleanup_killswitch() {
    show_debug "Full killswitch cleanup"
    cleanup_mss_clamping
    cleanup_bypass_routes
    cleanup_chains

    # Remove killswitch flag file
    rm -f /tmp/killswitch_up
    show_debug "Removed /tmp/killswitch_up flag file"
}

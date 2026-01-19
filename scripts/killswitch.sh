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
# - Auto-detects best iptables backend (iptables-nft → iptables → iptables-legacy)
# - Zero-overhead WAN checks via routing bypass
# - Minimal temporary exemptions for PIA API calls
# - No leak windows during reconnection
# - Idempotent operations safe to call multiple times

set -euo pipefail

source /app/scripts/ui.sh

# Detect and set iptables backend at script load time
# This runs immediately when the script is sourced, ensuring variables are available
IPT_CMD=""
IP6T_CMD=""

# Try iptables-nft first (nftables backend with iptables syntax)
# Use -n flag to avoid slow DNS lookups during detection
if iptables-nft -L -n >/dev/null 2>&1; then
    IPT_CMD="iptables-nft"
    IP6T_CMD="ip6tables-nft"
# Fall back to standard iptables
elif iptables -L -n >/dev/null 2>&1; then
    IPT_CMD="iptables"
    IP6T_CMD="ip6tables"
# Last resort: iptables-legacy
elif iptables-legacy -L -n >/dev/null 2>&1; then
    IPT_CMD="iptables-legacy"
    IP6T_CMD="ip6tables-legacy"
else
    show_error "No iptables implementation found"
    exit 1
fi

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
# IPTABLES/NFTABLES IMPLEMENTATION
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

    # 2. Loopback (localhost access to proxy/metrics)
    show_debug "INPUT Rule 2: Allow loopback"
    $IPT_CMD -A VPN_IN -i lo -j ACCEPT

    # 3. Local networks (if configured - allows LAN access to proxy/metrics on specific ports only)
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

    # Port forwarding will be dynamically inserted before DROP by add_forwarded_port() when available
    show_debug "INPUT: Port forwarding will be inserted before DROP when port is allocated"

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

    # Build list of allowed ports for IPv6 (same as IPv4)
    if [ -n "${LOCAL_NETWORKS:-}" ]; then
        local allowed_ports=""

        # Add user-specified LOCAL_PORTS (for dependent services like qBittorrent)
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

    # IPv6 OUTPUT chain - DROP-FIRST ARCHITECTURE
    $IP6T_CMD -N VPN_OUT6 2>/dev/null || $IP6T_CMD -F VPN_OUT6

    # DROP-FIRST: Add DROP immediately (security first, no leak windows)
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

    [ "${IPV6_ENABLED}" = "false" ] && show_success "IPv6 completely blocked" || \
        show_success "IPv6 routed through VPN only"
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

    show_debug "Inserting bypass route rules before DROP (TCP/13 via eth0)"

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
    ks_cleanup 2>/dev/null || true

    setup_ipv6_protection
    setup_input_chain
    setup_iptables_chain

    # DROP-FIRST ARCHITECTURE: Add DROP immediately (security first, no leak windows)
    show_debug "Adding DROP rule first (always present for security)"
    $IPT_CMD -A VPN_OUT -j DROP

    # Add bypass routes before DROP
    insert_bypass_routes "VPN_OUT" "$IPT_CMD"

    # Build permanent rules in REVERSE order by inserting at position 1
    # Each insert pushes previous rules down, so we insert in reverse order
    # Final order will be: [Est/Rel, Loopback, Local?, Bypass routes, DROP]

    # Step 1: Insert local networks (if configured) - these will be pushed down
    insert_local_network_rules "VPN_OUT" "false" "$IPT_CMD"

    # Step 2: Insert loopback at position 1 (pushes local networks down)
    show_debug "Inserting loopback rule at position 1"
    $IPT_CMD -I VPN_OUT 1 -o lo -j ACCEPT

    # Step 3: Insert established/related at position 1 (pushes everything down)
    show_debug "Inserting established/related rule at position 1"
    $IPT_CMD -I VPN_OUT 1 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

    # Final order: [Est/Rel, Loopback, Local?, Bypass routes, DROP]
    # VPN will be inserted at position 3 later (after loopback, before local networks)

    show_debug "Inserting VPN_OUT into OUTPUT chain"
    $IPT_CMD -I OUTPUT 1 -j VPN_OUT
}

add_vpn_interface() {
    show_debug "Adding VPN interface to iptables killswitch"

    # Insert VPN rules at position 3 (after Est/Rel and Loopback, before Local networks)
    # NO DROP REMOVAL - DROP stays in place, preventing any leak window
    local fwmark=$(wg show pia0 fwmark 2>/dev/null)
    show_debug "VPN fwmark: ${fwmark:-off}"

    # Insert VPN interface rule at position 3
    $IPT_CMD -I VPN_OUT 3 -o pia0 -j ACCEPT -m comment --comment "vpn_interface"

    # Insert fwmark rule at position 3 (if present, this pushes interface rule to position 4)
    if [ -n "$fwmark" ] && [ "$fwmark" != "off" ]; then
        $IPT_CMD -I VPN_OUT 3 -m mark --mark "$fwmark" -j ACCEPT -m comment --comment "vpn_fwmark"
        show_success "VPN added to killswitch (fwmark: $fwmark)"
    else
        show_success "VPN added to killswitch (interface-based)"
    fi

    # Order after insert: [Est/Rel, Loopback, VPN-fwmark?, VPN-interface, Local?, Bypass?, DROP]

    # Also update IPv6 (insert at position 3, no DROP removal)
    if [ "${IPV6_ENABLED}" != "false" ]; then
        show_debug "Updating IPv6 rules for VPN"
        $IP6T_CMD -I VPN_OUT6 3 -o pia0 -j ACCEPT
        $IP6T_CMD -I VPN_OUT6 3 -p ipv6-icmp -j ACCEPT
    fi
}

remove_vpn_interface() {
    show_debug "Removing VPN from killswitch"

    # Remove VPN-related rules by finding them with -S and converting to delete
    local removed_ipv4=0
    local removed_ipv6=0

    # Remove IPv4 VPN rules (fwmark and interface)
    while IFS= read -r rule; do
        if [ -n "$rule" ]; then
            local delete_rule="${rule/-A VPN_OUT/-D VPN_OUT}"
            if $IPT_CMD $delete_rule 2>/dev/null; then
                removed_ipv4=$((removed_ipv4 + 1))
                show_debug "Removed IPv4 VPN rule: $(echo "$rule" | grep -o 'comment.*')"
            fi
        fi
    done < <($IPT_CMD -S VPN_OUT 2>/dev/null | grep -E "vpn_fwmark|vpn_interface")

    # Remove IPv6 VPN rules
    while IFS= read -r rule; do
        if [ -n "$rule" ]; then
            local delete_rule="${rule/-A VPN_OUT6/-D VPN_OUT6}"
            if $IP6T_CMD $delete_rule 2>/dev/null; then
                removed_ipv6=$((removed_ipv6 + 1))
                show_debug "Removed IPv6 VPN rule"
            fi
        fi
    done < <($IP6T_CMD -S VPN_OUT6 2>/dev/null | grep "pia0")

    show_debug "Removed $removed_ipv4 IPv4 VPN rule(s), $removed_ipv6 IPv6 VPN rule(s)"
}

# Remove forwarded port from INPUT chain
remove_forwarded_port() {
    show_debug "Removing forwarded port from INPUT"

    # Remove port forwarding rules by finding them with -S and converting to delete
    local removed=0

    # Find and remove all port forwarding rules (handles tcp, udp, and legacy variants)
    while IFS= read -r rule; do
        if [ -n "$rule" ]; then
            local delete_rule="${rule/-A VPN_IN/-D VPN_IN}"
            if $IPT_CMD $delete_rule 2>/dev/null; then
                removed=$((removed + 1))
                show_debug "Removed port forwarding rule: $(echo "$rule" | grep -o 'dport [0-9]*' || echo 'legacy')"
            fi
        fi
    done < <($IPT_CMD -S VPN_IN 2>/dev/null | grep -E "port_forward")

    if [ $removed -gt 0 ]; then
        show_debug "Removed $removed port forwarding rule(s)"
    else
        show_debug "No port forwarding rules found to remove"
    fi
}

add_exemption() {
    local ip="$1"
    local port="$2"
    local proto="$3"
    local comment="$4"

    show_debug "Adding temporary iptables exemption: $ip:$port/$proto (tag: temp_$comment)"

    # Find DROP position and insert before it (exemptions should be last before DROP)
    # Match DROP in target column (column 2), works across all iptables variants
    local drop_pos=$($IPT_CMD -L VPN_OUT --line-numbers -n 2>/dev/null | awk '$2 == "DROP" {print $1; exit}')

    if [ -z "$drop_pos" ]; then
        show_error "Cannot find DROP rule in VPN_OUT chain"
        return 1
    fi

    # Insert before DROP
    $IPT_CMD -I VPN_OUT "$drop_pos" -d "$ip" -p "$proto" --dport "$port" -j ACCEPT -m comment --comment "temp_$comment"
}

ks_cleanup() {
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
# UNIFIED PUBLIC API
#═══════════════════════════════════════════════════════════════════════════════

# Verify baseline killswitch is actually active (CRITICAL for leak prevention)
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

# Verify VPN interface is allowed in killswitch
verify_vpn_in_killswitch() {
    show_debug "Verifying VPN is allowed in killswitch"

    # Use -S (save format) to see comments, since -L doesn't show them
    local rules_output=$($IPT_CMD -S VPN_OUT 2>/dev/null)
    show_debug "VPN_OUT rules:"
    show_debug "$rules_output"

    if ! echo "$rules_output" | grep -q "vpn_interface"; then
        show_error "VPN interface rule not found in killswitch"
        show_error "Expected to find comment 'vpn_interface' in rules above"
        return 1
    fi

    show_debug "VPN killswitch verification passed"
    return 0
}

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

    # CRITICAL: Verify killswitch is actually active before proceeding
    verify_baseline_killswitch || {
        show_error "Killswitch verification failed - this is a critical security issue"
        return 1
    }

    # Create flag file for health checks ONLY after verification passes
    touch /tmp/killswitch_up
    show_debug "Created /tmp/killswitch_up flag file"

    show_success "Killswitch ready"
}

# Add VPN interface to killswitch (called after VPN is up)
add_vpn_to_killswitch() {
    add_vpn_interface || {
        show_error "Failed to add VPN to iptables killswitch"
        return 1
    }

    # CRITICAL: Verify VPN was actually added to killswitch
    verify_vpn_in_killswitch || {
        show_error "VPN killswitch verification failed - VPN may not be protected"
        return 1
    }

    return 0
}

# Remove VPN interface from killswitch (called before VPN teardown)
remove_vpn_from_killswitch() {
    remove_vpn_interface
}

# Add temporary exemption (returns handle for removal)
add_temporary_exemption() {
    local ip="$1"
    local port="$2"
    local proto="${3:-tcp}"
    local comment="${4:-exemption}"

    add_exemption "$ip" "$port" "$proto" "$comment"
}

# Remove temporary exemption by comment
remove_temporary_exemption() {
    local comment="$1"

    show_debug "Removing temporary iptables exemption: temp_$comment"

    # Find and remove ALL matching rules by converting -A to -D
    # This is more robust than trying to match the exact rule specification
    local removed=0
    local failed=0

    # Get all rules with this comment, convert -A to -D, and execute
    while IFS= read -r rule; do
        if [ -n "$rule" ]; then
            # Convert -A VPN_OUT to -D VPN_OUT
            local delete_rule="${rule/-A VPN_OUT/-D VPN_OUT}"
            if $IPT_CMD $delete_rule 2>/dev/null; then
                removed=$((removed + 1))
            else
                failed=$((failed + 1))
                show_debug "Failed to remove rule: $delete_rule"
            fi
        fi
    done < <($IPT_CMD -S VPN_OUT 2>/dev/null | grep "temp_$comment")

    if [ $removed -gt 0 ]; then
        show_debug "Removed $removed instance(s) of temp_$comment"
    fi

    if [ $failed -gt 0 ]; then
        show_error "Failed to remove $failed instance(s) of temp_$comment"
    fi

    if [ $removed -eq 0 ] && [ $failed -eq 0 ]; then
        show_debug "No rules found with comment: temp_$comment (may have been already removed)"
    fi
}

# Remove all temporary exemptions (cleanup on error)
remove_all_temporary_exemptions() {
    show_debug "Removing all temporary exemptions"

    # Remove all rules with "temp_" in comment
    local removed=0
    local failed=0

    # Process each temp_ rule
    while IFS= read -r rule; do
        if [ -n "$rule" ]; then
            # Convert -A VPN_OUT to -D VPN_OUT
            local delete_rule="${rule/-A VPN_OUT/-D VPN_OUT}"
            if $IPT_CMD $delete_rule 2>/dev/null; then
                removed=$((removed + 1))
            else
                failed=$((failed + 1))
                show_debug "Failed to remove rule: $delete_rule"
            fi
        fi
    done < <($IPT_CMD -S VPN_OUT 2>/dev/null | grep "temp_")

    if [ $removed -gt 0 ]; then
        show_debug "Removed $removed temporary exemption(s)"
    fi

    if [ $failed -gt 0 ]; then
        show_error "Failed to remove $failed temporary exemption(s)"
    fi

    if [ $removed -eq 0 ] && [ $failed -eq 0 ]; then
        show_debug "No temporary exemptions found to remove"
    fi
}

# Add forwarded port to INPUT chain (called when port forwarding is enabled)
add_forwarded_port_to_input() {
    local port="$1"

    if [ -z "$port" ]; then
        show_debug "No port specified, skipping forwarded port rule"
        return 0
    fi

    show_debug "Adding forwarded port to INPUT: $port (TCP + UDP)"

    # Verify VPN_IN chain exists before trying to add rule
    if ! $IPT_CMD -L VPN_IN -n >/dev/null 2>&1; then
        show_error "VPN_IN chain not found in killswitch - cannot add port forwarding rule"
        return 1
    fi

    # Remove any existing forwarded port rules first
    remove_forwarded_port

    # Find DROP position dynamically (more robust than hardcoded position)
    local drop_pos=$($IPT_CMD -L VPN_IN --line-numbers -n 2>/dev/null | awk '$2 == "DROP" {print $1; exit}')

    if [ -z "$drop_pos" ]; then
        show_error "Cannot find DROP rule in VPN_IN chain"
        return 1
    fi

    # Insert rules before the final DROP
    # Add rules for both TCP and UDP (required for torrenting: TCP for peers, UDP for DHT/uTP)
    if $IPT_CMD -I VPN_IN "$drop_pos" -i pia0 -p tcp --dport "$port" -j ACCEPT -m comment --comment "port_forward_tcp" 2>/dev/null && \
       $IPT_CMD -I VPN_IN "$drop_pos" -i pia0 -p udp --dport "$port" -j ACCEPT -m comment --comment "port_forward_udp" 2>/dev/null; then
        show_debug "Port forwarding enabled: $port (TCP+UDP) at position $drop_pos"
    else
        show_error "Failed to add port forwarding rules for port $port"
        return 1
    fi
}

# Full cleanup
cleanup_killswitch() {
    show_debug "Full killswitch cleanup"
    cleanup_bypass_routes
    ks_cleanup

    # Remove killswitch flag file
    rm -f /tmp/killswitch_up
    show_debug "Removed /tmp/killswitch_up flag file"
}

# Show firewall statistics
show_ruleset_stats() {
    local ipv4_out_rules=$($IPT_CMD -L VPN_OUT -n 2>/dev/null | grep -c "^ACCEPT\|^DROP")
    local ipv4_in_rules=$($IPT_CMD -L VPN_IN -n 2>/dev/null | grep -c "^ACCEPT\|^DROP")
    local ipv6_out_rules=$($IP6T_CMD -L VPN_OUT6 -n 2>/dev/null | grep -c "^ACCEPT\|^DROP")
    local ipv6_in_rules=$($IP6T_CMD -L VPN_IN6 -n 2>/dev/null | grep -c "^ACCEPT\|^DROP")

    local total_rules=$((ipv4_out_rules + ipv4_in_rules + ipv6_out_rules + ipv6_in_rules))
    show_debug "iptables stats: $ipv4_out_rules IPv4 out, $ipv4_in_rules IPv4 in, $ipv6_out_rules IPv6 out, $ipv6_in_rules IPv6 in"
    show_success "Firewall: ${total_rules} rules, $IPT_CMD"
}

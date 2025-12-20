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
if nft list tables >/dev/null 2>&1; then
    USE_NFTABLES=true
    show_debug "Firewall backend: nftables"
else
    show_warning "nftables kernel support not available, falling back to iptables"
    show_debug "Firewall backend: iptables (fallback)"
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
# NFTABLES IMPLEMENTATION
#═══════════════════════════════════════════════════════════════════════════════

nft_setup_base_table() {
    show_debug "Setting up nftables base table: inet vpn_filter"
    nft add table inet vpn_filter 2>/dev/null || nft flush table inet vpn_filter
    show_debug "Creating output chain (priority 0, policy drop)"
    nft add chain inet vpn_filter output { type filter hook output priority 0 \; policy drop \; }
    show_debug "Creating input chain (priority 0, policy drop)"
    nft add chain inet vpn_filter input { type filter hook input priority 0 \; policy drop \; }
    show_debug "Creating forward chain (priority 0, policy drop)"
    nft add chain inet vpn_filter forward { type filter hook forward priority 0 \; policy drop \; }
}

nft_create_sets() {
    show_debug "Creating nftables sets for efficient lookups"
    nft add set inet vpn_filter local_nets_v4 { type ipv4_addr \; flags interval \; }
    nft add set inet vpn_filter local_nets_v6 { type ipv6_addr \; flags interval \; }
    show_debug "Created 2 sets: local_nets_v4, local_nets_v6"
}

nft_populate_local_networks() {
    local mode="$1"
    show_debug "Populating local network sets (mode: ${mode:-disabled})"
    
    if [ "$mode" = "all" ]; then
        show_debug "Adding all RFC1918 networks to local_nets_v4"
        nft add element inet vpn_filter local_nets_v4 { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 }
        show_debug "Adding local IPv6 networks to local_nets_v6"
        nft add element inet vpn_filter local_nets_v6 { fe80::/10, fc00::/7, ff00::/8 }
        show_success "Local network access: All RFC1918 networks"
    elif [ -n "$mode" ]; then
        local ipv4_list=""
        local ipv6_list=""
        
        IFS=',' read -ra NETWORKS <<< "$mode"
        for network in "${NETWORKS[@]}"; do
            network=$(echo "$network" | xargs)
            if [[ "$network" == *":"* ]]; then
                show_debug "Adding IPv6 network: $network"
                [ -n "$ipv6_list" ] && ipv6_list+=", "
                ipv6_list+="$network"
            else
                show_debug "Adding IPv4 network: $network"
                [ -n "$ipv4_list" ] && ipv4_list+=", "
                ipv4_list+="$network"
            fi
        done
        
        [ -n "$ipv4_list" ] && nft add element inet vpn_filter local_nets_v4 { $ipv4_list }
        [ -n "$ipv6_list" ] && nft add element inet vpn_filter local_nets_v6 { $ipv6_list }
        
        show_success "Local network access: $mode"
    else
        show_debug "No local networks configured"
        show_success "Local network access: Disabled (all traffic through VPN)"
    fi
}


# Apply baseline killswitch (everything blocked except local/VPN/exemptions)
nft_apply_baseline_killswitch() {
    show_step "Applying killswitch..."
    
    show_debug "Cleaning up any existing nftables configuration"
    nft_cleanup 2>/dev/null || true
    
    nft_setup_base_table
    nft_create_sets
    nft_populate_local_networks "${LOCAL_NETWORK:-}"
    
    # Build rules in optimal order (performance-optimized for active VPN)
    show_debug "Building nftables ruleset (optimal order for performance)"

    # 1. Loopback (very frequent)
    show_debug "Rule 1: Allow loopback (lo)"
    nft add rule inet vpn_filter output oifname "lo" accept

    # 2. Bypass routing destinations (WAN health checks via DAYTIME protocol)
    # MUST come before established/related to allow initial SYN packets!
    # Restricted to: eth0 interface + TCP + port 13 (DAYTIME) only
    # These are handled by routing table 100
    show_debug "Rule 2: Allow bypass routing destinations (WAN checks via TCP/13)"
    nft add rule inet vpn_filter output oifname "eth0" ip daddr { 129.6.15.28, 129.6.15.29, 132.163.96.1, 132.163.97.1, 128.138.140.44 } tcp dport 13 accept comment "bypass_routes"

    # 3. VPN interface (will be added by nft_add_vpn_interface() when VPN is up)
    # This will be the most-matched rule for active VPN traffic (~90% of packets)
    show_debug "Rule 3: VPN interface (will be added after VPN is up)"

    # 4. Established/Related (return packets from bypass routes, local nets, etc.)
    show_debug "Rule 4: Allow established/related connections"
    nft add rule inet vpn_filter output ct state established,related accept

    # 5. Local networks (if configured)
    if [ "${LOCAL_NETWORK:-}" = "all" ] || [ -n "${LOCAL_NETWORK:-}" ]; then
        show_debug "Rule 5: Allow local networks (from sets)"
        nft add rule inet vpn_filter output ip daddr @local_nets_v4 accept
        if [ "${DISABLE_IPV6}" != "true" ]; then
            show_debug "Rule 5b: Allow local IPv6 networks"
            nft add rule inet vpn_filter output ip6 daddr @local_nets_v6 accept
        fi
    else
        show_debug "Rule 5: Skipped (no local networks)"
    fi

    # 6. ICMPv6 (essential for IPv6 functionality when IPv6 is enabled)
    if [ "${DISABLE_IPV6}" != "true" ]; then
        show_debug "Rule 6: Allow ICMPv6 (Neighbor Discovery, Router Discovery, MTU, etc.)"
        nft add rule inet vpn_filter output meta l4proto ipv6-icmp accept
    fi

    # 7. Drop everything else (handled by policy drop, no explicit rule needed)
    
    # IPv6 protection
    if [ "${DISABLE_IPV6}" = "true" ]; then
        show_success "IPv6 completely blocked"
    else
        show_success "IPv6 routed through VPN only"
    fi

    # INPUT chain rules (policy drop - only allow specific traffic)
    show_debug "Building INPUT chain rules"

    # 1. Loopback (localhost access to proxy/metrics)
    show_debug "INPUT Rule 1: Allow loopback"
    nft add rule inet vpn_filter input iifname "lo" accept

    # 2. Established/Related (responses to our outgoing connections)
    show_debug "INPUT Rule 2: Allow established/related"
    nft add rule inet vpn_filter input ct state established,related accept

    # 3. Local networks (if configured - allows LAN access to proxy/metrics on specific ports only)
    if [ "${LOCAL_NETWORK:-}" = "all" ] || [ -n "${LOCAL_NETWORK:-}" ]; then
        show_debug "INPUT Rule 3: Allow from local networks to specific ports"

        # Build list of allowed ports
        local allowed_ports=""
        if [ "${PROXY_ENABLED}" = "true" ]; then
            allowed_ports="${SOCKS5_PORT:-1080}, ${HTTP_PROXY_PORT:-8888}"
        fi
        if [ "${METRICS}" = "true" ]; then
            [ -n "$allowed_ports" ] && allowed_ports+=", "
            allowed_ports+="${METRICS_PORT:-9090}"
        fi

        # Add user-specified LOCAL_PORTS (for dependent services like qBittorrent)
        if [ -n "${LOCAL_PORTS:-}" ]; then
            [ -n "$allowed_ports" ] && allowed_ports+=", "
            # Replace commas with ", " for nftables syntax
            allowed_ports+=$(echo "$LOCAL_PORTS" | tr ',' '\n' | xargs | tr ' ' ',')
        fi

        # Only add rule if there are ports to allow
        if [ -n "$allowed_ports" ]; then
            show_debug "Allowing LAN access to ports: $allowed_ports (TCP+UDP)"
            # Add both TCP and UDP rules (many services need both: DNS, Plex discovery, HTTP/3, etc.)
            nft add rule inet vpn_filter input ip saddr @local_nets_v4 tcp dport { $allowed_ports } accept
            nft add rule inet vpn_filter input ip saddr @local_nets_v4 udp dport { $allowed_ports } accept
            if [ "${DISABLE_IPV6}" != "true" ]; then
                show_debug "INPUT Rule 3b: Allow from local IPv6 networks to specific ports (TCP+UDP)"
                nft add rule inet vpn_filter input ip6 saddr @local_nets_v6 tcp dport { $allowed_ports } accept
                nft add rule inet vpn_filter input ip6 saddr @local_nets_v6 udp dport { $allowed_ports } accept
            fi
        else
            show_debug "No proxy/metrics/local ports configured, skipping LAN port rules"
        fi
    else
        show_debug "INPUT Rule 3: Skipped (no local networks)"
    fi

    # 4. ICMPv6 (essential for IPv6 functionality when IPv6 is enabled)
    if [ "${DISABLE_IPV6}" != "true" ]; then
        show_debug "INPUT Rule 4: Allow ICMPv6 (Neighbor Discovery, Router Discovery, MTU, etc.)"
        nft add rule inet vpn_filter input meta l4proto ipv6-icmp accept
    fi

    # 5. Port forwarding will be added by add_forwarded_port_to_input() when available
    show_debug "INPUT Rule 5: Port forwarding (will be added when port is allocated)"

    # Policy drop handles everything else (proxy/metrics NOT exposed to internet via VPN)

    show_debug "Baseline killswitch applied successfully"
}

# Add VPN interface to killswitch (called after VPN is up)
nft_add_vpn_interface() {
    show_debug "Adding VPN interface to killswitch"
    local fwmark=$(wg show pia fwmark 2>/dev/null)
    show_debug "VPN fwmark: ${fwmark:-off}"

    # Get handle of the established/related rule to insert VPN rules before it
    # This puts VPN rules at positions 3-4 (after lo and bypass_routes)
    local conntrack_handle=$(nft -a list chain inet vpn_filter output 2>/dev/null | grep "ct state" | sed -n 's/.*# handle \([0-9]*\).*/\1/p')

    if [ -z "$conntrack_handle" ]; then
        show_error "Cannot find conntrack rule in killswitch"
        show_debug "conntrack rule handle not found in nftables output chain"
        return 1
    fi

    show_debug "conntrack rule handle: $conntrack_handle"

    # Insert VPN interface rule first (most common - user traffic through tunnel)
    show_debug "Inserting VPN interface rule before conntrack (handle $conntrack_handle)"
    nft insert rule inet vpn_filter output handle "$conntrack_handle" oifname "pia" accept comment "vpn_interface"

    # Insert fwmark rule for WireGuard protocol packets (to VPN endpoint via eth0)
    if [ -n "$fwmark" ] && [ "$fwmark" != "off" ]; then
        show_debug "Inserting fwmark rule before conntrack (handle $conntrack_handle)"
        nft insert rule inet vpn_filter output handle "$conntrack_handle" mark "$fwmark" accept comment "vpn_fwmark"
    fi

    show_success "VPN added to killswitch (interface + fwmark)"
}

# Remove VPN interface from killswitch (called before VPN teardown)
nft_remove_vpn_interface() {
    show_debug "Removing VPN interface from killswitch"
    
    # Delete rules with "vpn_" comments
    local removed=0
    nft -a list chain inet vpn_filter output 2>/dev/null | grep "comment \"vpn_" | awk '{print $NF}' | while read handle; do
        show_debug "Removing VPN rule (handle: $handle)"
        nft delete rule inet vpn_filter output handle "$handle" 2>/dev/null || true
        removed=$((removed + 1))
    done
    
    show_debug "Removed $removed VPN rule(s) from killswitch"
}

# Add temporary exemption (surgical hole in firewall)
nft_add_exemption() {
    local ip="$1"
    local port="$2"
    local proto="$3"
    local comment="$4"

    show_debug "Adding temporary exemption: $ip:$port/$proto (tag: temp_$comment)"

    # Append rule to end of chain (will be evaluated before policy drop)
    nft add rule inet vpn_filter output ip daddr "$ip" "$proto" dport "$port" accept comment "temp_$comment" 2>/dev/null
}

# Remove temporary exemption
nft_remove_exemption() {
    local comment="$1"
    
    show_debug "Removing temporary exemption: temp_$comment"
    
    local removed=0
    nft -a list chain inet vpn_filter output 2>/dev/null | grep "comment \"temp_$comment\"" | awk '{print $NF}' | while read handle; do
        nft delete rule inet vpn_filter output handle "$handle" 2>/dev/null || true
        removed=$((removed + 1))
    done
    
    [ $removed -gt 0 ] && show_debug "Removed $removed exemption rule(s)"
}

# Remove all temporary exemptions
nft_remove_all_exemptions() {
    show_debug "Removing all temporary exemptions"

    local removed=0
    nft -a list chain inet vpn_filter output 2>/dev/null | grep "comment \"temp_" | awk '{print $NF}' | while read handle; do
        nft delete rule inet vpn_filter output handle "$handle" 2>/dev/null || true
        removed=$((removed + 1))
    done

    show_debug "Removed $removed temporary exemption(s)"
}

# Add forwarded port to INPUT chain (called when port is allocated)
nft_add_forwarded_port() {
    local port="$1"

    if [ -z "$port" ]; then
        show_debug "No port specified, skipping forwarded port rule"
        return 0
    fi

    show_debug "Adding forwarded port to INPUT: $port (TCP + UDP)"

    # Verify INPUT chain exists before trying to add rule
    if ! nft list chain inet vpn_filter input >/dev/null 2>&1; then
        show_error "INPUT chain not found in killswitch - cannot add port forwarding rule"
        return 1
    fi

    # Remove any existing forwarded port rules first
    nft_remove_forwarded_port

    # Add rules for both TCP and UDP (required for torrenting: TCP for peers, UDP for DHT/uTP)
    if nft add rule inet vpn_filter input iifname "pia" tcp dport "$port" accept comment "port_forward_tcp" 2>/dev/null && \
       nft add rule inet vpn_filter input iifname "pia" udp dport "$port" accept comment "port_forward_udp" 2>/dev/null; then
        show_debug "Port forwarding enabled: $port (TCP+UDP)"
    else
        show_error "Failed to add port forwarding rules for port $port"
        return 1
    fi
}

# Remove forwarded port from INPUT chain
nft_remove_forwarded_port() {
    show_debug "Removing forwarded port from INPUT"

    local removed=0
    # Remove both TCP and UDP rules (match both old "port_forward" and new "port_forward_tcp/udp" comments)
    nft -a list chain inet vpn_filter input 2>/dev/null | grep -E "comment \"port_forward" | awk '{print $NF}' | while read handle; do
        nft delete rule inet vpn_filter input handle "$handle" 2>/dev/null || true
        removed=$((removed + 1))
    done

    [ $removed -gt 0 ] && show_debug "Removed $removed port forwarding rule(s)"
}

nft_cleanup() {
    show_debug "Cleaning up nftables table: inet vpn_filter"
    nft delete table inet vpn_filter 2>/dev/null || true
}

#═══════════════════════════════════════════════════════════════════════════════
# IPTABLES FALLBACK IMPLEMENTATION
#═══════════════════════════════════════════════════════════════════════════════

ipt_add_fw_rule() {
    local chain="$1"
    shift
    
    if [ "$chain" = "VPN_OUT" ]; then
        show_debug "Adding iptables rule to VPN_OUT: $*"
        iptables -A VPN_OUT "$@"
    else
        show_debug "Adding ip6tables rule to VPN_OUT6: $*"
        ip6tables -A VPN_OUT6 "$@"
    fi
}

ipt_apply_local_network_rules() {
    local chain=$1
    local is_ipv6=$2
    
    show_debug "Applying local network rules (chain: $chain, ipv6: $is_ipv6)"
    
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


ipt_setup_input_chain() {
    show_debug "Setting up iptables INPUT chain (policy drop)"

    # Set up VPN_IN chain with DROP policy
    iptables -P INPUT ACCEPT 2>/dev/null || true
    iptables -F INPUT 2>/dev/null || true
    iptables -X VPN_IN 2>/dev/null || true
    iptables -N VPN_IN

    # 1. Loopback (localhost access to proxy/metrics)
    show_debug "INPUT Rule 1: Allow loopback"
    iptables -A VPN_IN -i lo -j ACCEPT

    # 2. Established/Related (responses to our outgoing connections)
    show_debug "INPUT Rule 2: Allow established/related"
    iptables -A VPN_IN -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

    # 3. Local networks (if configured - allows LAN access to proxy/metrics on specific ports only)
    if [ "${LOCAL_NETWORK:-}" = "all" ] || [ -n "${LOCAL_NETWORK:-}" ]; then
        show_debug "INPUT Rule 3: Allow from local networks to specific ports"

        # Build list of allowed ports
        local allowed_ports=""
        if [ "${PROXY_ENABLED}" = "true" ]; then
            allowed_ports="${SOCKS5_PORT:-1080},${HTTP_PROXY_PORT:-8888}"
        fi
        if [ "${METRICS}" = "true" ]; then
            [ -n "$allowed_ports" ] && allowed_ports+=","
            allowed_ports+="${METRICS_PORT:-9090}"
        fi

        # Add user-specified LOCAL_PORTS (for dependent services like qBittorrent)
        if [ -n "${LOCAL_PORTS:-}" ]; then
            [ -n "$allowed_ports" ] && allowed_ports+=","
            # Clean up LOCAL_PORTS (remove spaces, ensure comma-separated)
            allowed_ports+=$(echo "$LOCAL_PORTS" | tr -d ' ')
        fi

        # Only add rules if there are ports to allow
        if [ -n "$allowed_ports" ]; then
            show_debug "Allowing LAN access to ports: $allowed_ports (TCP+UDP)"
            if [ "${LOCAL_NETWORK:-}" = "all" ]; then
                # TCP rules
                iptables -A VPN_IN -s 10.0.0.0/8 -p tcp -m multiport --dports "$allowed_ports" -j ACCEPT
                iptables -A VPN_IN -s 172.16.0.0/12 -p tcp -m multiport --dports "$allowed_ports" -j ACCEPT
                iptables -A VPN_IN -s 192.168.0.0/16 -p tcp -m multiport --dports "$allowed_ports" -j ACCEPT
                iptables -A VPN_IN -s 169.254.0.0/16 -p tcp -m multiport --dports "$allowed_ports" -j ACCEPT
                # UDP rules (for DNS, Plex discovery, HTTP/3, etc.)
                iptables -A VPN_IN -s 10.0.0.0/8 -p udp -m multiport --dports "$allowed_ports" -j ACCEPT
                iptables -A VPN_IN -s 172.16.0.0/12 -p udp -m multiport --dports "$allowed_ports" -j ACCEPT
                iptables -A VPN_IN -s 192.168.0.0/16 -p udp -m multiport --dports "$allowed_ports" -j ACCEPT
                iptables -A VPN_IN -s 169.254.0.0/16 -p udp -m multiport --dports "$allowed_ports" -j ACCEPT
            else
                IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORK"
                for network in "${NETWORKS[@]}"; do
                    network=$(echo "$network" | xargs)
                    if [[ "$network" != *":"* ]]; then
                        iptables -A VPN_IN -s "$network" -p tcp -m multiport --dports "$allowed_ports" -j ACCEPT
                        iptables -A VPN_IN -s "$network" -p udp -m multiport --dports "$allowed_ports" -j ACCEPT
                    fi
                done
            fi
        else
            show_debug "No proxy/metrics/local ports configured, skipping LAN port rules"
        fi
    else
        show_debug "INPUT Rule 3: Skipped (no local networks)"
    fi

    # 4. Port forwarding will be added by ipt_add_forwarded_port() when available
    show_debug "INPUT Rule 4: Port forwarding (will be added when port is allocated)"

    # DROP everything else
    show_debug "Adding final INPUT DROP rule"
    iptables -A VPN_IN -j DROP

    # Insert into INPUT chain
    show_debug "Inserting VPN_IN into INPUT chain"
    iptables -I INPUT 1 -j VPN_IN
}

ipt_setup_ipv6_protection() {
    show_debug "Setting up IPv6 protection"

    ip6tables -F OUTPUT 2>/dev/null || true
    ip6tables -F FORWARD 2>/dev/null || true
    ip6tables -F INPUT 2>/dev/null || true
    ip6tables -X VPN_IN6 2>/dev/null || true
    ip6tables -X VPN_OUT6 2>/dev/null || true

    # IPv6 INPUT chain
    ip6tables -N VPN_IN6 2>/dev/null || ip6tables -F VPN_IN6
    ip6tables -A VPN_IN6 -i lo -j ACCEPT
    ip6tables -A VPN_IN6 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

    # Allow ICMPv6 (essential for IPv6 functionality)
    if [ "${DISABLE_IPV6}" != "true" ]; then
        show_debug "Adding ICMPv6 rule to VPN_IN6"
        ip6tables -A VPN_IN6 -p ipv6-icmp -j ACCEPT
    fi

    # Build list of allowed ports for IPv6 (same as IPv4)
    if [ "${LOCAL_NETWORK:-}" = "all" ] || [ -n "${LOCAL_NETWORK:-}" ]; then
        local allowed_ports=""
        if [ "${PROXY_ENABLED}" = "true" ]; then
            allowed_ports="${SOCKS5_PORT:-1080},${HTTP_PROXY_PORT:-8888}"
        fi
        if [ "${METRICS}" = "true" ]; then
            [ -n "$allowed_ports" ] && allowed_ports+=","
            allowed_ports+="${METRICS_PORT:-9090}"
        fi

        # Add user-specified LOCAL_PORTS (for dependent services like qBittorrent)
        if [ -n "${LOCAL_PORTS:-}" ]; then
            [ -n "$allowed_ports" ] && allowed_ports+=","
            allowed_ports+=$(echo "$LOCAL_PORTS" | tr -d ' ')
        fi

        if [ -n "$allowed_ports" ]; then
            if [ "${LOCAL_NETWORK:-}" = "all" ]; then
                # TCP rules
                ip6tables -A VPN_IN6 -s fe80::/10 -p tcp -m multiport --dports "$allowed_ports" -j ACCEPT
                ip6tables -A VPN_IN6 -s fc00::/7 -p tcp -m multiport --dports "$allowed_ports" -j ACCEPT
                # UDP rules (for DNS, Plex discovery, HTTP/3, etc.)
                ip6tables -A VPN_IN6 -s fe80::/10 -p udp -m multiport --dports "$allowed_ports" -j ACCEPT
                ip6tables -A VPN_IN6 -s fc00::/7 -p udp -m multiport --dports "$allowed_ports" -j ACCEPT
            else
                IFS=',' read -ra NETWORKS <<< "$LOCAL_NETWORK"
                for network in "${NETWORKS[@]}"; do
                    network=$(echo "$network" | xargs)
                    if [[ "$network" == *":"* ]]; then
                        ip6tables -A VPN_IN6 -s "$network" -p tcp -m multiport --dports "$allowed_ports" -j ACCEPT
                        ip6tables -A VPN_IN6 -s "$network" -p udp -m multiport --dports "$allowed_ports" -j ACCEPT
                    fi
                done
            fi
        fi
    fi

    ip6tables -A VPN_IN6 -j DROP
    ip6tables -I INPUT 1 -j VPN_IN6

    # IPv6 OUTPUT chain
    ip6tables -N VPN_OUT6 2>/dev/null || ip6tables -F VPN_OUT6

    ipt_add_fw_rule "VPN_OUT6" -o lo -j ACCEPT
    ipt_add_fw_rule "VPN_OUT6" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

    ipt_apply_local_network_rules "VPN_OUT6" "true"

    # Allow ICMPv6 (essential for IPv6 functionality)
    if [ "${DISABLE_IPV6}" != "true" ]; then
        show_debug "Adding ICMPv6 rule to VPN_OUT6"
        ipt_add_fw_rule "VPN_OUT6" -p ipv6-icmp -j ACCEPT
        ipt_add_fw_rule "VPN_OUT6" -o pia -j ACCEPT
    fi

    ipt_add_fw_rule "VPN_OUT6" -j DROP
    ip6tables -I OUTPUT 1 -j VPN_OUT6

    [ "${DISABLE_IPV6}" = "true" ] && show_success "IPv6 completely blocked" || \
        show_success "IPv6 routed through VPN only"
}

ipt_setup_iptables_chain() {
    show_debug "Setting up iptables VPN_OUT chain"
    
    iptables -P OUTPUT ACCEPT 2>/dev/null || true
    iptables -F OUTPUT 2>/dev/null || true
    iptables -X VPN_OUT 2>/dev/null || true
    iptables -N VPN_OUT
}

ipt_add_standard_rules() {
    local include_vpn="${1:-false}"
    
    show_debug "Adding standard iptables rules (include_vpn: $include_vpn)"
    
    ipt_add_fw_rule "VPN_OUT" -o lo -j ACCEPT
    
    # Bypass routing destinations MUST come before established/related
    # to allow initial SYN packets!
    # Restricted to: eth0 interface + TCP + port 13 (DAYTIME) only
    show_debug "Adding bypass route rules (TCP/13 via eth0)"
    ipt_add_fw_rule "VPN_OUT" -o eth0 -p tcp --dport 13 -d 129.6.15.28 -j ACCEPT -m comment --comment "bypass_routes"
    ipt_add_fw_rule "VPN_OUT" -o eth0 -p tcp --dport 13 -d 129.6.15.29 -j ACCEPT -m comment --comment "bypass_routes"
    ipt_add_fw_rule "VPN_OUT" -o eth0 -p tcp --dport 13 -d 132.163.96.1 -j ACCEPT -m comment --comment "bypass_routes"
    ipt_add_fw_rule "VPN_OUT" -o eth0 -p tcp --dport 13 -d 132.163.97.1 -j ACCEPT -m comment --comment "bypass_routes"
    ipt_add_fw_rule "VPN_OUT" -o eth0 -p tcp --dport 13 -d 128.138.140.44 -j ACCEPT -m comment --comment "bypass_routes"
    
    ipt_add_fw_rule "VPN_OUT" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
    
    ipt_apply_local_network_rules "VPN_OUT" "false"
    
    if [ "$include_vpn" = "true" ]; then
        local fwmark=$(wg show pia fwmark 2>/dev/null)
        show_debug "VPN fwmark: ${fwmark:-off}"
        
        if [ -n "$fwmark" ] && [ "$fwmark" != "off" ]; then
            ipt_add_fw_rule "VPN_OUT" -m mark --mark "$fwmark" -j ACCEPT -m comment --comment "vpn_fwmark"
            show_success "VPN added to killswitch (fwmark: $fwmark)"
        else
            show_success "VPN added to killswitch (interface-based)"
        fi
        ipt_add_fw_rule "VPN_OUT" -o pia -j ACCEPT -m comment --comment "vpn_interface"
    fi
}

ipt_apply_baseline_killswitch() {
    show_step "Applying baseline killswitch..."

    show_debug "Cleaning up existing iptables configuration"
    ipt_cleanup 2>/dev/null || true

    ipt_setup_ipv6_protection
    ipt_setup_input_chain
    ipt_setup_iptables_chain
    ipt_add_standard_rules "false"

    show_debug "Adding final DROP rule"
    ipt_add_fw_rule "VPN_OUT" -j DROP

    show_debug "Inserting VPN_OUT into OUTPUT chain"
    iptables -I OUTPUT 1 -j VPN_OUT
}

ipt_add_vpn_interface() {
    show_debug "Adding VPN interface to iptables killswitch"
    
    # Remove DROP rule temporarily
    show_debug "Temporarily removing DROP rule"
    iptables -D VPN_OUT -j DROP 2>/dev/null || true
    
    # Add VPN rules
    local fwmark=$(wg show pia fwmark 2>/dev/null)
    show_debug "VPN fwmark: ${fwmark:-off}"
    
    if [ -n "$fwmark" ] && [ "$fwmark" != "off" ]; then
        ipt_add_fw_rule "VPN_OUT" -m mark --mark "$fwmark" -j ACCEPT -m comment --comment "vpn_fwmark"
        show_success "VPN added to killswitch (fwmark: $fwmark)"
    else
        show_success "VPN added to killswitch (interface-based)"
    fi
    ipt_add_fw_rule "VPN_OUT" -o pia -j ACCEPT -m comment --comment "vpn_interface"
    
    # Re-add DROP rule at end
    show_debug "Re-adding DROP rule"
    ipt_add_fw_rule "VPN_OUT" -j DROP
    
    # Also update IPv6
    if [ "${DISABLE_IPV6}" != "true" ]; then
        show_debug "Updating IPv6 rules for VPN"
        ip6tables -D VPN_OUT6 -j DROP 2>/dev/null || true
        ipt_add_fw_rule "VPN_OUT6" -p ipv6-icmp -j ACCEPT
        ipt_add_fw_rule "VPN_OUT6" -o pia -j ACCEPT
        ipt_add_fw_rule "VPN_OUT6" -j DROP
    fi
}

ipt_remove_vpn_interface() {
    show_debug "Removing VPN interface from iptables killswitch"

    # Remove VPN-related rules
    iptables -D VPN_OUT -m comment --comment "vpn_fwmark" -j ACCEPT 2>/dev/null || true
    iptables -D VPN_OUT -m comment --comment "vpn_interface" -j ACCEPT 2>/dev/null || true

    ip6tables -D VPN_OUT6 -o pia -j ACCEPT 2>/dev/null || true
}

# Add forwarded port to INPUT chain (called when port is allocated)
ipt_add_forwarded_port() {
    local port="$1"

    if [ -z "$port" ]; then
        show_debug "No port specified, skipping forwarded port rule"
        return 0
    fi

    show_debug "Adding forwarded port to INPUT: $port (TCP + UDP)"

    # Verify VPN_IN chain exists before trying to add rule
    if ! iptables -L VPN_IN -n >/dev/null 2>&1; then
        show_error "VPN_IN chain not found in killswitch - cannot add port forwarding rule"
        return 1
    fi

    # Remove any existing forwarded port rules first
    ipt_remove_forwarded_port

    # Insert rules before the final DROP (position 4)
    # Add rules for both TCP and UDP (required for torrenting: TCP for peers, UDP for DHT/uTP)
    if iptables -I VPN_IN 4 -i pia -p tcp --dport "$port" -j ACCEPT -m comment --comment "port_forward_tcp" 2>/dev/null && \
       iptables -I VPN_IN 4 -i pia -p udp --dport "$port" -j ACCEPT -m comment --comment "port_forward_udp" 2>/dev/null; then
        show_debug "Port forwarding enabled: $port (TCP+UDP)"
    else
        show_error "Failed to add port forwarding rules for port $port"
        return 1
    fi
}

# Remove forwarded port from INPUT chain
ipt_remove_forwarded_port() {
    show_debug "Removing forwarded port from INPUT"

    # Remove both TCP and UDP rules (handle both old "port_forward" and new "port_forward_tcp/udp" comments)
    iptables -D VPN_IN -m comment --comment "port_forward_tcp" -j ACCEPT 2>/dev/null || true
    iptables -D VPN_IN -m comment --comment "port_forward_udp" -j ACCEPT 2>/dev/null || true
    iptables -D VPN_IN -m comment --comment "port_forward" -j ACCEPT 2>/dev/null || true
}

ipt_add_exemption() {
    local ip="$1"
    local port="$2"
    local proto="$3"
    local comment="$4"
    
    show_debug "Adding temporary iptables exemption: $ip:$port/$proto (tag: temp_$comment)"
    
    # Insert near top (after established/related)
    iptables -I VPN_OUT 3 -d "$ip" -p "$proto" --dport "$port" -j ACCEPT -m comment --comment "temp_$comment"
}

ipt_remove_exemption() {
    local comment="$1"
    
    show_debug "Removing temporary iptables exemption: temp_$comment"
    
    iptables -D VPN_OUT -m comment --comment "temp_$comment" -j ACCEPT 2>/dev/null || true
}

ipt_remove_all_exemptions() {
    show_debug "Removing all temporary iptables exemptions"
    
    # Remove all rules with "temp_" in comment
    local removed=0
    iptables -S VPN_OUT | grep "temp_" | sed 's/-A/-D/' | while read rule; do
        iptables $rule 2>/dev/null || true
        removed=$((removed + 1))
    done
    
    show_debug "Removed $removed temporary exemption(s)"
}

ipt_cleanup() {
    show_debug "Cleaning up iptables configuration"

    # OUTPUT chains
    iptables -D OUTPUT -j VPN_OUT 2>/dev/null || true
    iptables -F VPN_OUT 2>/dev/null || true
    iptables -X VPN_OUT 2>/dev/null || true
    ip6tables -D OUTPUT -j VPN_OUT6 2>/dev/null || true
    ip6tables -F VPN_OUT6 2>/dev/null || true
    ip6tables -X VPN_OUT6 2>/dev/null || true

    # INPUT chains
    iptables -D INPUT -j VPN_IN 2>/dev/null || true
    iptables -F VPN_IN 2>/dev/null || true
    iptables -X VPN_IN 2>/dev/null || true
    ip6tables -D INPUT -j VPN_IN6 2>/dev/null || true
    ip6tables -F VPN_IN6 2>/dev/null || true
    ip6tables -X VPN_IN6 2>/dev/null || true
}

#═══════════════════════════════════════════════════════════════════════════════
# UNIFIED PUBLIC API
#═══════════════════════════════════════════════════════════════════════════════

# Verify baseline killswitch is actually active (CRITICAL for leak prevention)
verify_baseline_killswitch() {
    show_debug "Verifying baseline killswitch is active"

    if $USE_NFTABLES; then
        # Check that output chain exists with policy drop
        if ! nft list chain inet vpn_filter output 2>/dev/null | grep -q "policy drop"; then
            show_error "Killswitch verification failed: OUTPUT chain policy is not drop"
            return 1
        fi

        # Check that bypass routes are allowed (WAN health checks)
        if ! nft list chain inet vpn_filter output 2>/dev/null | grep -q "bypass_routes"; then
            show_error "Killswitch verification failed: Bypass routes not found"
            return 1
        fi

        # Check that loopback is allowed
        if ! nft list chain inet vpn_filter output 2>/dev/null | grep -q "oifname \"lo\""; then
            show_error "Killswitch verification failed: Loopback rule not found"
            return 1
        fi
    else
        # Check that VPN_OUT chain exists and has DROP rule
        if ! iptables -L VPN_OUT 2>/dev/null | grep -q "DROP"; then
            show_error "Killswitch verification failed: No DROP rule found in iptables"
            return 1
        fi

        # Check that VPN_OUT is actually in OUTPUT chain
        if ! iptables -L OUTPUT 2>/dev/null | grep -q "VPN_OUT"; then
            show_error "Killswitch verification failed: VPN_OUT not in OUTPUT chain"
            return 1
        fi
    fi

    show_debug "Baseline killswitch verification passed"
    return 0
}

# Verify VPN interface is allowed in killswitch
verify_vpn_in_killswitch() {
    show_debug "Verifying VPN is allowed in killswitch"

    if $USE_NFTABLES; then
        if ! nft list chain inet vpn_filter output 2>/dev/null | grep -q "vpn_interface"; then
            show_error "VPN interface rule not found in killswitch"
            return 1
        fi
    else
        if ! iptables -L VPN_OUT 2>/dev/null | grep -q "vpn_interface"; then
            show_error "VPN interface rule not found in killswitch"
            return 1
        fi
    fi

    show_debug "VPN killswitch verification passed"
    return 0
}

# Setup baseline killswitch + bypass routes (called once at startup)
setup_baseline_killswitch() {
    show_debug "Setting up baseline killswitch"

    # Remove any stale flag file
    rm -f /tmp/killswitch_up

    # DEFENSIVE: Clean up any orphaned rules from previous container runs
    # This handles cases where cleanup didn't run due to crash, OOM kill, etc.
    # Critical for Kubernetes where network namespaces may be reused
    show_debug "Cleaning up any orphaned killswitch rules from previous runs"
    cleanup_killswitch 2>/dev/null || true

    setup_bypass_routes || {
        show_error "Failed to setup bypass routes"
        return 1
    }

    if $USE_NFTABLES; then
        nft_apply_baseline_killswitch || {
            show_error "Failed to apply nftables killswitch"
            return 1
        }
    else
        ipt_apply_baseline_killswitch || {
            show_error "Failed to apply iptables killswitch"
            return 1
        }
    fi

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
    if $USE_NFTABLES; then
        nft_add_vpn_interface || {
            show_error "Failed to add VPN to nftables killswitch"
            return 1
        }
    else
        ipt_add_vpn_interface || {
            show_error "Failed to add VPN to iptables killswitch"
            return 1
        }
    fi

    # CRITICAL: Verify VPN was actually added to killswitch
    verify_vpn_in_killswitch || {
        show_error "VPN killswitch verification failed - VPN may not be protected"
        return 1
    }

    return 0
}

# Remove VPN interface from killswitch (called before VPN teardown)
remove_vpn_from_killswitch() {
    show_debug "Removing VPN from killswitch (backend: ${USE_NFTABLES})"
    
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
    show_debug "Removing all temporary exemptions"

    if $USE_NFTABLES; then
        nft_remove_all_exemptions
    else
        ipt_remove_all_exemptions
    fi
}

# Add forwarded port to INPUT chain (called when port forwarding is enabled)
add_forwarded_port_to_input() {
    local port="$1"

    if $USE_NFTABLES; then
        nft_add_forwarded_port "$port"
    else
        ipt_add_forwarded_port "$port"
    fi
}

# Remove forwarded port from INPUT chain (called when port changes or forwarding disabled)
remove_forwarded_port_from_input() {
    if $USE_NFTABLES; then
        nft_remove_forwarded_port
    else
        ipt_remove_forwarded_port
    fi
}

# Full cleanup
cleanup_killswitch() {
    show_debug "Full killswitch cleanup"
    cleanup_bypass_routes

    if $USE_NFTABLES; then
        nft_cleanup
    else
        ipt_cleanup
    fi

    # Remove killswitch flag file
    rm -f /tmp/killswitch_up
    show_debug "Removed /tmp/killswitch_up flag file"
}

# Show firewall statistics
show_ruleset_stats() {
    if $USE_NFTABLES; then
        local output_rules=$(nft list chain inet vpn_filter output 2>/dev/null | grep -c "^\s*\(oifname\|ct state\|ip daddr\|ip6 daddr\|mark\|tcp dport\|udp dport\|drop\|accept\)")
        local input_rules=$(nft list chain inet vpn_filter input 2>/dev/null | grep -c "^\s*\(iifname\|ct state\|ip saddr\|ip6 saddr\|tcp dport\)")
        local set_elements=0
        for set_name in local_nets_v4 local_nets_v6; do
            local count=$(nft list set inet vpn_filter "$set_name" 2>/dev/null | grep -c "elements" | head -1)
            [ -n "$count" ] && [ "$count" -gt 0 ] 2>/dev/null && set_elements=$((set_elements + 1))
        done

        local total_rules=$((output_rules + input_rules))
        show_debug "nftables stats: $output_rules output rules, $input_rules input rules, $set_elements sets"
        show_success "Firewall: ${total_rules} rules, ${set_elements} sets, nftables"
    else
        local ipv4_out_rules=$(iptables -L VPN_OUT 2>/dev/null | grep -c "^ACCEPT\|^DROP")
        local ipv4_in_rules=$(iptables -L VPN_IN 2>/dev/null | grep -c "^ACCEPT\|^DROP")
        local ipv6_out_rules=$(ip6tables -L VPN_OUT6 2>/dev/null | grep -c "^ACCEPT\|^DROP")
        local ipv6_in_rules=$(ip6tables -L VPN_IN6 2>/dev/null | grep -c "^ACCEPT\|^DROP")

        local total_rules=$((ipv4_out_rules + ipv4_in_rules + ipv6_out_rules + ipv6_in_rules))
        show_debug "iptables stats: $ipv4_out_rules IPv4 out, $ipv4_in_rules IPv4 in, $ipv6_out_rules IPv6 out, $ipv6_in_rules IPv6 in"
        show_success "Firewall: ${total_rules} rules, iptables"
    fi
}
#!/bin/bash
# Integration test suite for pia-tun
# Tests: VPN connection, killswitch, reconnection, port forwarding, proxy, metrics, and more
#
# Requirements: docker, jq, curl
# Usage: PIA_USER=xxx PIA_PASS=xxx ./testing/test.sh

set -euo pipefail

# Colors
RED='\033[0;31m'
GRN='\033[0;32m'
YLW='\033[0;33m'
BLU='\033[0;34m'
CYN='\033[0;36m'
BLD='\033[1m'
NC='\033[0m'

# Configuration
CONTAINER="pia-tun-test"
IMAGE_NAME="${IMAGE_NAME:-pia-tun:test}"

# Env
PIA_LOCATION="${PIA_LOCATION:-ca_ontario}"
METRICS_PORT=9091
METRICS_URL="http://localhost:$METRICS_PORT"
DNS="${DNS:-pia}"
PORT_FILE="${PORT_FILE:-/port}"
PROXY_ENABLED="${PROXY_ENABLED:-true}"
SOCKS5_PORT="${SOCKS5_PORT:-1081}"
HTTP_PROXY_PORT="${HTTP_PROXY_PORT:-8889}"
PROXY_USER="user"
PROXY_PASS="pass"
HC_INTERVAL="${HC_INTERVAL:-4}"
HC_FAILURE_WINDOW="${HC_FAILURE_WINDOW:-4}"

# Internals
HC_TIMEOUT=3

# Timeouts (seconds)
CONNECT_TIMEOUT=15
READY_TIMEOUT=10
MAX_DOWNTIME="$(( HC_INTERVAL + HC_FAILURE_WINDOW + HC_TIMEOUT ))"

# Counters
PASSED=0
FAILED=0
SKIPPED=0

# ============================================================================
# Test helpers
# ============================================================================

pass() { echo -e "  ${GRN}✓${NC} $1"; PASSED=$((PASSED + 1)); }
fail() { echo -e "  ${RED}✗${NC} $1"; FAILED=$((FAILED + 1)); }
skip() { echo -e "  ${YLW}↷${NC} $1"; SKIPPED=$((SKIPPED + 1)); }
step() { echo -e "\n${BLU}▶${NC} $1"; }
info() { echo -e "    $1"; }

# Get a prometheus metric value by name prefix
get_metric() {
    curl -sf "$METRICS_URL/metrics" 2>/dev/null | grep "^$1" | grep -v '#' | head -1 | awk '{print $2}' || true
}

# Get a JSON metric field
get_json() {
    curl -sf "$METRICS_URL/metrics?format=json" 2>/dev/null | jq -r ".$1 // empty"
}

# Run a command inside the container
dexec() {
    docker exec "$CONTAINER" "$@" 2>/dev/null
}

# Wait for a condition with timeout. Usage: wait_for <timeout> <description> <command...>
wait_for() {
    local timeout=$1 desc=$2; shift 2
    local max=$(( timeout * 10 ))
    for i in $(seq 1 "$max"); do
        if "$@" >/dev/null 2>&1; then
            info "$desc ($(awk "BEGIN{printf \"%.1f\", $i/10}")s)"
            return 0
        fi
        # Check container is still running
        if ! docker ps -q -f name="$CONTAINER" | grep -q .; then
            fail "$desc — container exited"
            return 1
        fi
        sleep 0.1
    done
    fail "$desc — timed out after ${timeout}s"
    return 1
}

# ============================================================================
# Setup / teardown
# ============================================================================

cleanup() {
    local rc=$?
    if [ $rc -ne 0 ] && [ "$PASSED" -eq 0 ] && [ "$FAILED" -eq 0 ]; then
        echo -e "\n  ${RED}✗${NC} Script exited unexpectedly (exit code $rc)"
        echo "    Last command failed — check output above"
    fi
    step "Cleanup"
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    exit $rc
}
trap cleanup EXIT

print_banner() {
    echo ""
    echo -e "${CYN}╔════════════════════════════════════════════════╗${NC}"
    echo -e "${CYN}║                                                ║${NC}"
    echo -e "${CYN}║               ${NC}pia-tun Test Suite${CYN}               ║${NC}"
    echo -e "${CYN}║                                                ║${NC}"
    echo -e "${CYN}╚════════════════════════════════════════════════╝${NC}"
}

check_prerequisites() {
    step "Prerequisites"
    for cmd in docker jq curl; do
        command -v "$cmd" >/dev/null 2>&1 || { fail "$cmd not installed"; exit 1; }
    done
    pass "Tools available (docker, jq, curl)"

    if [ -z "${PIA_USER:-}" ] || [ -z "${PIA_PASS:-}" ]; then
        fail "PIA_USER and PIA_PASS required"
        exit 1
    fi
    pass "PIA credentials set"
}

build() {
    step "Build"
    docker build -t "$IMAGE_NAME" -q . >/dev/null
    pass "Docker image built"
}

start_container() {
    step "Start container"
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true

    docker run -d \
        --name "$CONTAINER" \
        --cap-add=NET_ADMIN \
        --cap-drop=ALL \
        -p $METRICS_PORT:$METRICS_PORT \
        -e PIA_USER="$PIA_USER" \
        -e PIA_PASS="$PIA_PASS" \
        -e PIA_LOCATION="$PIA_LOCATION" \
        -e DNS="$DNS" \
        -e PF_ENABLED=true \
        -e PORT_FILE="$PORT_FILE" \
        -e LOCAL_NETWORKS="all" \
        -e PROXY_ENABLED=true \
        -e SOCKS5_PORT=$SOCKS5_PORT \
        -e HTTP_PROXY_PORT=$HTTP_PROXY_PORT \
        -e PROXY_USER="$PROXY_USER" \
        -e PROXY_PASS="$PROXY_PASS" \
        -e METRICS_ENABLED=true \
        -e METRICS_PORT=$METRICS_PORT \
        -e INSTANCE_NAME=test \
        -e HC_INTERVAL=$HC_INTERVAL \
        -e HC_FAILURE_WINDOW=$HC_FAILURE_WINDOW \
        -e LOG_LEVEL=trace \
        "$IMAGE_NAME" >/dev/null

    pass "Container started (HC_INTERVAL=${HC_INTERVAL}s, HC_FAILURE_WINDOW=${HC_FAILURE_WINDOW}s)"
}

# ============================================================================
# Wait phases
# ============================================================================

wait_for_ready() {
    step "Waiting for ready..."
    wait_for "$READY_TIMEOUT" "Ready endpoint reports ready" \
        curl -sf "$METRICS_URL/ready"
}

wait_for_healthy() {
    step "Waiting for VPN connection..."
    wait_for "$CONNECT_TIMEOUT" "Health endpoint reports healthy" \
        curl -sf "$METRICS_URL/health"
}

pf_active() {
    [ "$(get_metric pia_tun_port_forwarding_active)" != "0" ]
}

wan_down() {
    [ "$(get_metric pia_tun_wan_up)" != "1" ]
}

wait_for_port_forward() {
    step "Waiting for port forwarding..."
    wait_for 10 "Port forwarded" pf_active
}

wait_for_wan_down() {
    info "Waiting for wan down..."
    wait_for 10 "WAN down" wan_down
}

wait_for_unhealthy() {
    info "Waiting for unhealthy..."
    wait_for "$(( MAX_DOWNTIME + 5 ))" "Health endpoint reports unhealthy" \
        bash -c "! curl -sf $METRICS_URL/health >/dev/null 2>&1"
}

# ============================================================================
# Tests
# ============================================================================

dump_firewall_state() {
    local state=$1

    step "$state state"
    info "iptables --list-rules"
    dexec iptables --list-rules 2>/dev/null | sed 's/^/      /'
    if [ "$state" == "WAN-down" ]; then
        info "^^ Removed bypass_routes from VPN_OUT chain for this test to function"
    fi
    echo ""
    info "ip route show table 51820"
    dexec ip route show table 51820 2>/dev/null | sed 's/^/      /'
    echo ""
    info "ip rule list"
    dexec ip rule list 2>/dev/null | sed 's/^/      /'
}

test_ip_changed() {
    step "Test: IP changed"

    local real_ip vpn_ip
    real_ip=$(curl -sf -4 --max-time 10 ifconfig.me || true)
    vpn_ip=$(get_json current_ip)

    if [ -z "$real_ip" ]; then
        skip "Could not determine host IP"
        return 0
    fi

    if [ -z "$vpn_ip" ]; then
        fail "No VPN IP in metrics"
        return 1
    fi

    if [ "$vpn_ip" = "$real_ip" ]; then
        fail "VPN IP matches real IP ($real_ip) — possible leak"
        return 1
    fi

    pass "IP changed: $real_ip → $vpn_ip"
}

test_dns_leak() {
    # DNS leak test: PIA resolution should fail when VPN is down
    if [ "$DNS" = "pia" ]; then
        info "Testing DNS resolution (should fail)..."
        if dexec nslookup -timeout=3 example.com >/dev/null 2>&1; then
            fail "PIA DNS resolves with VPN down (leak via LOCAL_NETWORKS)"
        else
            pass "DNS blocked with VPN down"
        fi
    else
        skip "DNS leak test (DNS != pia)"
    fi
}

test_connection_metrics() {
    step "Test: Connection metrics"

    local conn_up wan_up ks_active server_checks checks server
    conn_up=$(get_metric "pia_tun_connection_up")
    wan_up=$(get_metric "pia_tun_wan_up")
    ks_active=$(get_metric "pia_tun_killswitch_active")
    server_checks=$(get_metric "pia_tun_server_latency_seconds_count")
    checks=$(get_json "health_checks_total")
    server=$(get_json "current_server")

    [ "$conn_up" = "1" ] && pass "connection_up = 1" || fail "connection_up = $conn_up"
    [ "$wan_up" = "1" ] && pass "wan_up = 1" || fail "wan_up = $wan_up"
    [ "$ks_active" = "1" ] && pass "killswitch_active = 1" || fail "killswitch_active = $ks_active"
    [ "${server_checks:-0}" -gt 0 ] && pass "server_latency > 0" || fail "server_latency_seconds_count = $server_checks"
    [ "${checks:-0}" -gt 0 ] && pass "Health checks running ($checks total)" || fail "No health checks recorded"
    [ -n "$server" ] && pass "Connected to $server" || fail "No server in metrics"
}

test_port_forwarding() {
    local state=$1

    if [ "$state" != "post-reconnect" ]; then
        step "Test: Port forwarding"
    else
        step "Test: Port forwarding ($state)"
    fi

    local port pf_active
    port=$(get_metric "pia_tun_port_forwarding_port")
    pf_active=$(get_metric "pia_tun_port_forwarding_active")

    [ "$pf_active" = "1" ] && pass "Port forwarding active" || fail "Port forwarding not active"

    if [ -n "$port" ] && [ "$port" != "0" ]; then
        pass "Forwarded port: $port"
    else
        fail "No forwarded port"
    fi

    # Verify port file matches
    local file_port
    file_port=$(dexec cat $PORT_FILE 2>/dev/null || echo "")
    if [ "$file_port" = "$port" ]; then
        pass "Port file matches metric ($file_port)"
    else
        fail "Port file ($file_port) doesn't match metric ($port)"
    fi

    # Verify iptables ACCEPT rules exist for forwarded port
    if [ -n "$port" ] && [ "$port" != "0" ]; then
        local rules
        rules=$(dexec iptables -L VPN_IN -n 2>/dev/null || true)
        if echo "$rules" | grep -q "port_forward_tcp" && echo "$rules" | grep -q "port_forward_udp"; then
            pass "Firewall rules for port $port (tcp+udp)"
        else
            fail "Missing firewall rules for forwarded port"
        fi
    fi
}

test_ipv6_blocked() {
    step "Test: IPv6 DROP rule"

    if dexec ip6tables -L VPN_OUT6 -n 2>/dev/null | grep -q DROP; then
        pass "IPv6 DROP rule in VPN_OUT6"
    else
        fail "No IPv6 DROP rule"
    fi

    # No non-local IPv6 routes
    local v6_routes
    v6_routes=$(dexec ip -6 route show 2>/dev/null | grep -v "^::1" | grep -v "^fe80" | wc -l || true)
    [ "$v6_routes" -eq 0 ] && pass "No global IPv6 routes" || fail "Found $v6_routes IPv6 routes"
}

test_killswitch() {
    step "Test: Killswitch and reconnect"

    local reconnects_before
    reconnects_before=$(get_metric "pia_tun_reconnects_total")
    reconnects_before=${reconnects_before:-0}
    reconnects_before=${reconnects_before%.*}

    # Bring interface down (simulates VPN failure)
    info "Deleting pia0..."
    dexec ip link delete pia0 || true
    sleep 1

    # DNS leak test: PIA resolution should fail when VPN is down
    test_dns_leak

    # Traffic leak test: outbound connections should be blocked
    info "Testing outbound connectivity (should fail)..."
    if dexec curl -sf --max-time 3 ifconfig.me >/dev/null 2>&1; then
        fail "Traffic leaks with VPN down"
    else
        pass "Traffic blocked with VPN down"
    fi

    # Wait for reconnect
    wait_for_unhealthy || return 1

    info "Waiting for health to recover..."
    if ! wait_for "$CONNECT_TIMEOUT" "Reconnected" curl -sf "$METRICS_URL/health"; then
        return 1
    fi

    # Verify reconnect counter incremented
    local reconnects_after
    reconnects_after=$(get_metric "pia_tun_reconnects_total")
    reconnects_after=${reconnects_after:-0}
    reconnects_after=${reconnects_after%.*}

    if [ "$reconnects_after" -gt "$reconnects_before" ]; then
        pass "Reconnect counter incremented ($reconnects_before -> $reconnects_after)"
    else
        fail "Reconnect counter didn't increment ($reconnects_before -> $reconnects_after)"
    fi
}

test_proxy() {
    step "Test: Proxy"

    local vpn_ip socks_ip http_ip
    vpn_ip=$(get_json current_ip)

    # SOCKS5
    local socks_port=$SOCKS5_PORT
    if dexec nc -z localhost "$socks_port" 2>/dev/null; then
        pass "SOCKS5 listening on :$socks_port"
    else
        fail "SOCKS5 not listening on :$socks_port"
        return 1
    fi

    local socks_url="localhost:$socks_port"
    if [ -n "${PROXY_USER:-}" ] && [ -n "${PROXY_PASS:-}" ]; then
        socks_url="${PROXY_USER}:${PROXY_PASS}@$socks_url"
    fi

    socks_ip=$(dexec curl -sf --max-time 10 --socks5 "$socks_url" ifconfig.me || true)
    if [ "$socks_ip" = "$vpn_ip" ]; then
        pass "SOCKS5 routes through VPN ($socks_ip)"
    else
        fail "SOCKS5 IP ($socks_ip) doesn't match VPN IP ($vpn_ip)"
    fi

    # HTTP proxy
    local http_port=$HTTP_PROXY_PORT
    if dexec nc -z localhost "$http_port" 2>/dev/null; then
        pass "HTTP proxy listening on :$http_port"
    else
        fail "HTTP proxy not listening on :$http_port"
        return 1
    fi

    local http_url="http://localhost:$http_port"
    if [ -n "${PROXY_USER:-}" ] && [ -n "${PROXY_PASS:-}" ]; then
        http_url="http://${PROXY_USER}:${PROXY_PASS}@localhost:$http_port"
    fi

    http_ip=$(dexec curl -sf --max-time 10 --proxy "$http_url" http://ifconfig.me || true)
    if [ "$http_ip" = "$vpn_ip" ]; then
        pass "HTTP proxy routes through VPN ($http_ip)"
    else
        fail "HTTP proxy IP ($http_ip) doesn't match VPN IP ($vpn_ip)"
    fi

    # Auth enforcement (if configured)
    if [ -n "${PROXY_USER:-}" ] && [ -n "${PROXY_PASS:-}" ]; then
        if ! dexec curl -sf --max-time 5 --socks5 "localhost:$socks_port" ifconfig.me >/dev/null 2>&1; then
            pass "SOCKS5 auth enforced"
        else
            fail "SOCKS5 accepts unauthenticated connections"
        fi

        if ! dexec curl -sf --max-time 5 --proxy "http://localhost:$http_port" http://ifconfig.me >/dev/null 2>&1; then
            pass "HTTP proxy auth enforced"
        else
            fail "HTTP proxy accepts unauthenticated connections"
        fi
    fi
}

test_metrics_endpoint() {
    step "Test: Metrics endpoint"

    # Prometheus format
    if curl -sf "$METRICS_URL/metrics" 2>/dev/null | grep -q "pia_tun_"; then
        pass "Prometheus endpoint responding"
    else
        fail "Prometheus endpoint not responding"
    fi

    # JSON format
    local json
    json=$(curl -sf "$METRICS_URL/metrics?format=json" 2>/dev/null || true)
    if [ -n "$json" ] && echo "$json" | jq . >/dev/null 2>&1; then
        pass "JSON endpoint responding"
    else
        fail "JSON endpoint not responding or invalid"
    fi

    # Latency sanity check
    local latency
    latency=$(echo "$json" | jq -r '.server_latency_ms // -1' 2>/dev/null)
    if [ "$latency" -ge 0 ] && [ "$latency" -le 500 ] 2>/dev/null; then
        pass "Server latency: ${latency}ms"
    elif [ "$latency" -gt 500 ] 2>/dev/null; then
        fail "Server latency too high: ${latency}ms"
    else
        skip "Could not read server latency"
    fi
}

emulate_wan_down() {
    step "Emulating wan down state..."

    dexec sh -c '
        while true; do
            num=$(iptables -L VPN_OUT --line-numbers -n | grep -m 1 "bypass_routes" | awk "{print \$1}")
            if [ -z "$num" ]; then
                break
            fi
            iptables -D VPN_OUT "$num" 2>/dev/null || break
        done
    '
    info "Removed bypass_routes from VPN_OUT chain"

    dexec ip link set down pia0
    info "pia0 interface set down"

    wait_for_unhealthy  || return 1
}

test_downtime_leaks() {
    local timeout=4

    step "Testing for leaks during wan down"
    # Test general leaks out default interface
    if dexec ping -q -c 1 -W "$timeout" 1.0.0.1 >/dev/null 2>&1; then
        fail "ICMP to 1.0.0.1 succeeded - should be blocked while down"
    else
        pass "ICMP to 1.0.0.1 blocked"
    fi

    # DNS leak test: PIA resolution should fail when VPN is down
    test_dns_leak
}

test_downtime_metrics() {
    step "Test: Down state metrics"

    local conn_up wan_up pf_active ks_active
    conn_up=$(get_metric "pia_tun_connection_up")
    wan_up=$(get_metric "pia_tun_wan_up")
    pf_active=$(get_metric "pia_tun_port_forwarding_active")
    ks_active=$(get_metric "pia_tun_killswitch_active")

    [ "$conn_up" = "0" ] && pass "connection_up = 0" || fail "connection_up = $conn_up"
    [ "$wan_up" = "0" ] && pass "wan_up = 0" || fail "wan_up = $wan_up"
    [ "$pf_active" = "0" ] && pass "port_forwarding_active = 0" || fail "port_forwarding_active = $pf_active"
    [ "$ks_active" = "1" ] && pass "killswitch_active = 1" || fail "killswitch_active = $ks_active"
}

# ============================================================================
# Main
# ============================================================================

summary() {
    local W=48

    if [ "$FAILED" -gt 0 ]; then
        CLR=$RED; RESULT="FAIL"
    else
        CLR=$GRN; RESULT="PASS"
    fi

    local ppad=$((W - 14 - ${#PASSED}))
    local fpad=$((W - 24 - ${#FAILED} - ${#RESULT}))
    local spad=$((W - 14 - ${#SKIPPED}))

    echo ""
    echo -e "${CLR}╔$(printf '═%.0s' $(seq 1 $W))╗${NC}"
    echo -e "${CLR}║${NC}     ${GRN}Passed:${NC}  ${PASSED}$(printf '%*s' $ppad '')${CLR}║${NC}"
    echo -e "${CLR}║${NC}     ${RED}Failed:${NC}  ${FAILED}$(printf '%*s' $fpad '')${CLR}${BLD}${RESULT}${NC}${CLR}          ║${NC}"
    echo -e "${CLR}║${NC}     ${YLW}Skipped:${NC} ${SKIPPED}$(printf '%*s' $spad '')${CLR}║${NC}"
    echo -e "${CLR}╚$(printf '═%.0s' $(seq 1 $W))╝${NC}"

    if [ "$RESULT" = "FAIL" ];  then
        echo ""
        echo ""
        echo -e "${BLU}▶${NC} Container logs"
        echo -e "${BLU}─────────────────────────────────────────────────────────────────────────────────────────${NC}"
        docker logs "$CONTAINER" 2>&1 || true
        echo -e "${BLU}─────────────────────────────────────────────────────────────────────────────────────────${NC}"
        return 1
    fi
}

suite() {
    # initialize
    print_banner
    check_prerequisites
    build
    start_container

    # wait for up
    wait_for_ready        || return 1
    wait_for_healthy      || return 1
    wait_for_port_forward || return 1

    # test runtime state
    dump_firewall_state "Run-time"
    test_ip_changed
    test_connection_metrics
    test_port_forwarding "initial-run"
    test_ipv6_blocked

    # test killswitch + reconnect
    test_killswitch || return 1

    # test (post)reconnect state
    wait_for_port_forward || return 1
    test_port_forwarding "post-reconnect"
    test_proxy
    test_metrics_endpoint

    # test downtime state
    emulate_wan_down || return 1
    wait_for_wan_down || return 1
    test_downtime_leaks
    test_downtime_metrics
    dump_firewall_state "WAN-down"
}

main() {
    suite || true
    summary
}

main "$@"

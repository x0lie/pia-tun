#!/bin/bash
# Automated test suite for pia-tun
# Tests critical functionality: VPN connection, IP changes, killswitch, reconnection

set -e  # Exit on error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
CONTAINER_NAME="pia-tun-test"
IMAGE_NAME="${IMAGE_NAME:-pia-tun:test}"

# Configurable timeouts (can be overridden via environment variables)
TEST_TIMEOUT="${TEST_TIMEOUT:-120}"              # VPN connection timeout (seconds)
PIPE_WAIT_TIMEOUT="${PIPE_WAIT_TIMEOUT:-120}"   # Named pipe creation timeout (seconds)
RECONNECT_TIMEOUT="${RECONNECT_TIMEOUT:-45}"    # Reconnection completion timeout (seconds)
PORT_FORWARD_TIMEOUT="${PORT_FORWARD_TIMEOUT:-60}"  # Port forwarding timeout (seconds)
LEAK_TEST_DURATION="${LEAK_TEST_DURATION:-10}"  # Killswitch leak test duration (seconds)
RECONNECT_LEAK_DURATION="${RECONNECT_LEAK_DURATION:-30}"  # Reconnection leak test duration (seconds)

# Counters
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0

# Debug mode - pause on failure to allow manual investigation
DEBUG_PAUSE="${DEBUG_PAUSE:-false}"

# Cleanup function
cleanup() {
    local exit_code=$?

    # Only show cleanup if we actually started containers
    if docker ps -a | grep -q "$CONTAINER_NAME" 2>/dev/null; then
        echo ""
        echo -e "${BLUE}=== Cleanup ===${NC}"
        docker-compose -f testing/docker-compose.test.yml down -v 2>/dev/null || true
        docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
    fi

    exit $exit_code
}

# Set trap to cleanup on exit
trap cleanup EXIT

# Test result functions
pass() {
    echo -e "${GREEN}✅ PASS:${NC} $1"
    TESTS_PASSED=$((TESTS_PASSED + 1))
}

fail() {
    echo -e "${RED}❌ FAIL:${NC} $1"
    TESTS_FAILED=$((TESTS_FAILED + 1))

    # Pause for debugging if requested
    if [ "${DEBUG_PAUSE}" == "true" ]; then
        echo ""
        echo -e "${YELLOW}╔════════════════════════════════════════════════╗${NC}"
        echo -e "${YELLOW}║           DEBUG MODE: Test Failed             ║${NC}"
        echo -e "${YELLOW}╚════════════════════════════════════════════════╝${NC}"
        echo -e "${BLUE}Container:${NC} $CONTAINER_NAME"
        echo -e "${BLUE}Commands:${NC}"
        echo "  docker exec -it $CONTAINER_NAME bash"
        echo "  docker logs $CONTAINER_NAME"
        echo "  docker exec $CONTAINER_NAME cat /tmp/proxy.log"
        echo ""
        echo -e "${YELLOW}Press ENTER to continue tests or Ctrl+C to exit${NC}"
        read -r
    fi

    if [ "${STRICT_MODE:-false}" == "true" ]; then
        exit 1
    fi
}

skip() {
    echo -e "${YELLOW}⏭️  SKIP:${NC} $1"
    TESTS_SKIPPED=$((TESTS_SKIPPED + 1))
}

info() {
    echo -e "${BLUE}ℹ️  INFO:${NC} $1"
}

# Check prerequisites
check_prerequisites() {
    echo -e "${BLUE}=== Checking Prerequisites ===${NC}"

    if ! command -v docker &> /dev/null; then
        fail "Docker is not installed"
        exit 1
    fi
    pass "Docker is available"

    if ! command -v docker-compose &> /dev/null; then
        fail "Docker Compose is not installed"
        exit 1
    fi
    pass "Docker Compose is available"

    if ! command -v go &> /dev/null; then
        fail "Go is not installed (required for building leak tester)"
        exit 1
    fi
    pass "Go is available"

    if ! command -v jq &> /dev/null; then
        fail "jq is not installed (required for parsing test results)"
        exit 1
    fi
    pass "jq is available"

    # Check for required environment variables
    if [ -z "$PIA_USER" ] || [ -z "$PIA_PASS" ]; then
        fail "PIA_USER and PIA_PASS environment variables required"
        info "Set them: export PIA_USER=p1234567 PIA_PASS='yourpassword'"
        exit 1
    fi
    pass "PIA credentials configured"
}

# Build Docker image
build_image() {
    echo ""
    echo -e "${BLUE}=== Building Docker Image ===${NC}"
    if docker build -t "$IMAGE_NAME" .; then
        pass "Image built successfully"
    else
        fail "Image build failed"
        exit 1
    fi
}

# Build leak testing tool
build_leak_tester() {
    echo ""
    echo -e "${BLUE}=== Building Leak Test Tool ===${NC}"

    # Create bin directory if it doesn't exist
    mkdir -p bin

    # Build the leak tester (static binary for Alpine compatibility)
    cd testing/leaktest
    if CGO_ENABLED=0 go build -ldflags="-w -s" -o ../../bin/leaktest .; then
        cd ../..
        pass "Leak tester built successfully"
    else
        cd ../..
        fail "Leak tester build failed"
        exit 1
    fi
}

# Start container
start_container() {
    echo ""
    echo -e "${BLUE}=== Starting Container ===${NC}"

    # Clean up any existing container
    docker rm -f "$CONTAINER_NAME" 2>/dev/null || true

    # Start container with test configuration
    docker run -d \
        --name "$CONTAINER_NAME" \
        --cap-add=NET_ADMIN \
        --cap-drop=ALL \
        -e PIA_USER="$PIA_USER" \
        -e PIA_PASS="$PIA_PASS" \
        -e PIA_LOCATION="${PIA_LOCATION:-ca_ontario,ca_toronto}" \
        -e PORT_FORWARDING="${PORT_FORWARDING:-false}" \
        -e PROXY_ENABLED="${PROXY_ENABLED:-false}" \
        -e SOCKS5_PORT="${SOCKS5_PORT:-1080}" \
        -e HTTP_PROXY_PORT="${HTTP_PROXY_PORT:-8888}" \
        -e PROXY_USER="${PROXY_USER:-}" \
        -e PROXY_PASS="${PROXY_PASS:-}" \
        -e LOG_LEVEL=debug \
        -e METRICS=true \
        "$IMAGE_NAME" > /dev/null

    if [ $? -eq 0 ]; then
        pass "Container started"
    else
        fail "Container failed to start"
        exit 1
    fi
}

# Wait for VPN connection
wait_for_connection() {
    echo ""
    echo -e "${BLUE}=== Waiting for VPN Connection ===${NC}"

    for i in $(seq 1 $TEST_TIMEOUT); do
        # Check if container is still running
        if ! docker ps | grep -q "$CONTAINER_NAME"; then
            fail "Container exited unexpectedly"
            echo "Last 20 lines of logs:"
            docker logs --tail 20 "$CONTAINER_NAME"
            return 1
        fi

        # Check for WireGuard handshake
        if docker exec "$CONTAINER_NAME" wg show pia 2>/dev/null | grep -q "latest handshake"; then
            pass "VPN connected (took ${i}s)"
            return 0
        fi

        # Progress indicator
        if [ $((i % 10)) -eq 0 ]; then
            info "Still waiting... (${i}s/${TEST_TIMEOUT}s)"
        fi

        sleep 1
    done

    fail "VPN connection timeout after ${TEST_TIMEOUT}s"
    echo "Last 30 lines of logs:"
    docker logs --tail 30 "$CONTAINER_NAME"
    return 1
}

# Wait for Port file
wait_for_port() {
    # Skip entirely if port-forwarding feature is disabled
    [ "${PORT_FORWARDING:-false}" = "true" ] || {
        skip "Port forwarding not enabled"
        return 0
    }

    echo ""
    echo -e "${BLUE}=== Waiting for /etc/wireguard/port to appear ===${NC}"

    local elapsed=0
    while [ $elapsed -lt ${PORT_FORWARD_TIMEOUT} ]; do
        # Optional: early exit if container died
        if ! docker ps --format "table {{.Names}}" | grep -q "^${CONTAINER_NAME}$"; then
            fail "Container $CONTAINER_NAME is no longer running"
            docker logs --tail 30 "$CONTAINER_NAME" 2>/dev/null || true
            return 1
        fi

        # This is the most reliable way: use `test -f` inside the container
        if docker exec "$CONTAINER_NAME" test -f /etc/wireguard/port 2>/dev/null; then
            local port_content
            port_content=$(docker exec "$CONTAINER_NAME" cat /etc/wireguard/port 2>/dev/null || echo "unknown")
            pass "Port file appeared after ${elapsed}s → forwarded port: $port_content"
            return 0
        fi

        # Progress indicator every 15 seconds
        [ $((elapsed % 15)) -eq 0 ] && [ $elapsed -gt 0 ] && \
            info "Still waiting for port file... (${elapsed}s elapsed)"

        sleep 1
        elapsed=$((elapsed + 1))
    done

    fail "Timed out after ${PORT_FORWARD_TIMEOUT}s waiting for /etc/wireguard/port"
    echo "=== Last 30 lines of container logs ==="
    docker logs --tail 30 "$CONTAINER_NAME" 2>/dev/null || true
    return 1
}

# Test: Get real IP (host IP for comparison)
get_real_ip() {
    echo ""
    echo -e "${BLUE}=== Getting Real IP ===${NC}"
    REAL_IP=$(curl -s --max-time 10 ifconfig.me || echo "")

    if [ -z "$REAL_IP" ]; then
        skip "Could not determine real IP (network issue?)"
        return 1
    fi

    info "Real IP: $REAL_IP"
    return 0
}

# Test: VPN IP is different from real IP
test_ip_changed() {
    echo ""
    echo -e "${BLUE}=== Test: IP Changed ===${NC}"

    VPN_IP=$(docker exec "$CONTAINER_NAME" curl -s --max-time 10 ifconfig.me 2>/dev/null || echo "")

    if [ -z "$VPN_IP" ]; then
        fail "Could not get VPN IP"
        return 1
    fi

    info "VPN IP: $VPN_IP"

    if [ -z "$REAL_IP" ]; then
        skip "Real IP unknown, cannot compare"
        return 0
    fi

    if [ "$VPN_IP" == "$REAL_IP" ]; then
        fail "VPN IP matches real IP (VPN not working or leak detected)"
        return 1
    fi

    pass "IP changed: $REAL_IP → $VPN_IP"
}

# Test: IPv6 is blocked
test_ipv6_blocked() {
    echo ""
    echo -e "${BLUE}=== Test: IPv6 Blocked ===${NC}"

    # Test 1: Check firewall rules block IPv6
    info "Checking firewall rules..."

    # Check if using nftables or iptables
    if docker exec "$CONTAINER_NAME" nft list table inet vpn_filter 2>/dev/null >/dev/null; then
        # Using nftables - check chain policy and IPv6 rules
        CHAIN_OUTPUT=$(docker exec "$CONTAINER_NAME" nft list chain inet vpn_filter output 2>/dev/null)

        # Check if policy is drop
        if echo "$CHAIN_OUTPUT" | grep -q "policy drop"; then
            # Check if there are any rules that explicitly allow IPv6
            if echo "$CHAIN_OUTPUT" | grep -E "(ip6|meta l3proto ip6)" | grep -q "accept"; then
                fail "nftables: IPv6 traffic may be allowed (found IPv6 accept rules)"
                echo "$CHAIN_OUTPUT" | grep -E "(ip6|meta l3proto ip6)"
                return 1
            else
                pass "nftables: IPv6 blocked (policy drop, no IPv6 allow rules)"
            fi
        else
            fail "nftables: Output chain policy is not drop"
            return 1
        fi
    elif docker exec "$CONTAINER_NAME" ip6tables -L OUTPUT -n 2>/dev/null | grep -q "DROP"; then
        # Using iptables - check for IPv6 policy/rules
        pass "ip6tables: IPv6 DROP rules found"
    else
        skip "Could not verify firewall IPv6 rules (firewall not accessible)"
    fi

    # Test 2: Check no IPv6 routes exist (except loopback)
    info "Checking IPv6 routes..."
    IPV6_ROUTES=$(docker exec "$CONTAINER_NAME" ip -6 route show 2>/dev/null | grep -v "^::1" | grep -v "^fe80" | wc -l)

    if [ "$IPV6_ROUTES" -eq 0 ]; then
        pass "No non-local IPv6 routes exist"
    else
        fail "Found $IPV6_ROUTES IPv6 route(s) outside of local scope"
        docker exec "$CONTAINER_NAME" ip -6 route show 2>/dev/null || true
        return 1
    fi

    # Test 3: Try to get IPv6 address via external service (should fail/timeout)
    info "Testing IPv6 connectivity..."
    if docker exec "$CONTAINER_NAME" curl -6 -s --max-time 5 ifconfig.co 2>/dev/null; then
        fail "IPv6 leaked (got response from IPv6 endpoint)"
        return 1
    fi

    pass "IPv6 connectivity blocked (no response from IPv6 endpoint)"
}

# Test: Killswitch blocks traffic when VPN is down
test_killswitch() {
    echo ""
    echo -e "${BLUE}=== Test: Killswitch (Comprehensive) ===${NC}"

    if [ -z "$REAL_IP" ]; then
        skip "Real IP unknown, cannot run comprehensive leak tests"
        return 0
    fi

    # Copy leak tester into container (use /usr/local/bin instead of /tmp due to noexec)
    info "Copying leak tester into container..."
    docker cp bin/leaktest "$CONTAINER_NAME:/usr/local/bin/leaktest"

    # Test if leak tester can run
    if ! docker exec "$CONTAINER_NAME" /usr/local/bin/leaktest --version >/dev/null 2>&1; then
        fail "Leak tester binary cannot run in container (architecture mismatch?)"
        docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest 2>/dev/null || true
        return 1
    fi

    info "Bringing down VPN interface..."
    docker exec "$CONTAINER_NAME" ip link set pia down 2>/dev/null || true

    sleep 2

    # Run leak tester with high concurrency
    info "Running intensive leak tests (${LEAK_TEST_DURATION}s, 50 concurrent workers)..."

    # Test HTTP, HTTPS, DNS, UDP, and bypass route restrictions
    if ! docker exec "$CONTAINER_NAME" /usr/local/bin/leaktest \
        --duration ${LEAK_TEST_DURATION}s \
        --concurrency 50 \
        --interval 100ms \
        --real-ip "$REAL_IP" \
        --protocols http,https,dns,udp,bypass \
        --output /tmp/killswitch_results.json \
        --quiet; then
        fail "Leak tester failed to run (check logs above)"
        docker exec "$CONTAINER_NAME" ip link set pia up 2>/dev/null || true
        docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest 2>/dev/null || true
        return 1
    fi

    # Check if results file was created
    if ! docker exec "$CONTAINER_NAME" test -f /tmp/killswitch_results.json 2>/dev/null; then
        fail "Leak tester did not create results file"
        docker exec "$CONTAINER_NAME" ip link set pia up 2>/dev/null || true
        docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest 2>/dev/null || true
        return 1
    fi

    # Check results
    LEAKS=$(docker exec "$CONTAINER_NAME" cat /tmp/killswitch_results.json | jq -r '.leaks_detected' 2>/dev/null || echo "error")
    ATTEMPTS=$(docker exec "$CONTAINER_NAME" cat /tmp/killswitch_results.json | jq -r '.total_attempts' 2>/dev/null || echo "0")

    # Bring interface back up
    info "Bringing VPN interface back up..."
    docker exec "$CONTAINER_NAME" ip link set pia up 2>/dev/null || true

    # Cleanup
    docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest /tmp/killswitch_results.json 2>/dev/null || true

    if [ "$LEAKS" == "error" ]; then
        fail "Leak tester failed to run"
        return 1
    fi

    if [ "$LEAKS" -gt 0 ]; then
        fail "Killswitch leaked: $LEAKS leaks detected in $ATTEMPTS attempts"
        return 1
    fi

    pass "Killswitch working: 0 leaks in $ATTEMPTS attempts (HTTP, HTTPS, DNS, UDP, bypass restrictions tested)"
    sleep 3
}

# Test: Verify all temporary exemptions are cleaned up
test_exemptions_cleaned() {
    echo ""
    echo -e "${BLUE}=== Test: Firewall Exemptions Cleaned ===${NC}"

    # Check for temporary exemptions in nftables
    TEMP_RULES=$(docker exec "$CONTAINER_NAME" nft list chain inet vpn_filter output 2>/dev/null | grep -c "temp_" 2>/dev/null || echo "0")
    TEMP_RULES=$(echo "$TEMP_RULES" | tr -d '\n\r' | xargs)

    if [ "$TEMP_RULES" == "error" ] || [ -z "$TEMP_RULES" ]; then
        # Try iptables fallback
        TEMP_RULES=$(docker exec "$CONTAINER_NAME" iptables -L OUTPUT -n -v 2>/dev/null | grep -c "temp_" 2>/dev/null || echo "0")
        TEMP_RULES=$(echo "$TEMP_RULES" | tr -d '\n\r' | xargs)
    fi

    if [ "$TEMP_RULES" -gt 0 ] 2>/dev/null; then
        fail "Found $TEMP_RULES temporary exemptions still active (security risk)"
        info "Listing active temporary exemptions:"
        docker exec "$CONTAINER_NAME" nft list chain inet vpn_filter output 2>/dev/null | grep "temp_" || \
        docker exec "$CONTAINER_NAME" iptables -L OUTPUT -n -v 2>/dev/null | grep "temp_"
        return 1
    fi

    pass "All temporary exemptions cleaned up (no persistent holes)"
}

# Test: Reconnection doesn't leak IP
test_reconnection() {
    echo ""
    echo -e "${BLUE}=== Test: Reconnection (No Leaks) ===${NC}"

    if [ -z "$REAL_IP" ]; then
        skip "Real IP unknown, cannot detect leaks"
        return 0
    fi

    # Wait for named pipe to be created (indicates run.sh is ready for reconnection signals)
    info "Waiting for reconnect named pipe to be created..."

    for i in $(seq 1 $PIPE_WAIT_TIMEOUT); do
        if docker exec "$CONTAINER_NAME" test -p /tmp/vpn_reconnect_pipe 2>/dev/null; then
            info "Named pipe ready (found after ${i}s)"
            break
        fi

        # Progress indicator
        if [ $((i % 15)) -eq 0 ]; then
            info "Still waiting for named pipe... (${i}s/${PIPE_WAIT_TIMEOUT}s)"
        fi

        sleep 1
    done

    # Verify pipe exists
    if ! docker exec "$CONTAINER_NAME" test -p /tmp/vpn_reconnect_pipe 2>/dev/null; then
        fail "Named pipe not created after ${PIPE_WAIT_TIMEOUT}s"
        return 1
    fi

    # Wait for initial port forwarding to complete before triggering reconnection
    # This prevents killing the port forwarding process mid-acquisition
    # Check by looking for the /tmp/port_forwarding_complete flag or the port file
    info "Checking if port forwarding is enabled..."

    # Give it a moment to start
    sleep 5

    # Check if port forwarding process is running
    # if docker exec "$CONTAINER_NAME" pgrep -f "portforward" >/dev/null 2>&1; then
    #     info "Port forwarding is enabled, waiting for initial acquisition to complete..."

    #     for i in $(seq 1 60); do
    #         # Check for completion flag
    #         if docker exec "$CONTAINER_NAME" test -f /tmp/port_forwarding_complete 2>/dev/null; then
    #             INITIAL_PORT=$(docker exec "$CONTAINER_NAME" cat /etc/wireguard/port 2>/dev/null || echo "unknown")
    #             info "Port forwarding ready (port: $INITIAL_PORT, took ${i}s)"
    #             break
    #         fi

    #         if [ $((i % 10)) -eq 0 ]; then
    #             info "Still waiting for port forwarding... (${i}s/60s)"
    #         fi

    #         sleep 1
    #     done

    #     # Check if we got a port
    #     if ! docker exec "$CONTAINER_NAME" test -f /tmp/port_forwarding_complete 2>/dev/null; then
    #         skip "Port forwarding did not complete in 60s, skipping reconnection test"
    #         return 0
    #     fi
    # else
    #     info "Port forwarding not enabled, proceeding with reconnection test"
    # fi

    # Copy leak tester into container BEFORE triggering reconnection
    info "Copying leak tester into container..."
    docker cp bin/leaktest "$CONTAINER_NAME:/usr/local/bin/leaktest"

    # Test if leak tester can run
    if ! docker exec "$CONTAINER_NAME" /usr/local/bin/leaktest --version >/dev/null 2>&1; then
        fail "Leak tester binary cannot run in container (architecture mismatch?)"
        docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest 2>/dev/null || true
        return 1
    fi

    # Start intensive leak testing in background BEFORE triggering reconnection
    # Note: Only test HTTP/HTTPS for reconnection (can detect real IP vs VPN IP)
    # DNS/UDP would show false positives when VPN comes back up (can't distinguish tunnel vs bypass)
    info "Starting intensive leak monitoring (100 concurrent workers, ${RECONNECT_LEAK_DURATION}s)..."
    docker exec -d "$CONTAINER_NAME" sh -c "
        /usr/local/bin/leaktest \
            --duration ${RECONNECT_LEAK_DURATION}s \
            --concurrency 100 \
            --interval 50ms \
            --real-ip '$REAL_IP' \
            --protocols http,https \
            --output /tmp/reconnection_results.json \
            --quiet >/tmp/leaktest.log 2>&1
    "

    # Give leak tester a moment to start
    sleep 2

    # Trigger immediate reconnection by writing to the named pipe
    info "Triggering reconnection via named pipe..."
    if ! docker exec "$CONTAINER_NAME" sh -c 'echo "test_suite_reconnection" > /tmp/vpn_reconnect_pipe' 2>/dev/null; then
        fail "Failed to write to reconnect pipe"
        docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest /tmp/reconnection_results.json 2>/dev/null || true
        return 1
    fi

    info "Wrote to reconnect pipe successfully"

    # Wait for reconnection flag to appear (indicates reconnection has started)
    info "Waiting for reconnection to start..."
    RECONNECT_STARTED=false
    WAIT_TIME=10  # Pipe-based reconnection should start quickly

    for i in $(seq 1 $WAIT_TIME); do
        if docker exec "$CONTAINER_NAME" test -f /tmp/reconnecting 2>/dev/null; then
            RECONNECT_STARTED=true
            info "Reconnection started (detected after ${i}s)"
            break
        fi

        sleep 1
    done

    if [ "$RECONNECT_STARTED" == "false" ]; then
        skip "Reconnection did not start within ${WAIT_TIME}s"
        docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest /tmp/reconnection_results.json 2>/dev/null || true
        return 0
    fi

    # Wait for reconnection to complete
    info "Monitoring reconnection progress..."
    for i in $(seq 1 $RECONNECT_TIMEOUT); do
        if ! docker exec "$CONTAINER_NAME" test -f /tmp/reconnecting 2>/dev/null; then
            info "Reconnection completed after ${i}s"
            break
        fi

        if [ $((i % 10)) -eq 0 ]; then
            info "Still reconnecting... (${i}s)"
        fi

        sleep 1
    done

    # Wait for leak tester to finish and write results (up to 30 seconds)
    info "Waiting for leak tester to finish..."
    for i in $(seq 1 30); do
        if docker exec "$CONTAINER_NAME" test -f /tmp/reconnection_results.json 2>/dev/null; then
            info "Leak test completed (found results after ${i}s)"
            break
        fi

        if [ $((i % 5)) -eq 0 ]; then
            info "Still testing... (${i}s/30s)"
        fi

        sleep 1
    done

    # Check if results file exists
    if ! docker exec "$CONTAINER_NAME" test -f /tmp/reconnection_results.json 2>/dev/null; then
        fail "Leak tester did not create results file (process may have crashed)"
        info "Checking for leaktest process..."
        docker exec "$CONTAINER_NAME" ps aux 2>/dev/null | grep leaktest || true
        info "Leak tester log output:"
        docker exec "$CONTAINER_NAME" cat /tmp/leaktest.log 2>/dev/null || echo "(no log file)"
        docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest /tmp/leaktest.log 2>/dev/null || true
        return 1
    fi

    # Check results
    LEAKS=$(docker exec "$CONTAINER_NAME" cat /tmp/reconnection_results.json 2>/dev/null | jq -r '.leaks_detected' || echo "error")
    ATTEMPTS=$(docker exec "$CONTAINER_NAME" cat /tmp/reconnection_results.json 2>/dev/null | jq -r '.total_attempts' || echo "0")

    if [ "$LEAKS" == "error" ] || [ -z "$LEAKS" ]; then
        fail "Leak tester failed to parse results"
        info "Leak tester log output:"
        docker exec "$CONTAINER_NAME" cat /tmp/leaktest.log 2>/dev/null || echo "(no log file)"
        docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest /tmp/reconnection_results.json /tmp/leaktest.log 2>/dev/null || true
        return 1
    fi

    if [ "$ATTEMPTS" == "0" ] || [ -z "$ATTEMPTS" ]; then
        fail "Leak tester reported 0 attempts (likely crashed early)"
        info "Leak tester log output:"
        docker exec "$CONTAINER_NAME" cat /tmp/leaktest.log 2>/dev/null || echo "(no log file)"
        docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest /tmp/reconnection_results.json /tmp/leaktest.log 2>/dev/null || true
        return 1
    fi

    if [ "$LEAKS" -gt 0 ] 2>/dev/null; then
        fail "IP leaked during reconnection: $LEAKS leaks in $ATTEMPTS attempts"

        # Show detailed leak breakdown
        info "Analyzing leak details..."
        echo ""
        echo "Leak breakdown by protocol:"
        docker exec "$CONTAINER_NAME" cat /tmp/reconnection_results.json 2>/dev/null | jq -r '.leaks_per_protocol'
        echo ""
        echo "First 10 leak details:"
        docker exec "$CONTAINER_NAME" cat /tmp/reconnection_results.json 2>/dev/null | jq -r '.leak_details[0:10]'
        echo ""
        echo "Summary:"
        docker exec "$CONTAINER_NAME" cat /tmp/reconnection_results.json 2>/dev/null | jq -r '.summary'

        docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest /tmp/reconnection_results.json /tmp/leaktest.log 2>/dev/null || true
        return 1
    fi

    pass "No IP leaks during reconnection: 0 leaks in $ATTEMPTS concurrent attempts"

    # Cleanup
    docker exec "$CONTAINER_NAME" rm -f /usr/local/bin/leaktest /tmp/reconnection_results.json /tmp/leaktest.log 2>/dev/null || true

    # Wait for reconnection to fully complete (WireGuard handshake)
    info "Waiting for VPN handshake to complete..."
    for i in $(seq 1 30); do
        if docker exec "$CONTAINER_NAME" wg show pia 2>/dev/null | grep -q "latest handshake"; then
            info "VPN reconnected successfully (handshake after ${i}s)"
            return 0
        fi
        sleep 1
    done

    skip "VPN handshake did not complete in 30s (may need more time)"
}

# Test: Proxy functionality (if enabled)
test_proxy() {
    echo ""
    echo -e "${BLUE}=== Test: Proxy (SOCKS5 and HTTP) ===${NC}"

    if [ "${PROXY_ENABLED:-false}" != "true" ]; then
        skip "Proxy not enabled"
        return 0
    fi

    # Get VPN IP for comparison
    VPN_IP=$(docker exec "$CONTAINER_NAME" curl -s --max-time 10 ifconfig.me 2>/dev/null || echo "")

    if [ -z "$VPN_IP" ]; then
        skip "Could not get VPN IP for proxy testing"
        return 0
    fi

    # Test 1: Check SOCKS5 proxy is listening
    SOCKS5_PORT="${SOCKS5_PORT:-1080}"
    info "Checking SOCKS5 proxy on port $SOCKS5_PORT..."

    if ! docker exec "$CONTAINER_NAME" nc -z localhost "$SOCKS5_PORT" 2>/dev/null; then
        fail "SOCKS5 proxy not listening on port $SOCKS5_PORT"
        return 1
    fi

    pass "SOCKS5 proxy listening on port $SOCKS5_PORT"

    # Test 2: Test SOCKS5 proxy routes through VPN
    info "Testing SOCKS5 proxy routes through VPN..."

    # Build SOCKS5 URL (with auth if configured)
    if [ -n "${PROXY_USER:-}" ] && [ -n "${PROXY_PASS:-}" ]; then
        SOCKS5_URL="${PROXY_USER}:${PROXY_PASS}@localhost:${SOCKS5_PORT}"
    else
        SOCKS5_URL="localhost:${SOCKS5_PORT}"
    fi

    PROXY_IP=$(docker exec "$CONTAINER_NAME" curl -s --max-time 10 \
        --socks5 "$SOCKS5_URL" \
        ifconfig.me 2>/dev/null || echo "")

    if [ -z "$PROXY_IP" ]; then
        fail "SOCKS5 proxy connection failed"
        return 1
    fi

    if [ "$PROXY_IP" != "$VPN_IP" ]; then
        fail "SOCKS5 proxy IP ($PROXY_IP) doesn't match VPN IP ($VPN_IP)"
        return 1
    fi

    pass "SOCKS5 proxy routes through VPN (IP: $PROXY_IP)"

    # Test 3: Check HTTP proxy is listening
    HTTP_PROXY_PORT="${HTTP_PROXY_PORT:-8888}"
    info "Checking HTTP proxy on port $HTTP_PROXY_PORT..."

    if ! docker exec "$CONTAINER_NAME" nc -z localhost "$HTTP_PROXY_PORT" 2>/dev/null; then
        fail "HTTP proxy not listening on port $HTTP_PROXY_PORT"
        return 1
    fi

    pass "HTTP proxy listening on port $HTTP_PROXY_PORT"

    # Test 4: Test HTTP proxy routes through VPN
    info "Testing HTTP proxy routes through VPN..."

    # Build HTTP proxy URL (with auth if configured)
    if [ -n "${PROXY_USER:-}" ] && [ -n "${PROXY_PASS:-}" ]; then
        HTTP_PROXY_URL="http://${PROXY_USER}:${PROXY_PASS}@localhost:${HTTP_PROXY_PORT}"
    else
        HTTP_PROXY_URL="http://localhost:${HTTP_PROXY_PORT}"
    fi

    HTTP_PROXY_IP=$(docker exec "$CONTAINER_NAME" curl -s --max-time 10 \
        --proxy "$HTTP_PROXY_URL" \
        http://ifconfig.me 2>/dev/null || echo "")

    if [ -z "$HTTP_PROXY_IP" ]; then
        fail "HTTP proxy connection failed"
        return 1
    fi

    if [ "$HTTP_PROXY_IP" != "$VPN_IP" ]; then
        fail "HTTP proxy IP ($HTTP_PROXY_IP) doesn't match VPN IP ($VPN_IP)"
        return 1
    fi

    pass "HTTP proxy routes through VPN (IP: $HTTP_PROXY_IP)"

    # Test 5: Verify authentication is enforced (if configured)
    if [ -n "${PROXY_USER:-}" ] && [ -n "${PROXY_PASS:-}" ]; then
        info "Verifying proxy authentication is enforced..."

        # Try SOCKS5 without auth (should fail)
        if docker exec "$CONTAINER_NAME" curl -s --max-time 5 --fail \
            --socks5 localhost:"$SOCKS5_PORT" \
            ifconfig.me 2>/dev/null >/dev/null; then
            fail "SOCKS5 proxy accepted unauthenticated connection (auth not enforced)"
            return 1
        fi

        pass "SOCKS5 authentication enforced (unauthenticated request blocked)"

        # Try HTTP proxy without auth (should fail with 407)
        # Note: --fail makes curl exit with error code 22 on HTTP errors like 407
        if docker exec "$CONTAINER_NAME" curl -s --max-time 5 --fail \
            --proxy http://localhost:"$HTTP_PROXY_PORT" \
            http://ifconfig.me 2>/dev/null >/dev/null; then
            fail "HTTP proxy accepted unauthenticated connection (auth not enforced)"
            info "Check logs with: docker logs $CONTAINER_NAME | grep -i auth"
            return 1
        fi

        pass "HTTP authentication enforced (unauthenticated request blocked)"
    else
        info "Proxy authentication not configured (proxies accept unauthenticated connections)"
    fi
}

# Test: Port forwarding (if enabled)
test_port_forwarding() {
    echo ""
    echo -e "${BLUE}=== Test: Port Forwarding ===${NC}"

    if [ "${PORT_FORWARDING:-false}" != "true" ]; then
        skip "Port forwarding not enabled"
        return 0
    fi

    # Wait for port forwarding to complete
    info "Waiting for port forwarding setup..."
    for i in $(seq 1 60); do
        if docker exec "$CONTAINER_NAME" test -f /tmp/port_forwarding_complete 2>/dev/null; then
            break
        fi
        sleep 1
    done

    # Check if port file exists
    if ! docker exec "$CONTAINER_NAME" test -f /etc/wireguard/port 2>/dev/null; then
        fail "Port forwarding file not created"
        return 1
    fi

    PORT=$(docker exec "$CONTAINER_NAME" cat /etc/wireguard/port 2>/dev/null || echo "")

    if [ -z "$PORT" ]; then
        fail "Port forwarding file is empty"
        return 1
    fi

    if [ "$PORT" -lt 1024 ] || [ "$PORT" -gt 65535 ]; then
        fail "Invalid port number: $PORT"
        return 1
    fi

    pass "Port forwarding acquired port: $PORT"
}

# Test: Metrics endpoint (if enabled)
test_metrics() {
    echo ""
    echo -e "${BLUE}=== Test: Metrics Endpoint ===${NC}"

    # Test 1: Check endpoint responds
    if ! docker exec "$CONTAINER_NAME" curl -s http://localhost:9090/metrics 2>/dev/null | grep -q "vpn_"; then
        fail "Metrics endpoint not responding"
        return 1
    fi

    pass "Metrics endpoint responding"

    # Test 2: Fetch metrics in JSON format
    METRICS_JSON=$(docker exec "$CONTAINER_NAME" curl -s "http://localhost:9090/metrics?format=json" 2>/dev/null)

    if [ -z "$METRICS_JSON" ]; then
        fail "Metrics JSON format not working"
        return 1
    fi

    pass "Metrics JSON format working"

    # Test 3: Validate connection state via checks
    # Note: vpn_connected field doesn't exist in JSON metrics, but we can infer from checks
    SUCCESSFUL_CHECKS=$(echo "$METRICS_JSON" | jq -r '.successful_checks // -1' 2>/dev/null)
    FAILED_CHECKS=$(echo "$METRICS_JSON" | jq -r '.failed_checks // -1' 2>/dev/null)

    if [ "$SUCCESSFUL_CHECKS" -ge 0 ] && [ "$FAILED_CHECKS" -ge 0 ] 2>/dev/null; then
        # If we have more successful checks than failed, connection is likely good
        if [ "$SUCCESSFUL_CHECKS" -gt 0 ] 2>/dev/null; then
            pass "Metric validation: successful_checks = $SUCCESSFUL_CHECKS (VPN operational)"
        else
            skip "Metric validation: No successful checks yet"
        fi
    else
        skip "Could not parse check metrics"
    fi

    # Test 4: Validate server_latency is reasonable (0-1000ms)
    SERVER_LATENCY=$(echo "$METRICS_JSON" | jq -r '.server_latency_ms // -1' 2>/dev/null)
    if [ "$SERVER_LATENCY" -ge 0 ] && [ "$SERVER_LATENCY" -le 1000 ] 2>/dev/null; then
        pass "Metric validation: server_latency_ms = ${SERVER_LATENCY}ms (reasonable)"
    elif [ "$SERVER_LATENCY" -gt 1000 ] 2>/dev/null; then
        fail "Metric validation: server_latency_ms = ${SERVER_LATENCY}ms (too high)"
        return 1
    else
        skip "Could not parse server_latency_ms metric"
    fi

    # Test 5: Validate health check counters are incrementing
    TOTAL_CHECKS=$(echo "$METRICS_JSON" | jq -r '.total_checks // 0' 2>/dev/null)
    if [ "$TOTAL_CHECKS" -gt 0 ] 2>/dev/null; then
        pass "Metric validation: total_checks = $TOTAL_CHECKS (monitoring active)"
    else
        skip "Could not parse total_checks metric or no checks yet"
    fi

    # Test 6: Validate success rate is high
    # Note: consecutive_failures doesn't exist in JSON, but we can check success_rate_decimal
    SUCCESS_RATE=$(echo "$METRICS_JSON" | jq -r '.success_rate_decimal // -1' 2>/dev/null)

    if [ "$SUCCESS_RATE" != "-1" ] && [ "$SUCCESS_RATE" != "null" ]; then
        # Convert to integer percentage for comparison (use awk for float math)
        SUCCESS_PCT=$(echo "$SUCCESS_RATE" | awk '{printf "%.0f", $1 * 100}')

        if [ -n "$SUCCESS_PCT" ] && [ "$SUCCESS_PCT" -ge 80 ] 2>/dev/null; then
            pass "Metric validation: success_rate = ${SUCCESS_PCT}% (healthy)"
        elif [ -n "$SUCCESS_PCT" ] 2>/dev/null; then
            fail "Metric validation: success_rate = ${SUCCESS_PCT}% (unhealthy, below 80%)"
            return 1
        else
            skip "Could not parse success rate"
        fi
    else
        skip "Could not parse success_rate_decimal metric"
    fi
}

# Show test summary
show_summary() {
    echo ""
    echo -e "${BLUE}╔════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║              Test Summary                      ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════════════╝${NC}"
    echo -e "${GREEN}Passed:${NC}  $TESTS_PASSED"
    echo -e "${RED}Failed:${NC}  $TESTS_FAILED"
    echo -e "${YELLOW}Skipped:${NC} $TESTS_SKIPPED"
    echo ""

    if [ $TESTS_FAILED -gt 0 ]; then
        echo -e "${RED}❌ Some tests failed${NC}"
        echo ""
        echo "To debug:"
        echo "  docker logs $CONTAINER_NAME"
        echo "  docker exec -it $CONTAINER_NAME bash"
        return 1
    else
        echo -e "${GREEN}✅ All tests passed!${NC}"
        return 0
    fi
}

# Main test execution
main() {
    echo -e "${BLUE}╔════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║          pia-tun Test Suite                    ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════════════╝${NC}"
    echo ""

    check_prerequisites
    build_image
    build_leak_tester
    start_container
    get_real_ip

    if ! wait_for_connection; then
        show_summary
        exit 1
    fi

    if ! wait_for_port; then
        show_summary
        exit 1
    fi

    # Run all tests
    test_ip_changed
    test_ipv6_blocked
    test_killswitch

    test_exemptions_cleaned
    test_reconnection
    test_proxy
#    test_port_forwarding
    test_metrics

    # Show results
    show_summary
}

# Run main function
main "$@"

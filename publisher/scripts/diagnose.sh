#!/bin/bash
# diagnose.sh — WireZTNA Publisher Self-Diagnostic Tool
#
# Validates the publisher's own health from its perspective:
#   1. Enrollment state (is it enrolled, does it have keys/config?)
#   2. WireGuard tunnel to broker (interface up, handshake, traffic)
#   3. Control plane connectivity (can it reach the API for heartbeats?)
#   4. IP forwarding (can it relay traffic to LAN?)
#   5. Local network reachability (can it reach exposed CIDRs?)
#   6. DNS resolution (can it resolve local names?)
#
# Usage:
#   ./diagnose.sh              # Full diagnostic
#   ./diagnose.sh --quick      # Quick check (skip ping tests)
#   ./diagnose.sh --json       # Machine-readable output
#
# Exit codes:
#   0 = All checks passed
#   1 = Critical failures (tunnel down, cannot reach broker)
#   2 = Warnings (degraded but partially functional)

set -euo pipefail

# ─── Configuration ───
CONTROL_PLANE_URL="${CONTROL_PLANE_URL:-http://localhost:8443}"
PUBLISHER_ID="${PUBLISHER_ID:-}"
WG_INTERFACE="${WG_INTERFACE:-wg-broker}"
CONFIG_DIR="${CONFIG_DIR:-/etc/wireguard}"
STATE_FILE="${CONFIG_DIR}/.enrolled"

# ─── Parse arguments ───
QUICK=false
JSON_OUTPUT=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --quick) QUICK=true; shift ;;
        --json) JSON_OUTPUT=true; shift ;;
        -h|--help)
            head -20 "$0" | tail -17
            exit 0
            ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

# ─── Output helpers ───
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

PASS_COUNT=0
WARN_COUNT=0
FAIL_COUNT=0
RESULTS=()

pass() {
    ((PASS_COUNT++))
    RESULTS+=("{\"test\":\"$1\",\"status\":\"pass\",\"detail\":\"$2\"}")
    if [ "$JSON_OUTPUT" = false ]; then
        echo -e "  ${GREEN}[PASS]${NC} $1 — $2"
    fi
}

warn() {
    ((WARN_COUNT++))
    RESULTS+=("{\"test\":\"$1\",\"status\":\"warn\",\"detail\":\"$2\"}")
    if [ "$JSON_OUTPUT" = false ]; then
        echo -e "  ${YELLOW}[WARN]${NC} $1 — $2"
    fi
}

fail() {
    ((FAIL_COUNT++))
    RESULTS+=("{\"test\":\"$1\",\"status\":\"fail\",\"detail\":\"$2\"}")
    if [ "$JSON_OUTPUT" = false ]; then
        echo -e "  ${RED}[FAIL]${NC} $1 — $2"
    fi
}

section() {
    if [ "$JSON_OUTPUT" = false ]; then
        echo ""
        echo -e "${BLUE}━━━ $1 ━━━${NC}"
    fi
}

# ─── Load publisher ID from state file if not in env ───
if [ -z "$PUBLISHER_ID" ] && [ -f "$STATE_FILE" ]; then
    PUBLISHER_ID=$(grep "publisher_id=" "$STATE_FILE" 2>/dev/null | cut -d= -f2 || true)
fi

if [ "$JSON_OUTPUT" = false ]; then
    echo "═══════════════════════════════════════════════════════"
    echo "  WireZTNA Publisher Diagnostic — $(date -Iseconds)"
    echo "  Publisher ID: ${PUBLISHER_ID:-NOT ENROLLED}"
    echo "  WG Interface: $WG_INTERFACE"
    echo "  Control Plane: $CONTROL_PLANE_URL"
    echo "═══════════════════════════════════════════════════════"
fi

# ════════════════════════════════════════════════════════════════
# 1. ENROLLMENT STATE
# ════════════════════════════════════════════════════════════════

section "1. Enrollment State"

# 1.1 State file exists
if [ -f "$STATE_FILE" ]; then
    ENROLLED_AT=$(grep "enrolled_at=" "$STATE_FILE" 2>/dev/null | cut -d= -f2 || echo "unknown")
    BROKER_ENDPOINT=$(grep "broker_endpoint=" "$STATE_FILE" 2>/dev/null | cut -d= -f2 || echo "unknown")
    pass "Enrollment" "Enrolled at $ENROLLED_AT (broker: $BROKER_ENDPOINT)"
else
    fail "Enrollment" "Not enrolled — $STATE_FILE does not exist. Run enroll.sh first"
fi

# 1.2 Publisher ID
if [ -n "$PUBLISHER_ID" ]; then
    pass "Publisher ID" "$PUBLISHER_ID"
else
    fail "Publisher ID" "No publisher ID found — enrollment may have failed"
fi

# 1.3 Private key exists
PRIVATE_KEY_FILE="${CONFIG_DIR}/publisher.key"
if [ -f "$PRIVATE_KEY_FILE" ]; then
    KEY_PERMS=$(stat -c '%a' "$PRIVATE_KEY_FILE" 2>/dev/null || stat -f '%Lp' "$PRIVATE_KEY_FILE" 2>/dev/null || echo "unknown")
    if [ "$KEY_PERMS" = "600" ]; then
        pass "Private key" "Exists with correct permissions (600)"
    else
        warn "Private key" "Exists but permissions are $KEY_PERMS (should be 600)"
    fi
else
    fail "Private key" "File not found at $PRIVATE_KEY_FILE"
fi

# 1.4 WireGuard config file
WG_CONF="${CONFIG_DIR}/${WG_INTERFACE}.conf"
if [ -f "$WG_CONF" ]; then
    # Verify it has the essential sections
    if grep -q "\[Interface\]" "$WG_CONF" && grep -q "\[Peer\]" "$WG_CONF"; then
        pass "WG config" "$WG_CONF present with [Interface] and [Peer] sections"
    else
        fail "WG config" "$WG_CONF exists but is malformed (missing sections)"
    fi
else
    fail "WG config" "Config file $WG_CONF not found"
fi

# ════════════════════════════════════════════════════════════════
# 2. WIREGUARD TUNNEL TO BROKER
# ════════════════════════════════════════════════════════════════

section "2. WireGuard Tunnel"

# 2.1 Interface exists
if ip link show "$WG_INTERFACE" &>/dev/null; then
    pass "Interface" "'$WG_INTERFACE' exists"
else
    fail "Interface" "'$WG_INTERFACE' does not exist — tunnel is NOT running"
    # Skip remaining WG checks
    QUICK=true
fi

# 2.2 Interface state
if ip link show "$WG_INTERFACE" 2>/dev/null | grep -q "state UP\|state UNKNOWN"; then
    pass "Interface state" "UP"
else
    if ip link show "$WG_INTERFACE" &>/dev/null; then
        fail "Interface state" "Interface exists but is DOWN"
    fi
fi

# 2.3 WireGuard peer configured
if command -v wg &>/dev/null; then
    PEER_COUNT=$(wg show "$WG_INTERFACE" peers 2>/dev/null | wc -l || echo 0)
    if [ "$PEER_COUNT" -gt 0 ]; then
        BROKER_PUBKEY=$(wg show "$WG_INTERFACE" peers 2>/dev/null | head -1)
        pass "Broker peer" "Configured (pubkey: ${BROKER_PUBKEY:0:12}...)"
    else
        fail "Broker peer" "No peers configured on $WG_INTERFACE — config is invalid"
    fi

    # 2.4 Listening port
    LISTEN_PORT=$(wg show "$WG_INTERFACE" listen-port 2>/dev/null || echo "")
    if [ -n "$LISTEN_PORT" ]; then
        pass "Listen port" "UDP port $LISTEN_PORT"
    else
        warn "Listen port" "Cannot determine listening port"
    fi

    # 2.5 Handshake freshness (THE critical check)
    HANDSHAKE_TS=$(wg show "$WG_INTERFACE" latest-handshakes 2>/dev/null | awk '{print $2}' | head -1)
    if [ -n "$HANDSHAKE_TS" ] && [ "${HANDSHAKE_TS:-0}" != "0" ]; then
        NOW=$(date +%s)
        HANDSHAKE_AGE=$((NOW - HANDSHAKE_TS))
        if [ "$HANDSHAKE_AGE" -lt 150 ]; then
            pass "Handshake" "Last handshake ${HANDSHAKE_AGE}s ago — tunnel ALIVE"
        elif [ "$HANDSHAKE_AGE" -lt 300 ]; then
            warn "Handshake" "Last handshake ${HANDSHAKE_AGE}s ago — getting stale, check broker"
        else
            fail "Handshake" "Last handshake ${HANDSHAKE_AGE}s ago — tunnel is DEAD"
        fi
    else
        fail "Handshake" "No handshake ever completed — broker unreachable or keys mismatch"
    fi

    # 2.6 Endpoint (is broker reachable?)
    ENDPOINT=$(wg show "$WG_INTERFACE" endpoints 2>/dev/null | awk '{print $2}' | head -1)
    if [ -n "$ENDPOINT" ] && [ "$ENDPOINT" != "(none)" ]; then
        pass "Broker endpoint" "$ENDPOINT"
    else
        warn "Broker endpoint" "No endpoint resolved — broker may not be reachable"
    fi

    # 2.7 Transfer stats
    TRANSFER=$(wg show "$WG_INTERFACE" transfer 2>/dev/null | head -1)
    if [ -n "$TRANSFER" ]; then
        RX=$(echo "$TRANSFER" | awk '{print $2}')
        TX=$(echo "$TRANSFER" | awk '{print $3}')
        if [ "${RX:-0}" = "0" ] && [ "${TX:-0}" = "0" ]; then
            warn "Traffic" "Zero bytes transferred — tunnel active but no data flowing"
        else
            RX_H=$(numfmt --to=iec "${RX:-0}" 2>/dev/null || echo "${RX:-0}B")
            TX_H=$(numfmt --to=iec "${TX:-0}" 2>/dev/null || echo "${TX:-0}B")
            pass "Traffic" "RX: $RX_H / TX: $TX_H"
        fi
    fi
else
    fail "wireguard-tools" "wg command not found — cannot inspect tunnel"
fi

# 2.8 Ping broker tunnel IP (10.100.X.1 — derived from config)
if [ "$QUICK" = false ]; then
    BROKER_TUNNEL_IP=$(grep -oP 'AllowedIPs\s*=\s*\K[0-9.]+' "$WG_CONF" 2>/dev/null | head -1 || true)
    # Try to extract from the state file if conf doesn't show it cleanly
    if [ -z "$BROKER_TUNNEL_IP" ]; then
        # Default broker tunnel IP pattern: 10.100.X.1 or 10.200.0.1
        BROKER_TUNNEL_IP="10.200.0.1"
    fi

    if ping -c 1 -W 3 "$BROKER_TUNNEL_IP" &>/dev/null; then
        pass "Tunnel ping" "Broker tunnel IP ($BROKER_TUNNEL_IP) reachable via tunnel"
    else
        # Try the overlay IP as fallback
        if ping -c 1 -W 3 "10.200.0.1" &>/dev/null; then
            pass "Tunnel ping" "Broker overlay IP (10.200.0.1) reachable via tunnel"
        else
            fail "Tunnel ping" "Cannot ping broker through tunnel — tunnel may be one-way broken"
        fi
    fi
fi

# ════════════════════════════════════════════════════════════════
# 3. CONTROL PLANE CONNECTIVITY
# ════════════════════════════════════════════════════════════════

section "3. Control Plane API"

# 3.1 Health check
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 5 "${CONTROL_PLANE_URL}/health" 2>/dev/null || echo "000")
if [ "$HTTP_CODE" = "200" ]; then
    pass "API reachable" "HTTP 200 from ${CONTROL_PLANE_URL}/health"
elif [ "$HTTP_CODE" = "000" ]; then
    fail "API reachable" "Cannot reach control plane at $CONTROL_PLANE_URL (connection refused/timeout)"
else
    warn "API reachable" "HTTP $HTTP_CODE from control plane (expected 200)"
fi

# 3.2 Heartbeat endpoint (simulate)
if [ -n "$PUBLISHER_ID" ] && [ "$HTTP_CODE" = "200" ]; then
    HB_CODE=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 5 \
        -X POST \
        -H "Content-Type: application/json" \
        -d "{\"publisher_id\":\"$PUBLISHER_ID\",\"timestamp\":\"$(date -Iseconds)\",\"uptime_seconds\":0,\"status\":\"online\"}" \
        "${CONTROL_PLANE_URL}/api/v1/publishers/${PUBLISHER_ID}/heartbeat" 2>/dev/null || echo "000")
    if [ "$HB_CODE" = "204" ] || [ "$HB_CODE" = "200" ]; then
        pass "Heartbeat" "Heartbeat accepted by control plane (HTTP $HB_CODE)"
    elif [ "$HB_CODE" = "404" ]; then
        fail "Heartbeat" "Publisher ID not found on control plane — may need re-enrollment"
    else
        warn "Heartbeat" "Heartbeat returned HTTP $HB_CODE (expected 204)"
    fi
fi

# ════════════════════════════════════════════════════════════════
# 4. IP FORWARDING & ROUTING
# ════════════════════════════════════════════════════════════════

section "4. IP Forwarding & Routing"

# 4.1 IP forwarding enabled
IP_FWD=$(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo "0")
if [ "$IP_FWD" = "1" ]; then
    pass "IP forwarding" "Enabled (net.ipv4.ip_forward=1)"
else
    fail "IP forwarding" "DISABLED — publisher cannot relay traffic to local network"
fi

# 4.2 Default route exists (publisher needs internet to reach broker)
if ip route show default &>/dev/null && ip route show default | grep -q "default"; then
    DEFAULT_GW=$(ip route show default | awk '{print $3}' | head -1)
    DEFAULT_IF=$(ip route show default | awk '{print $5}' | head -1)
    pass "Default route" "via $DEFAULT_GW dev $DEFAULT_IF"
else
    warn "Default route" "No default route — publisher may not reach broker over internet"
fi

# 4.3 WireGuard tunnel IP assigned
TUNNEL_IP=$(ip addr show "$WG_INTERFACE" 2>/dev/null | grep -oP 'inet \K[0-9.]+' | head -1 || true)
if [ -n "$TUNNEL_IP" ]; then
    pass "Tunnel IP" "$TUNNEL_IP assigned to $WG_INTERFACE"
else
    if ip link show "$WG_INTERFACE" &>/dev/null; then
        fail "Tunnel IP" "No IP address assigned to $WG_INTERFACE"
    fi
fi

# ════════════════════════════════════════════════════════════════
# 5. LOCAL NETWORK REACHABILITY
# ════════════════════════════════════════════════════════════════

section "5. Local Network (exposed CIDRs)"

if [ "$QUICK" = false ]; then
    # Try to determine what CIDRs this publisher exposes
    # Check from the WG config or environment
    # The publisher's exposed_cidrs are in the control plane, but locally
    # we can test our own default gateway and common local targets

    # Test: can we reach our own gateway?
    if [ -n "${DEFAULT_GW:-}" ]; then
        if ping -c 1 -W 2 "$DEFAULT_GW" &>/dev/null; then
            pass "Gateway ping" "Default gateway ($DEFAULT_GW) reachable"
        else
            fail "Gateway ping" "Cannot ping default gateway ($DEFAULT_GW) — local network issue"
        fi
    fi

    # Test: resolve a local hostname via our DNS
    LOCAL_DNS=$(grep -m1 '^nameserver' /etc/resolv.conf 2>/dev/null | awk '{print $2}' || true)
    if [ -n "$LOCAL_DNS" ] && [ "$LOCAL_DNS" != "127.0.0.11" ]; then
        if command -v dig &>/dev/null; then
            if dig +short +time=2 +tries=1 @"$LOCAL_DNS" google.com &>/dev/null; then
                pass "Local DNS" "DNS server $LOCAL_DNS responds to queries"
            else
                warn "Local DNS" "DNS server $LOCAL_DNS not responding"
            fi
        elif command -v nslookup &>/dev/null; then
            if nslookup google.com "$LOCAL_DNS" &>/dev/null; then
                pass "Local DNS" "DNS server $LOCAL_DNS responds to queries"
            else
                warn "Local DNS" "DNS server $LOCAL_DNS not responding"
            fi
        fi
    fi

    # Test: are any local interfaces (besides WG and lo) up?
    LOCAL_IFS=$(ip -o link show 2>/dev/null | grep -v "lo\|$WG_INTERFACE\|docker\|veth" | grep "state UP" | awk -F': ' '{print $2}' || true)
    if [ -n "$LOCAL_IFS" ]; then
        pass "Local interfaces" "Active: $(echo $LOCAL_IFS | tr '\n' ' ')"
    else
        warn "Local interfaces" "No non-tunnel interfaces are UP — publisher may be isolated"
    fi
else
    pass "Local network" "Skipped (--quick mode)"
fi

# ════════════════════════════════════════════════════════════════
# 6. HEARTBEAT PROCESS
# ════════════════════════════════════════════════════════════════

section "6. Heartbeat Process"

# Check if heartbeat loop is running
HB_PID=$(pgrep -f "heartbeat.sh" 2>/dev/null || pgrep -f "heartbeat.*loop" 2>/dev/null || echo "")
if [ -n "$HB_PID" ]; then
    pass "Heartbeat process" "Running (PID: $HB_PID)"
else
    warn "Heartbeat process" "Not running — control plane won't know this publisher is alive"
fi

# ════════════════════════════════════════════════════════════════
# SUMMARY
# ════════════════════════════════════════════════════════════════

if [ "$JSON_OUTPUT" = true ]; then
    echo "{"
    echo "  \"timestamp\": \"$(date -Iseconds)\","
    echo "  \"publisher_id\": \"${PUBLISHER_ID:-null}\","
    echo "  \"summary\": {\"pass\": $PASS_COUNT, \"warn\": $WARN_COUNT, \"fail\": $FAIL_COUNT},"
    echo "  \"results\": ["
    for i in "${!RESULTS[@]}"; do
        if [ $i -lt $((${#RESULTS[@]} - 1)) ]; then
            echo "    ${RESULTS[$i]},"
        else
            echo "    ${RESULTS[$i]}"
        fi
    done
    echo "  ]"
    echo "}"
else
    echo ""
    echo "═══════════════════════════════════════════════════════"
    echo -e "  RESULTS: ${GREEN}$PASS_COUNT passed${NC}, ${YELLOW}$WARN_COUNT warnings${NC}, ${RED}$FAIL_COUNT failures${NC}"
    echo "═══════════════════════════════════════════════════════"

    if [ "$FAIL_COUNT" -gt 0 ]; then
        echo ""
        echo "  Troubleshooting tips:"
        echo "  • Not enrolled      → Set ENROLLMENT_TOKEN and run enroll.sh"
        echo "  • No handshake      → Check broker is running, UDP port open, keys correct"
        echo "  • Tunnel dead       → Try: wg-quick down $WG_INTERFACE && wg-quick up $WG_INTERFACE"
        echo "  • IP forward off    → Run: sysctl -w net.ipv4.ip_forward=1"
        echo "  • API unreachable   → Check CONTROL_PLANE_URL, network/firewall between publisher and control plane"
        echo "  • Publisher not found → Re-enroll: rm $STATE_FILE && run enroll.sh"
        echo ""
    fi
fi

# Exit code
if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
elif [ "$WARN_COUNT" -gt 0 ]; then
    exit 2
else
    exit 0
fi

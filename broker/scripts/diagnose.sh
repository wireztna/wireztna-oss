#!/bin/bash
# diagnose.sh — WireZTNA Broker End-to-End Diagnostic Tool
#
# Validates each layer of the connection path from broker perspective:
#   1. Control Plane connectivity
#   2. Broker WireGuard interface (wg-clients)
#   3. Publisher namespaces and tunnels
#   4. Client peers and sessions
#   5. nftables / policy routing
#   6. DNS resolution (CoreDNS → publisher namespaces)
#
# Usage:
#   sudo ./diagnose.sh                    # Full diagnostic
#   sudo ./diagnose.sh --quick            # Quick check (skip slow tests)
#   sudo ./diagnose.sh --publisher <id>   # Focus on one publisher
#   sudo ./diagnose.sh --client <ip>      # Focus on one client overlay IP
#   sudo ./diagnose.sh --json             # Machine-readable output
#
# Exit codes:
#   0 = All checks passed
#   1 = Critical failures detected
#   2 = Warnings (degraded but functional)

set -euo pipefail

# ─── Configuration ───
CONTROL_PLANE_URL="${CONTROL_PLANE_URL:-http://localhost:8443}"
BROKER_ID="${BROKER_ID:-broker-01}"
BROKER_API_KEY="${BROKER_API_KEY:-}"
WG_CLIENTS_IF="wg-clients"
CONFIG_DIR="/etc/wireztna"

# ─── Parse arguments ───
QUICK=false
FOCUS_PUBLISHER=""
FOCUS_CLIENT=""
JSON_OUTPUT=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --quick) QUICK=true; shift ;;
        --publisher) FOCUS_PUBLISHER="$2"; shift 2 ;;
        --client) FOCUS_CLIENT="$2"; shift 2 ;;
        --json) JSON_OUTPUT=true; shift ;;
        -h|--help)
            head -25 "$0" | tail -22
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
NC='\033[0m' # No Color

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

# ─── Pre-flight ───
if [ "$(id -u)" -ne 0 ] && [ "$JSON_OUTPUT" = false ]; then
    echo "WARNING: Running without root — some checks will be skipped"
    echo "         Run with: sudo ./diagnose.sh"
    echo ""
fi

if [ "$JSON_OUTPUT" = false ]; then
    echo "═══════════════════════════════════════════════════════"
    echo "  WireZTNA Broker Diagnostic — $(date -Iseconds)"
    echo "  Broker ID: $BROKER_ID"
    echo "  Control Plane: $CONTROL_PLANE_URL"
    echo "═══════════════════════════════════════════════════════"
fi

# ════════════════════════════════════════════════════════════════
# 1. CONTROL PLANE CONNECTIVITY
# ════════════════════════════════════════════════════════════════

section "1. Control Plane Connectivity"

# 1.1 Health endpoint
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 5 "${CONTROL_PLANE_URL}/health" 2>/dev/null || echo "000")
if [ "$HTTP_CODE" = "200" ]; then
    pass "API health" "HTTP 200 OK"
elif [ "$HTTP_CODE" = "000" ]; then
    fail "API health" "Connection refused or timeout — control plane unreachable at $CONTROL_PLANE_URL"
else
    fail "API health" "HTTP $HTTP_CODE (expected 200)"
fi

# 1.2 Broker config fetch
if [ "$HTTP_CODE" = "200" ]; then
    CONFIG_RESPONSE=$(curl -s -w "\n%{http_code}" --connect-timeout 5 \
        -H "X-API-Key: $BROKER_API_KEY" \
        "${CONTROL_PLANE_URL}/api/v1/brokers/${BROKER_ID}/config" 2>/dev/null || echo -e "\n000")
    CONFIG_HTTP=$(echo "$CONFIG_RESPONSE" | tail -1)
    CONFIG_BODY=$(echo "$CONFIG_RESPONSE" | sed '$d')

    if [ "$CONFIG_HTTP" = "200" ]; then
        PUB_COUNT=$(echo "$CONFIG_BODY" | grep -o '"publishers":\[' | wc -l || echo 0)
        CLIENT_COUNT=$(echo "$CONFIG_BODY" | grep -o '"public_key"' | wc -l || echo 0)
        pass "Broker config" "Fetched OK (publishers + clients in response)"
    else
        fail "Broker config" "HTTP $CONFIG_HTTP fetching broker config — reconciler cannot work"
    fi
fi

# ════════════════════════════════════════════════════════════════
# 2. BROKER WIREGUARD INTERFACE (wg-clients)
# ════════════════════════════════════════════════════════════════

section "2. Broker WireGuard Interface"

# 2.1 Interface exists
if ip link show "$WG_CLIENTS_IF" &>/dev/null; then
    pass "wg-clients interface" "Interface exists and is visible"
else
    fail "wg-clients interface" "Interface '$WG_CLIENTS_IF' does not exist — clients cannot connect"
fi

# 2.2 Interface is UP
if ip link show "$WG_CLIENTS_IF" 2>/dev/null | grep -q "state UP\|state UNKNOWN"; then
    pass "wg-clients state" "Interface is UP"
else
    if ip link show "$WG_CLIENTS_IF" &>/dev/null; then
        fail "wg-clients state" "Interface exists but is DOWN"
    fi
fi

# 2.3 WireGuard listening
if command -v wg &>/dev/null; then
    WG_PORT=$(wg show "$WG_CLIENTS_IF" listen-port 2>/dev/null || echo "")
    if [ -n "$WG_PORT" ]; then
        pass "wg-clients listen" "Listening on UDP port $WG_PORT"
    else
        fail "wg-clients listen" "Cannot determine listening port"
    fi
    
    # 2.4 Peer count
    PEER_COUNT=$(wg show "$WG_CLIENTS_IF" peers 2>/dev/null | wc -l || echo 0)
    if [ "$PEER_COUNT" -gt 0 ]; then
        pass "Client peers" "$PEER_COUNT peer(s) configured"
    else
        warn "Client peers" "No client peers configured — no users connected or reconciler hasn't run"
    fi
else
    fail "wireguard-tools" "wg command not found — cannot inspect WireGuard state"
fi

# ════════════════════════════════════════════════════════════════
# 3. PUBLISHER NAMESPACES & TUNNELS
# ════════════════════════════════════════════════════════════════

section "3. Publisher Namespaces & Tunnels"

# Get all namespaces
NAMESPACES=$(ip netns list 2>/dev/null | awk '{print $1}' | grep "^ns-" || true)
NS_COUNT=$(echo "$NAMESPACES" | grep -c "^ns-" 2>/dev/null || echo 0)

if [ "$NS_COUNT" -gt 0 ]; then
    pass "Namespaces" "$NS_COUNT publisher namespace(s) found"
else
    warn "Namespaces" "No publisher namespaces exist — no publishers enrolled/reconciled yet"
fi

# Check each namespace
for NS in $NAMESPACES; do
    # Skip if focusing on a specific publisher
    if [ -n "$FOCUS_PUBLISHER" ] && ! echo "$NS" | grep -q "$FOCUS_PUBLISHER"; then
        continue
    fi

    if [ "$JSON_OUTPUT" = false ]; then
        echo ""
        echo -e "  ${BLUE}Namespace: $NS${NC}"
    fi

    # 3.1 Veth pair connectivity
    ID_SHORT="${NS#ns-}"
    ID_VETH="${ID_SHORT:0:6}"
    VETH_HOST="v${ID_VETH}-h"

    if ip link show "$VETH_HOST" &>/dev/null; then
        pass "$NS/veth" "Host veth '$VETH_HOST' exists and linked"
    else
        fail "$NS/veth" "Host veth '$VETH_HOST' missing — namespace is orphaned"
    fi

    # 3.2 WireGuard interface inside namespace
    WG_IF_NS=$(ip netns exec "$NS" wg show interfaces 2>/dev/null || echo "")
    if [ -n "$WG_IF_NS" ]; then
        pass "$NS/wg-interface" "WireGuard interface: $WG_IF_NS"

        # 3.3 Handshake freshness (critical check)
        HANDSHAKE_RAW=$(ip netns exec "$NS" wg show "$WG_IF_NS" latest-handshakes 2>/dev/null | awk '{print $2}' | head -1)
        if [ -n "$HANDSHAKE_RAW" ] && [ "$HANDSHAKE_RAW" != "0" ]; then
            NOW=$(date +%s)
            HANDSHAKE_AGE=$((NOW - HANDSHAKE_RAW))
            if [ "$HANDSHAKE_AGE" -lt 150 ]; then
                pass "$NS/handshake" "Last handshake ${HANDSHAKE_AGE}s ago (healthy)"
            elif [ "$HANDSHAKE_AGE" -lt 300 ]; then
                warn "$NS/handshake" "Last handshake ${HANDSHAKE_AGE}s ago (stale, may be losing connectivity)"
            else
                fail "$NS/handshake" "Last handshake ${HANDSHAKE_AGE}s ago — tunnel is DEAD, publisher unreachable"
            fi
        else
            fail "$NS/handshake" "No handshake ever completed — publisher never connected or wrong keys"
        fi

        # 3.4 Tunnel IP ping (only if not in quick mode)
        if [ "$QUICK" = false ]; then
            # Get publisher tunnel IP from WG allowed-ips
            PUB_TUNNEL_IP=$(ip netns exec "$NS" wg show "$WG_IF_NS" allowed-ips 2>/dev/null | awk '{print $2}' | head -1 | cut -d/ -f1)
            if [ -n "$PUB_TUNNEL_IP" ]; then
                if ip netns exec "$NS" ping -c 1 -W 2 "$PUB_TUNNEL_IP" &>/dev/null; then
                    pass "$NS/tunnel-ping" "Tunnel ping to publisher ($PUB_TUNNEL_IP) successful"
                else
                    fail "$NS/tunnel-ping" "Cannot ping publisher at $PUB_TUNNEL_IP through tunnel"
                fi
            fi
        fi

        # 3.5 Transfer stats (detect zero traffic)
        TRANSFER=$(ip netns exec "$NS" wg show "$WG_IF_NS" transfer 2>/dev/null | head -1)
        if [ -n "$TRANSFER" ]; then
            RX=$(echo "$TRANSFER" | awk '{print $2}')
            TX=$(echo "$TRANSFER" | awk '{print $3}')
            if [ "${RX:-0}" = "0" ] && [ "${TX:-0}" = "0" ]; then
                warn "$NS/traffic" "Zero bytes transferred — tunnel configured but no traffic ever flowed"
            else
                RX_H=$(numfmt --to=iec "${RX:-0}" 2>/dev/null || echo "${RX:-0}B")
                TX_H=$(numfmt --to=iec "${TX:-0}" 2>/dev/null || echo "${TX:-0}B")
                pass "$NS/traffic" "RX: $RX_H / TX: $TX_H"
            fi
        fi
    else
        fail "$NS/wg-interface" "No WireGuard interface inside namespace — tunnel not created"
    fi
done

# ════════════════════════════════════════════════════════════════
# 4. CLIENT PEER HEALTH
# ════════════════════════════════════════════════════════════════

section "4. Client Peer Health"

if command -v wg &>/dev/null && wg show "$WG_CLIENTS_IF" &>/dev/null 2>&1; then
    # Parse all client peers
    CLIENT_DUMP=$(wg show "$WG_CLIENTS_IF" dump 2>/dev/null | tail -n +2)
    CONNECTED=0
    STALE=0
    DEAD=0

    while IFS=$'\t' read -r pubkey psk endpoint allowed_ips handshake rx tx persistent; do
        [ -z "$pubkey" ] && continue

        # Filter by client IP if specified
        if [ -n "$FOCUS_CLIENT" ] && ! echo "$allowed_ips" | grep -q "$FOCUS_CLIENT"; then
            continue
        fi

        CLIENT_IP=$(echo "$allowed_ips" | cut -d/ -f1)
        NOW=$(date +%s)

        if [ "${handshake:-0}" = "0" ]; then
            ((DEAD++))
            if [ -n "$FOCUS_CLIENT" ] || [ "$PEER_COUNT" -le 10 ]; then
                fail "Client $CLIENT_IP" "Never completed handshake — config issue or client offline"
            fi
        else
            AGE=$((NOW - handshake))
            if [ "$AGE" -lt 180 ]; then
                ((CONNECTED++))
                if [ -n "$FOCUS_CLIENT" ]; then
                    pass "Client $CLIENT_IP" "Connected (handshake ${AGE}s ago, endpoint: ${endpoint:-unknown})"
                fi
            else
                ((STALE++))
                if [ -n "$FOCUS_CLIENT" ] || [ "$PEER_COUNT" -le 10 ]; then
                    warn "Client $CLIENT_IP" "Stale handshake (${AGE}s ago) — client likely offline"
                fi
            fi
        fi

        # Check PSK presence (critical for session model)
        if [ "$psk" = "(none)" ] || [ -z "$psk" ]; then
            if [ -n "$FOCUS_CLIENT" ] || [ "$PEER_COUNT" -le 10 ]; then
                fail "Client $CLIENT_IP/psk" "No PSK set — session not active, handshake will fail"
            fi
        fi
    done <<< "$CLIENT_DUMP"

    if [ -z "$FOCUS_CLIENT" ]; then
        TOTAL=$((CONNECTED + STALE + DEAD))
        if [ "$TOTAL" -gt 0 ]; then
            if [ "$DEAD" -gt 0 ] || [ "$STALE" -gt "$CONNECTED" ]; then
                warn "Client summary" "$CONNECTED connected, $STALE stale, $DEAD never-handshaked (of $TOTAL)"
            else
                pass "Client summary" "$CONNECTED connected, $STALE stale, $DEAD never-handshaked (of $TOTAL)"
            fi
        fi
    fi
fi

# ════════════════════════════════════════════════════════════════
# 5. NFTABLES & POLICY ROUTING
# ════════════════════════════════════════════════════════════════

section "5. Firewall & Policy Routing"

# 5.1 nftables table exists
if command -v nft &>/dev/null; then
    if nft list table inet wireztna &>/dev/null 2>&1; then
        FORWARD_RULES=$(nft list chain inet wireztna forward 2>/dev/null | grep -c "accept" || echo 0)
        MANGLE_RULES=$(nft list chain inet wireztna prerouting 2>/dev/null | grep -c "mark set" || echo 0)
        pass "nftables" "Table 'inet wireztna' active: $FORWARD_RULES forward rules, $MANGLE_RULES mangle rules"

        if [ "$FORWARD_RULES" -eq 0 ] && [ "$NS_COUNT" -gt 0 ]; then
            warn "nftables/forward" "No forward ACCEPT rules — clients cannot reach any publisher"
        fi
    else
        if [ "$NS_COUNT" -gt 0 ]; then
            fail "nftables" "Table 'inet wireztna' does not exist — no access policy enforced"
        else
            warn "nftables" "Table 'inet wireztna' not yet created (no publishers reconciled)"
        fi
    fi
else
    warn "nft command" "nft not found — cannot verify firewall rules"
fi

# 5.2 Policy routing (ip rules)
POLICY_RULES=$(ip rule list 2>/dev/null | grep -c "fwmark" || echo 0)
if [ "$POLICY_RULES" -gt 0 ]; then
    pass "Policy routing" "$POLICY_RULES fwmark-based ip rules active"
elif [ "$NS_COUNT" -gt 0 ]; then
    fail "Policy routing" "No fwmark ip rules — traffic cannot reach publisher namespaces"
else
    pass "Policy routing" "No rules needed (no publishers configured)"
fi

# 5.3 IP forwarding enabled on host
if [ "$(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null)" = "1" ]; then
    pass "IP forwarding" "Enabled on host"
else
    fail "IP forwarding" "DISABLED — broker cannot forward any traffic"
fi

# ════════════════════════════════════════════════════════════════
# 6. DNS (CoreDNS)
# ════════════════════════════════════════════════════════════════

section "6. DNS Resolution"

# 6.1 CoreDNS running
COREDNS_PID=$(pidof coredns 2>/dev/null || echo "")
if [ -n "$COREDNS_PID" ]; then
    pass "CoreDNS" "Running (PID: $COREDNS_PID)"
else
    # Check if it's in a container
    if docker ps 2>/dev/null | grep -q coredns; then
        pass "CoreDNS" "Running in Docker container"
    else
        warn "CoreDNS" "Process not found — private DNS resolution will fail"
    fi
fi

# 6.2 CoreDNS listening on port 53
if command -v ss &>/dev/null; then
    if ss -ulnp 2>/dev/null | grep -q ":53 "; then
        pass "DNS port 53" "UDP port 53 is listening"
    else
        warn "DNS port 53" "Nothing listening on UDP/53 — DNS queries will fail"
    fi
fi

# 6.3 DNS proxy processes for publishers
DNS_PROXIES=$(ls /var/run/wireztna/dns-proxy-*.pid 2>/dev/null | wc -l || echo 0)
if [ "$DNS_PROXIES" -gt 0 ]; then
    ALIVE=0
    for pidfile in /var/run/wireztna/dns-proxy-*.pid; do
        PID=$(cat "$pidfile" 2>/dev/null || echo "")
        if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
            ((ALIVE++))
        fi
    done
    if [ "$ALIVE" -eq "$DNS_PROXIES" ]; then
        pass "DNS proxies" "$ALIVE/$DNS_PROXIES DNS proxy processes alive"
    else
        warn "DNS proxies" "Only $ALIVE/$DNS_PROXIES DNS proxy processes alive"
    fi
fi

# 6.4 Quick DNS resolution test (if not in quick mode)
if [ "$QUICK" = false ] && command -v dig &>/dev/null; then
    # Test basic DNS (public)
    if dig +short +time=2 google.com @127.0.0.1 &>/dev/null; then
        pass "DNS resolution" "Public DNS resolution via CoreDNS works"
    elif [ -n "$COREDNS_PID" ]; then
        warn "DNS resolution" "CoreDNS running but public resolution failed"
    fi
fi

# ════════════════════════════════════════════════════════════════
# 7. RECONCILER STATE
# ════════════════════════════════════════════════════════════════

section "7. Reconciler State"

# 7.1 Namespace map file
NS_MAP_FILE="$CONFIG_DIR/namespace-map.json"
if [ -f "$NS_MAP_FILE" ]; then
    NS_MAP_ENTRIES=$(grep -c "publisher_id" "$NS_MAP_FILE" 2>/dev/null || echo 0)
    pass "Namespace map" "$NS_MAP_ENTRIES entries in $NS_MAP_FILE"
else
    if [ "$NS_COUNT" -gt 0 ]; then
        warn "Namespace map" "File missing but namespaces exist — reconciler may not have run"
    else
        pass "Namespace map" "Not yet created (no publishers reconciled)"
    fi
fi

# 7.2 nftables rules file
NFT_FILE="$CONFIG_DIR/nftables-wireztna.nft"
if [ -f "$NFT_FILE" ]; then
    NFT_AGE=$(( $(date +%s) - $(stat -c %Y "$NFT_FILE" 2>/dev/null || stat -f %m "$NFT_FILE" 2>/dev/null || echo 0) ))
    if [ "$NFT_AGE" -lt 60 ]; then
        pass "Rules file" "Last updated ${NFT_AGE}s ago (fresh)"
    elif [ "$NFT_AGE" -lt 300 ]; then
        pass "Rules file" "Last updated ${NFT_AGE}s ago"
    else
        warn "Rules file" "Last updated ${NFT_AGE}s ago — reconciler may be stuck"
    fi
fi

# 7.3 Check if broker agent process is running
RECONCILER_PID=$(pgrep -f "reconciler" 2>/dev/null || pgrep -f "broker.agent" 2>/dev/null || echo "")
if [ -n "$RECONCILER_PID" ]; then
    pass "Broker agent" "Running (PID: $RECONCILER_PID)"
else
    # Might be in Docker
    if docker ps 2>/dev/null | grep -q "broker"; then
        pass "Broker agent" "Running in Docker container"
    else
        warn "Broker agent" "Process not found — state will not be reconciled"
    fi
fi

# ════════════════════════════════════════════════════════════════
# SUMMARY
# ════════════════════════════════════════════════════════════════

if [ "$JSON_OUTPUT" = true ]; then
    echo "{"
    echo "  \"timestamp\": \"$(date -Iseconds)\","
    echo "  \"broker_id\": \"$BROKER_ID\","
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
        echo "  Troubleshooting tips for failures:"
        echo "  • Control plane unreachable → check Docker/service status, firewall"
        echo "  • No handshake → wrong keys, NAT/firewall blocking UDP, or peer offline"
        echo "  • No PSK → client needs to call POST /sessions/renew"
        echo "  • No nftables rules → reconciler not running or crashed"
        echo "  • IP forwarding off → run: sysctl -w net.ipv4.ip_forward=1"
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

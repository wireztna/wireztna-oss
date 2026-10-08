#!/bin/bash
# entrypoint.sh — Publisher container entrypoint
#
# Handles:
# 1. Enrollment (if not already enrolled)
# 2. Start WireGuard tunnel (handle host-mode restarts gracefully)
# 3. Start heartbeat loop
# 4. Keep container running

set -euo pipefail

echo "═══════════════════════════════════════════"
echo "  WireZTNA Publisher Agent"
echo "  $(date -Iseconds)"
echo "═══════════════════════════════════════════"

# ─── Enable IP forwarding ───
sysctl -w net.ipv4.ip_forward=1 2>/dev/null || echo "[WARN] Cannot set sysctl (need --cap-add=NET_ADMIN)"

# ─── Setup traffic forwarding (masquerade tunnel traffic to local network) ───
# When the broker sends traffic through the WG tunnel destined for the local VPC
# (e.g., DNS queries to 10.50.0.2, or client traffic to 10.50.1.x), the publisher
# must forward it to the local interface and masquerade the source IP so replies
# come back through the publisher.
echo "[*] Configuring traffic forwarding..."

# Detect the main network interface (first non-lo, non-wg interface with a default route)
MAIN_IF=$(ip route | grep '^default' | awk '{print $5}' | head -1)
if [ -z "$MAIN_IF" ]; then
    MAIN_IF="eth0"
fi
WG_INTERFACE="${WG_INTERFACE:-wg-broker}"

# Masquerade traffic from broker tunnel going out to local network
iptables -t nat -C POSTROUTING -s 10.100.0.0/16 -o "$MAIN_IF" -j MASQUERADE 2>/dev/null \
    || iptables -t nat -A POSTROUTING -s 10.100.0.0/16 -o "$MAIN_IF" -j MASQUERADE
iptables -t nat -C POSTROUTING -s 10.200.0.0/16 -o "$MAIN_IF" -j MASQUERADE 2>/dev/null \
    || iptables -t nat -A POSTROUTING -s 10.200.0.0/16 -o "$MAIN_IF" -j MASQUERADE

# Allow forwarding between WG tunnel and local network
iptables -C FORWARD -i "$WG_INTERFACE" -o "$MAIN_IF" -j ACCEPT 2>/dev/null \
    || iptables -I FORWARD 1 -i "$WG_INTERFACE" -o "$MAIN_IF" -j ACCEPT
iptables -C FORWARD -i "$MAIN_IF" -o "$WG_INTERFACE" -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null \
    || iptables -I FORWARD 2 -i "$MAIN_IF" -o "$WG_INTERFACE" -m state --state RELATED,ESTABLISHED -j ACCEPT

echo "[✓] Forwarding configured ($WG_INTERFACE ↔ $MAIN_IF with masquerade)"

# ─── Enrollment ───
/opt/wireztna/enroll.sh

# ─── Ensure tunnel is up (handle host-mode interface persistence) ───
WG_INTERFACE="${WG_INTERFACE:-wg-broker}"
if wg show "$WG_INTERFACE" &>/dev/null; then
    echo "[✓] Tunnel already active (interface persisted from previous run)"
    wg show "$WG_INTERFACE"
else
    echo "[*] Starting WireGuard tunnel..."
    # Interface might exist as a stale link — clean up first
    wg-quick down "$WG_INTERFACE" 2>/dev/null || true
    ip link del "$WG_INTERFACE" 2>/dev/null || true
    sleep 1
    wg-quick up "$WG_INTERFACE"
    echo "[✓] Tunnel started"
    wg show "$WG_INTERFACE"
fi

# ─── Start heartbeat in background ───
echo "[*] Starting heartbeat reporter..."
/opt/wireztna/heartbeat.sh --loop &
HEARTBEAT_PID=$!

# ─── Handle shutdown gracefully ───
trap_handler() {
    echo "[*] Shutting down publisher..."
    kill $HEARTBEAT_PID 2>/dev/null || true
    # Don't bring down WG on stop — in host-mode it would disrupt
    # other containers/processes using the interface. Let it persist.
    # wg-quick down "$WG_INTERFACE" 2>/dev/null || true
    echo "[✓] Publisher stopped (WG interface left active on host)"
    exit 0
}
trap trap_handler SIGTERM SIGINT

echo ""
echo "═══════════════════════════════════════════"
echo "  Publisher running. Waiting for traffic..."
echo "═══════════════════════════════════════════"

# ─── Keep container alive ───
wait $HEARTBEAT_PID

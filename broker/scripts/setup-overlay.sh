#!/bin/bash
# setup-overlay.sh — Configure the WireGuard client overlay interface on the broker
# This creates the wg-clients interface that all ZTNA clients connect to.
#
# Usage: sudo ./setup-overlay.sh
#
# Environment:
#   BROKER_WG_PORT      - WireGuard listen port (default: 51820)
#   BROKER_OVERLAY_IP   - Broker IP on overlay (default: 10.200.0.1)
#   BROKER_OVERLAY_MASK - Overlay network mask (default: 16)
#   WG_PRIVATE_KEY_FILE - Path to broker private key (default: /etc/wireguard/broker.key)

set -euo pipefail

# ─── Configuration ───
WG_PORT="${BROKER_WG_PORT:-51820}"
OVERLAY_IP="${BROKER_OVERLAY_IP:-10.200.0.1}"
OVERLAY_MASK="${BROKER_OVERLAY_MASK:-16}"
WG_INTERFACE="wg-clients"
KEY_DIR="/etc/wireguard"
PRIVATE_KEY_FILE="${WG_PRIVATE_KEY_FILE:-${KEY_DIR}/broker.key}"
PUBLIC_KEY_FILE="${KEY_DIR}/broker.pub"

echo "═══════════════════════════════════════════"
echo "  WireZTNA Broker — Overlay Setup"
echo "═══════════════════════════════════════════"

# ─── Ensure WireGuard is available ───
if ! command -v wg &>/dev/null; then
    echo "ERROR: wireguard-tools not installed. Run: apt install wireguard-tools"
    exit 1
fi

# ─── Generate keypair if not exists ───
mkdir -p "$KEY_DIR"
chmod 700 "$KEY_DIR"

if [ ! -f "$PRIVATE_KEY_FILE" ]; then
    echo "[*] Generating broker WireGuard keypair..."
    wg genkey | tee "$PRIVATE_KEY_FILE" | wg pubkey > "$PUBLIC_KEY_FILE"
    chmod 600 "$PRIVATE_KEY_FILE"
    echo "[✓] Keypair generated"
    echo "    Private: $PRIVATE_KEY_FILE"
    echo "    Public:  $PUBLIC_KEY_FILE"
else
    echo "[✓] Existing keypair found at $PRIVATE_KEY_FILE"
    # Regenerate public key from private (in case pub file is missing)
    wg pubkey < "$PRIVATE_KEY_FILE" > "$PUBLIC_KEY_FILE"
fi

echo "    Public Key: $(cat "$PUBLIC_KEY_FILE")"

# ─── Create WireGuard interface ───
if ip link show "$WG_INTERFACE" &>/dev/null; then
    echo "[*] Interface $WG_INTERFACE already exists, reconfiguring..."
    ip link set "$WG_INTERFACE" down
    ip link delete "$WG_INTERFACE"
fi

echo "[*] Creating interface $WG_INTERFACE..."
ip link add dev "$WG_INTERFACE" type wireguard

# ─── Configure WireGuard ───
wg set "$WG_INTERFACE" \
    listen-port "$WG_PORT" \
    private-key "$PRIVATE_KEY_FILE"

# ─── Assign IP and bring up ───
ip addr add "${OVERLAY_IP}/${OVERLAY_MASK}" dev "$WG_INTERFACE"
ip link set "$WG_INTERFACE" up

# ─── Enable IP forwarding ───
sysctl -w net.ipv4.ip_forward=1 > /dev/null
echo "net.ipv4.ip_forward=1" > /etc/sysctl.d/99-wireztna.conf

echo ""
echo "[✓] Overlay interface configured:"
echo "    Interface:  $WG_INTERFACE"
echo "    Address:    ${OVERLAY_IP}/${OVERLAY_MASK}"
echo "    Port:       $WG_PORT"
echo "    Public Key: $(cat "$PUBLIC_KEY_FILE")"
echo ""
echo "═══════════════════════════════════════════"
echo "  Broker overlay ready. Clients can now connect."
echo "═══════════════════════════════════════════"

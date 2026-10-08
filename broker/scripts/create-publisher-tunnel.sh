#!/bin/bash
# create-publisher-tunnel.sh — Create a WireGuard interface in a site namespace for a publisher
#
# This creates a WireGuard interface whose UDP socket lives in the host namespace
# (so it can reach the internet) but whose cleartext interface lives in the site
# namespace (so routes are isolated).
#
# Usage: sudo ./create-publisher-tunnel.sh <site-name> <site-id> <publisher-pubkey> <tunnel-ip-broker> <tunnel-ip-publisher> <allowed-cidrs> [publisher-endpoint]
#
# Example:
#   sudo ./create-publisher-tunnel.sh site-madrid 1 "AbCd...=" 10.100.1.1 10.100.1.2 "10.0.0.0/24,10.0.1.0/24" "203.0.113.5:51821"
#
# Parameters:
#   site-name          - Name of the site namespace
#   site-id            - Numeric ID (for port calculation: 51820 + site-id)
#   publisher-pubkey   - Publisher's WireGuard public key
#   tunnel-ip-broker   - Broker's IP on this tunnel (e.g., 10.100.1.1)
#   tunnel-ip-publisher- Publisher's IP on this tunnel (e.g., 10.100.1.2)
#   allowed-cidrs      - Comma-separated CIDRs reachable via publisher
#   publisher-endpoint - (Optional) Publisher's public endpoint if known

set -euo pipefail

if [ $# -lt 6 ]; then
    echo "Usage: $0 <site-name> <site-id> <publisher-pubkey> <tunnel-ip-broker> <tunnel-ip-publisher> <allowed-cidrs> [publisher-endpoint]"
    exit 1
fi

SITE="$1"
SITE_ID="$2"
PUB_PUBKEY="$3"
TUNNEL_IP_BROKER="$4"
TUNNEL_IP_PUB="$5"
ALLOWED_CIDRS="$6"
PUB_ENDPOINT="${7:-}"

NETNS="$SITE"
WG_IF="wg-pub-${SITE}"
WG_PORT=$((51820 + SITE_ID))
KEY_DIR="/etc/wireguard"
PRIVATE_KEY_FILE="${KEY_DIR}/${WG_IF}.key"

echo "[*] Creating publisher tunnel in namespace '$NETNS'"
echo "    Interface: $WG_IF"
echo "    Listen port: $WG_PORT"
echo "    Tunnel IPs: broker=$TUNNEL_IP_BROKER, publisher=$TUNNEL_IP_PUB"
echo "    Allowed CIDRs: $ALLOWED_CIDRS"

# ─── Verify namespace exists ───
if ! ip netns list | grep -qw "$NETNS"; then
    echo "ERROR: Namespace '$NETNS' does not exist. Run create-namespace.sh first."
    exit 1
fi

# ─── Generate tunnel keypair if needed ───
mkdir -p "$KEY_DIR"
if [ ! -f "$PRIVATE_KEY_FILE" ]; then
    wg genkey > "$PRIVATE_KEY_FILE"
    chmod 600 "$PRIVATE_KEY_FILE"
    echo "[*] Generated tunnel private key: $PRIVATE_KEY_FILE"
fi
PUB_KEY_LOCAL=$(wg pubkey < "$PRIVATE_KEY_FILE")
echo "    Broker tunnel public key: $PUB_KEY_LOCAL"

# ─── Remove existing interface if present ───
if ip netns exec "$NETNS" ip link show "$WG_IF" &>/dev/null; then
    ip netns exec "$NETNS" ip link delete "$WG_IF"
fi
# Also check in host namespace
if ip link show "$WG_IF" &>/dev/null; then
    ip link delete "$WG_IF"
fi

# ─── Create WireGuard interface in host namespace (socket stays here) ───
ip link add "$WG_IF" type wireguard

# ─── Configure WireGuard (in host namespace before moving) ───
wg set "$WG_IF" \
    listen-port "$WG_PORT" \
    private-key "$PRIVATE_KEY_FILE"

# ─── Add publisher peer ───
ALLOWED_IPS="${TUNNEL_IP_PUB}/32"
# Append site CIDRs
IFS=',' read -ra CIDRS <<< "$ALLOWED_CIDRS"
for cidr in "${CIDRS[@]}"; do
    ALLOWED_IPS="${ALLOWED_IPS},${cidr}"
done

if [ -n "$PUB_ENDPOINT" ]; then
    wg set "$WG_IF" peer "$PUB_PUBKEY" \
        allowed-ips "$ALLOWED_IPS" \
        endpoint "$PUB_ENDPOINT" \
        persistent-keepalive 25
else
    wg set "$WG_IF" peer "$PUB_PUBKEY" \
        allowed-ips "$ALLOWED_IPS" \
        persistent-keepalive 25
fi

# ─── Move interface into site namespace ───
ip link set "$WG_IF" netns "$NETNS"

# ─── Configure inside namespace ───
ip netns exec "$NETNS" ip addr add "${TUNNEL_IP_BROKER}/32" dev "$WG_IF"
ip netns exec "$NETNS" ip link set "$WG_IF" up

# ─── Add routes for publisher's allowed CIDRs ───
for cidr in "${CIDRS[@]}"; do
    ip netns exec "$NETNS" ip route add "$cidr" dev "$WG_IF" 2>/dev/null || true
done
ip netns exec "$NETNS" ip route add "${TUNNEL_IP_PUB}/32" dev "$WG_IF" 2>/dev/null || true

# ─── NAT masquerade for traffic going to publisher ───
ip netns exec "$NETNS" nft add table ip nat 2>/dev/null || true
ip netns exec "$NETNS" nft add chain ip nat postrouting '{ type nat hook postrouting priority 100 ; }' 2>/dev/null || true
ip netns exec "$NETNS" nft add rule ip nat postrouting oif "$WG_IF" masquerade 2>/dev/null || true

echo ""
echo "[✓] Publisher tunnel created in namespace '$NETNS'"
echo "    Interface: $WG_IF (inside namespace)"
echo "    Socket: UDP port $WG_PORT (in host namespace)"
echo "    Broker tunnel pubkey: $PUB_KEY_LOCAL"
echo ""
echo "    Give this pubkey to the publisher for its [Peer] config."

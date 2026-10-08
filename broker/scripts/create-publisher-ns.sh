#!/bin/bash
# create-publisher-ns.sh — Create an isolated network namespace for a publisher
#
# This creates:
#   1. A network namespace named ns-<publisher_id_short> (first 8 chars of UUID)
#   2. A veth pair connecting the host to the namespace (v<id>-h ↔ v<id>-ns)
#   3. IP addressing on both ends (10.252.N.1/30 ↔ 10.252.N.2/30)
#   4. Routing and IP forwarding within the namespace
#
# Usage: sudo ./create-publisher-ns.sh <publisher-id> <publisher-index>
# Example: sudo ./create-publisher-ns.sh a1b2c3d4-e5f6-7890-abcd-ef1234567890 3
#
# The publisher-index (1-255) determines the veth IP addressing.

set -euo pipefail

if [ $# -lt 2 ]; then
    echo "Usage: $0 <publisher-id> <publisher-index>"
    echo "  publisher-id:    UUID of the publisher"
    echo "  publisher-index: Numeric index 1-255 (used for veth addressing)"
    exit 1
fi

PUBLISHER_ID="$1"
PUBLISHER_INDEX="$2"

# Derive names from publisher ID per design doc
ID_SHORT="${PUBLISHER_ID:0:8}"
ID_VETH="${PUBLISHER_ID:0:6}"

NETNS="ns-${ID_SHORT}"
VETH_HOST="v${ID_VETH}-h"
VETH_NS="v${ID_VETH}-ns"
HOST_IP="10.252.${PUBLISHER_INDEX}.1"
NS_IP="10.252.${PUBLISHER_INDEX}.2"
MASK="30"

echo "═══════════════════════════════════════════"
echo "  Creating publisher namespace: $NETNS"
echo "  Publisher ID: $PUBLISHER_ID"
echo "  Publisher Index: $PUBLISHER_INDEX"
echo "═══════════════════════════════════════════"

# ─── Check if namespace already exists ───
if ip netns list | grep -qw "$NETNS"; then
    echo "[!] Namespace '$NETNS' already exists. Use destroy-publisher-ns.sh first."
    exit 1
fi

# ─── Create namespace ───
echo "[*] Creating network namespace '$NETNS'..."
ip netns add "$NETNS"

# ─── Bring up loopback in namespace ───
ip netns exec "$NETNS" ip link set lo up

# ─── Create veth pair ───
echo "[*] Creating veth pair: $VETH_HOST ↔ $VETH_NS"
ip link add "$VETH_HOST" type veth peer name "$VETH_NS"

# ─── Move one end into namespace ───
ip link set "$VETH_NS" netns "$NETNS"

# ─── Configure host side ───
ip addr add "${HOST_IP}/${MASK}" dev "$VETH_HOST"
ip link set "$VETH_HOST" up

# ─── Configure namespace side ───
ip netns exec "$NETNS" ip addr add "${NS_IP}/${MASK}" dev "$VETH_NS"
ip netns exec "$NETNS" ip link set "$VETH_NS" up

# ─── Default route in namespace (via host veth) ───
ip netns exec "$NETNS" ip route add default via "$HOST_IP"

# ─── Enable forwarding in namespace ───
ip netns exec "$NETNS" sysctl -w net.ipv4.ip_forward=1 > /dev/null

echo ""
echo "[✓] Namespace '$NETNS' created successfully"
echo "    Host side:      $VETH_HOST = ${HOST_IP}/${MASK}"
echo "    Namespace side: $VETH_NS   = ${NS_IP}/${MASK}"
echo ""
echo "    To execute commands in this namespace:"
echo "    sudo ip netns exec $NETNS <command>"
echo ""
echo "═══════════════════════════════════════════"

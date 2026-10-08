#!/bin/bash
# create-namespace.sh — Create an isolated network namespace for a site
#
# This creates:
#   1. A network namespace named after the site
#   2. A veth pair connecting the host to the namespace
#   3. IP addressing on both ends (10.252.SITE_ID.1/30 ↔ 10.252.SITE_ID.2/30)
#   4. Routing and NAT within the namespace
#
# Usage: sudo ./create-namespace.sh <site-name> <site-id>
# Example: sudo ./create-namespace.sh site-madrid 1
#
# The site-id (1-255) determines the veth IP addressing.

set -euo pipefail

if [ $# -lt 2 ]; then
    echo "Usage: $0 <site-name> <site-id>"
    echo "  site-name: Unique name for the site (e.g., site-madrid)"
    echo "  site-id:   Numeric ID 1-255 (used for veth addressing)"
    exit 1
fi

SITE="$1"
SITE_ID="$2"
NETNS="$SITE"
VETH_HOST="veth-${SITE}-h"
VETH_NS="veth-${SITE}-ns"
HOST_IP="10.252.${SITE_ID}.1"
NS_IP="10.252.${SITE_ID}.2"
MASK="30"

echo "═══════════════════════════════════════════"
echo "  Creating namespace: $NETNS (ID: $SITE_ID)"
echo "═══════════════════════════════════════════"

# ─── Check if namespace already exists ───
if ip netns list | grep -qw "$NETNS"; then
    echo "[!] Namespace '$NETNS' already exists. Use destroy-namespace.sh first."
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

# ─── Add route on host to reach namespace ───
# (traffic destined for NS_IP goes via veth — already implicit via connected route)

echo ""
echo "[✓] Namespace '$NETNS' created successfully"
echo "    Host side:      $VETH_HOST = ${HOST_IP}/${MASK}"
echo "    Namespace side: $VETH_NS   = ${NS_IP}/${MASK}"
echo ""
echo "    To execute commands in this namespace:"
echo "    sudo ip netns exec $NETNS <command>"
echo ""
echo "═══════════════════════════════════════════"

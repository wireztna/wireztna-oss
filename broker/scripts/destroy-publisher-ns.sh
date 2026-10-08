#!/bin/bash
# destroy-publisher-ns.sh — Tear down a publisher's network namespace and veth pair
#
# This removes:
#   1. Any ip rules for policy routing to 10.252.N.0/30 (the veth subnet)
#   2. The network namespace ns-<publisher_id_short>
#   3. The veth pair (host side: v<id>-h; namespace side is removed with the namespace)
#
# Usage: sudo ./destroy-publisher-ns.sh <publisher-id> <publisher-index>
# Example: sudo ./destroy-publisher-ns.sh a1b2c3d4-e5f6-7890-abcd-ef1234567890 3
#
# The publisher-index is needed to identify the associated ip rules and routes.

set -euo pipefail

if [ $# -lt 2 ]; then
    echo "Usage: $0 <publisher-id> <publisher-index>"
    echo "  publisher-id:    UUID of the publisher"
    echo "  publisher-index: Numeric index 1-255 (used for veth addressing)"
    exit 1
fi

PUBLISHER_ID="$1"
PUBLISHER_INDEX="$2"

# Derive names from publisher ID (same logic as create-publisher-ns.sh)
ID_SHORT="${PUBLISHER_ID:0:8}"
ID_VETH="${PUBLISHER_ID:0:6}"

NETNS="ns-${ID_SHORT}"
VETH_HOST="v${ID_VETH}-h"
HOST_IP="10.252.${PUBLISHER_INDEX}.1"
SUBNET="10.252.${PUBLISHER_INDEX}.0/30"

echo "═══════════════════════════════════════════"
echo "  Destroying publisher namespace: $NETNS"
echo "  Publisher ID: $PUBLISHER_ID"
echo "  Publisher Index: $PUBLISHER_INDEX"
echo "═══════════════════════════════════════════"

# ─── Check if namespace exists ───
if ! ip netns list | grep -qw "$NETNS"; then
    echo "[!] Namespace '$NETNS' does not exist. Nothing to destroy."
    exit 0
fi

# ─── Remove ip rules for policy routing ───
echo "[*] Removing ip rules for subnet $SUBNET..."
while ip rule show | grep -q "$SUBNET"; do
    ip rule del to "$SUBNET" 2>/dev/null || true
done

# Also remove any rules referencing the host-side veth IP as a lookup
while ip rule show | grep -q "from $HOST_IP"; do
    ip rule del from "$HOST_IP" 2>/dev/null || true
done

# ─── Remove any routes in custom tables referencing this publisher ───
# Policy routing tables often use the publisher index as the table number
TABLE_ID="$PUBLISHER_INDEX"
if ip route show table "$TABLE_ID" &>/dev/null; then
    echo "[*] Flushing routing table $TABLE_ID..."
    ip route flush table "$TABLE_ID" 2>/dev/null || true
fi

# ─── Delete namespace (also removes veth namespace-side end) ───
echo "[*] Deleting namespace '$NETNS'..."
ip netns delete "$NETNS"

# ─── Clean up host-side veth (may already be gone with namespace deletion) ───
if ip link show "$VETH_HOST" &>/dev/null; then
    echo "[*] Removing host-side veth '$VETH_HOST'..."
    ip link delete "$VETH_HOST"
fi

echo ""
echo "[✓] Namespace '$NETNS' destroyed successfully"
echo "    Removed veth: $VETH_HOST"
echo "    Cleaned ip rules for: $SUBNET"
echo ""
echo "═══════════════════════════════════════════"

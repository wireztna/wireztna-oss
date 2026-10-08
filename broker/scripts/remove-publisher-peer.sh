#!/bin/bash
# remove-publisher-peer.sh — Remove a publisher peer from a site's WireGuard tunnel
#
# Usage: sudo ./remove-publisher-peer.sh <site-name> <publisher-pubkey>

set -euo pipefail

if [ $# -lt 2 ]; then
    echo "Usage: $0 <site-name> <publisher-pubkey>"
    exit 1
fi

SITE="$1"
PUB_PUBKEY="$2"
NETNS="$SITE"
WG_IF="wg-pub-${SITE}"

echo "[*] Removing publisher peer from $WG_IF in namespace $NETNS"

ip netns exec "$NETNS" wg set "$WG_IF" peer "$PUB_PUBKEY" remove

echo "[✓] Publisher peer removed from namespace $NETNS"

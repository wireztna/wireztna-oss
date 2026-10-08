#!/bin/bash
# add-publisher-peer.sh — Add an additional publisher peer to an existing site tunnel
# Used when adding a second+ gateway to a site for HA/balancing
#
# Usage: sudo ./add-publisher-peer.sh <site-name> <publisher-pubkey> <tunnel-ip-publisher> <allowed-cidrs> [publisher-endpoint]

set -euo pipefail

if [ $# -lt 4 ]; then
    echo "Usage: $0 <site-name> <publisher-pubkey> <tunnel-ip-publisher> <allowed-cidrs> [endpoint]"
    exit 1
fi

SITE="$1"
PUB_PUBKEY="$2"
TUNNEL_IP_PUB="$3"
ALLOWED_CIDRS="$4"
PUB_ENDPOINT="${5:-}"

NETNS="$SITE"
WG_IF="wg-pub-${SITE}"

# Build allowed IPs
ALLOWED_IPS="${TUNNEL_IP_PUB}/32"
IFS=',' read -ra CIDRS <<< "$ALLOWED_CIDRS"
for cidr in "${CIDRS[@]}"; do
    ALLOWED_IPS="${ALLOWED_IPS},${cidr}"
done

echo "[*] Adding publisher peer to $WG_IF in namespace $NETNS"

if [ -n "$PUB_ENDPOINT" ]; then
    ip netns exec "$NETNS" wg set "$WG_IF" peer "$PUB_PUBKEY" \
        allowed-ips "$ALLOWED_IPS" \
        endpoint "$PUB_ENDPOINT" \
        persistent-keepalive 25
else
    ip netns exec "$NETNS" wg set "$WG_IF" peer "$PUB_PUBKEY" \
        allowed-ips "$ALLOWED_IPS" \
        persistent-keepalive 25
fi

echo "[✓] Publisher peer added to namespace $NETNS"

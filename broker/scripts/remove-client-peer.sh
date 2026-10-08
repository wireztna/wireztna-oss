#!/bin/bash
# remove-client-peer.sh — Remove a client peer from the broker's overlay interface
#
# Usage: sudo ./remove-client-peer.sh <public_key>

set -euo pipefail

WG_INTERFACE="wg-clients"

if [ $# -lt 1 ]; then
    echo "Usage: $0 <public_key>"
    exit 1
fi

PUBLIC_KEY="$1"

echo "[*] Removing client peer from $WG_INTERFACE"
echo "    Public Key: $PUBLIC_KEY"

wg set "$WG_INTERFACE" peer "$PUBLIC_KEY" remove

echo "[✓] Client peer removed"

#!/bin/bash
# update-client-psk.sh — Update the preshared key for an existing client peer
#
# This is called by the reconciler when a client renews their session,
# receiving a fresh PSK. The WireGuard handshake will use the new PSK
# on the next re-key (within ~2 minutes).
#
# Usage: sudo ./update-client-psk.sh <public_key> <preshared_key>
# Example: sudo ./update-client-psk.sh "abc123...=" "newpsk...="

set -euo pipefail

WG_INTERFACE="wg-clients"

if [ $# -lt 2 ]; then
    echo "Usage: $0 <public_key> <preshared_key>"
    exit 1
fi

PUBLIC_KEY="$1"
PRESHARED_KEY="$2"

# Verify the peer exists
if ! wg show "$WG_INTERFACE" peers | grep -q "$PUBLIC_KEY"; then
    echo "ERROR: Peer not found on $WG_INTERFACE: $PUBLIC_KEY"
    exit 1
fi

echo "[*] Updating PSK for peer on $WG_INTERFACE"
echo "    Public Key: ${PUBLIC_KEY:0:8}..."

# Write PSK to temporary file (wg requires file input for preshared-key)
PSK_FILE=$(mktemp)
echo "$PRESHARED_KEY" > "$PSK_FILE"

wg set "$WG_INTERFACE" peer "$PUBLIC_KEY" preshared-key "$PSK_FILE"

rm -f "$PSK_FILE"

echo "[✓] PSK updated — new handshake will use the rotated key"

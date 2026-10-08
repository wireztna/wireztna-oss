#!/bin/bash
# add-client-peer.sh — Add a client peer to the broker's overlay interface
#
# Usage: sudo ./add-client-peer.sh <public_key> <overlay_ip> [preshared_key]
# Example: sudo ./add-client-peer.sh "abc123...=" "10.200.0.15"
# Example with PSK: sudo ./add-client-peer.sh "abc123...=" "10.200.0.15" "base64psk...="

set -euo pipefail

WG_INTERFACE="wg-clients"

if [ $# -lt 2 ]; then
    echo "Usage: $0 <public_key> <overlay_ip> [preshared_key]"
    echo "Example: $0 'AbCdEf...=' '10.200.0.15'"
    echo "Example with PSK: $0 'AbCdEf...=' '10.200.0.15' 'PskBase64...='"
    exit 1
fi

PUBLIC_KEY="$1"
OVERLAY_IP="$2"
PRESHARED_KEY="${3:-}"

echo "[*] Adding client peer to $WG_INTERFACE"
echo "    Public Key: $PUBLIC_KEY"
echo "    Allowed IP: ${OVERLAY_IP}/32"

if [ -n "$PRESHARED_KEY" ]; then
    echo "    PSK: (provided)"
    PSK_FILE=$(mktemp)
    echo "$PRESHARED_KEY" > "$PSK_FILE"
    wg set "$WG_INTERFACE" peer "$PUBLIC_KEY" preshared-key "$PSK_FILE" allowed-ips "${OVERLAY_IP}/32"
    rm -f "$PSK_FILE"
else
    wg set "$WG_INTERFACE" peer "$PUBLIC_KEY" allowed-ips "${OVERLAY_IP}/32"
fi

echo "[✓] Client peer added successfully"

#!/bin/bash
# destroy-namespace.sh — Remove a site's network namespace and all associated resources
#
# Usage: sudo ./destroy-namespace.sh <site-name>
# Example: sudo ./destroy-namespace.sh site-madrid

set -euo pipefail

if [ $# -lt 1 ]; then
    echo "Usage: $0 <site-name>"
    exit 1
fi

SITE="$1"
NETNS="$SITE"
VETH_HOST="veth-${SITE}-h"

echo "[*] Destroying namespace: $NETNS"

# ─── Check if namespace exists ───
if ! ip netns list | grep -qw "$NETNS"; then
    echo "[!] Namespace '$NETNS' does not exist."
    exit 1
fi

# ─── Delete namespace (also removes veth-ns end) ───
ip netns delete "$NETNS"

# ─── Clean up host-side veth (may already be gone) ───
if ip link show "$VETH_HOST" &>/dev/null; then
    ip link delete "$VETH_HOST"
fi

echo "[✓] Namespace '$NETNS' destroyed"

#!/bin/bash
# setup-ns-dns.sh — Start a DNS proxy bridging host → publisher namespace
#
# Starts a socat UDP forwarder on the host that forwards DNS queries into
# the publisher's network namespace, where the publisher's DNS server is
# reachable via the WireGuard tunnel.
#
# CoreDNS (running in the host namespace) forwards zone queries to
# 127.0.0.1:<proxy_port>. This script bridges that to the real DNS server
# inside the namespace.
#
# Usage: sudo ./setup-ns-dns.sh <publisher-id> <publisher-index> <dns-server-ip>
#
# Example: sudo ./setup-ns-dns.sh a1b2c3d4-e5f6-7890-abcd-1234567890ab 1 10.0.0.53
#   → Starts socat on host port 5301 (5300 + 1)
#   → Forwards UDP to 10.0.0.53:53 inside ns-a1b2c3d4

set -euo pipefail

if [ $# -lt 3 ]; then
    echo "Usage: $0 <publisher-id> <publisher-index> <dns-server-ip>"
    exit 1
fi

PUB_ID="$1"
PUB_INDEX="$2"
DNS_SERVER="$3"
ID_SHORT="${PUB_ID:0:8}"
NAMESPACE="ns-${ID_SHORT}"
PROXY_PORT=$((5300 + PUB_INDEX))
PID_DIR="/var/run/wireztna"
PID_FILE="${PID_DIR}/dns-proxy-${PUB_INDEX}.pid"

echo "[*] Setting up DNS proxy for publisher ${ID_SHORT} (index ${PUB_INDEX})"
echo "    Host port: 127.0.0.1:${PROXY_PORT} (UDP)"
echo "    Namespace: ${NAMESPACE}"
echo "    Target DNS: ${DNS_SERVER}:53"

# ─── Create PID directory ───
mkdir -p "$PID_DIR"

# ─── Kill existing proxy for this publisher index ───
if [ -f "$PID_FILE" ]; then
    OLD_PID=$(cat "$PID_FILE")
    if kill -0 "$OLD_PID" 2>/dev/null; then
        echo "[*] Stopping existing DNS proxy (PID $OLD_PID)"
        kill "$OLD_PID" 2>/dev/null || true
        sleep 0.5
    fi
    rm -f "$PID_FILE"
fi

# ─── Start socat UDP forwarder ───
# socat on the host listens for UDP on the proxy port and forwards into the namespace
# using "ip netns exec" to reach the publisher's DNS server through the WG tunnel.
socat UDP4-LISTEN:${PROXY_PORT},bind=127.0.0.1,fork,reuseaddr \
    EXEC:"ip netns exec ${NAMESPACE} socat STDIO UDP4\:${DNS_SERVER}\:53" \
    >/dev/null 2>&1 &

SOCAT_PID=$!

# Verify it started
sleep 0.5
if kill -0 "$SOCAT_PID" 2>/dev/null; then
    echo "$SOCAT_PID" > "$PID_FILE"
    echo "[✓] DNS proxy running (PID ${SOCAT_PID})"
    echo "    Listening: 127.0.0.1:${PROXY_PORT} → ${NAMESPACE}/${DNS_SERVER}:53"
else
    echo "[✗] DNS proxy failed to start"
    exit 1
fi

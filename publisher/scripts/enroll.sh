#!/bin/bash
# enroll.sh — Publisher enrollment and WireGuard tunnel bootstrap
#
# This script runs on first boot of a publisher. It:
# 1. Generates a WireGuard keypair
# 2. Registers with the control plane using the enrollment token
# 3. Receives broker endpoint and public key
# 4. Configures and starts the WireGuard tunnel
# 5. Enables IP forwarding
#
# Required environment variables:
#   ENROLLMENT_TOKEN    - One-time token from the control plane portal
#   CONTROL_PLANE_URL   - URL of the control plane API (e.g., https://broker.example.com:8443)
#
# Optional:
#   PUBLISHER_WG_PORT   - Local WireGuard port (default: 51821)
#   PUBLISHER_NAME      - Friendly name (default: hostname)
#   LOCAL_DNS           - Local DNS server IP to expose (default: none)
#   CONFIG_DIR          - Where to store configs (default: /etc/wireguard)

set -euo pipefail

# ─── Configuration ───
CONFIG_DIR="${CONFIG_DIR:-/etc/wireguard}"
STATE_FILE="${CONFIG_DIR}/.enrolled"
WG_INTERFACE="wg-broker"

# ─── Check if already enrolled (before requiring token) ───
if [ -f "$STATE_FILE" ]; then
    echo "[!] Publisher already enrolled. Remove $STATE_FILE to re-enroll."
    echo "    Skipping enrollment — tunnel will be managed by entrypoint."
    exit 0
fi

# Token only required for fresh enrollment
ENROLLMENT_TOKEN="${ENROLLMENT_TOKEN:?ERROR: ENROLLMENT_TOKEN is required}"
CONTROL_PLANE_URL="${CONTROL_PLANE_URL:?ERROR: CONTROL_PLANE_URL is required}"
WG_PORT="${PUBLISHER_WG_PORT:-51821}"
PUBLISHER_NAME="${PUBLISHER_NAME:-$(hostname)}"
# Auto-detect local DNS if not explicitly set
if [ -z "${LOCAL_DNS:-}" ]; then
    LOCAL_DNS=$(grep -m1 '^nameserver' /etc/resolv.conf 2>/dev/null | awk '{print $2}' || true)
    # Skip Docker internal DNS
    if [ "$LOCAL_DNS" = "127.0.0.11" ] && [ -f /host/etc/resolv.conf ]; then
        LOCAL_DNS=$(grep -m1 '^nameserver' /host/etc/resolv.conf | awk '{print $2}' || true)
    fi
    if [ -n "$LOCAL_DNS" ]; then
        echo "[*] Auto-detected local DNS: $LOCAL_DNS"
    fi
fi
PRIVATE_KEY_FILE="${CONFIG_DIR}/publisher.key"
CONFIG_FILE="${CONFIG_DIR}/${WG_INTERFACE}.conf"

echo "═══════════════════════════════════════════"
echo "  WireZTNA Publisher Enrollment"
echo "═══════════════════════════════════════════"

# ─── Ensure WireGuard available ───
if ! command -v wg &>/dev/null; then
    echo "ERROR: wireguard-tools not installed"
    exit 1
fi

# ─── Generate keypair ───
mkdir -p "$CONFIG_DIR"
chmod 700 "$CONFIG_DIR"

echo "[*] Generating WireGuard keypair..."
PRIVATE_KEY=$(wg genkey)
echo "$PRIVATE_KEY" > "$PRIVATE_KEY_FILE"
chmod 600 "$PRIVATE_KEY_FILE"
PUBLIC_KEY=$(echo "$PRIVATE_KEY" | wg pubkey)
echo "[✓] Public key: $PUBLIC_KEY"

# ─── Register with control plane ───
echo "[*] Enrolling with control plane at $CONTROL_PLANE_URL..."

ENROLL_PAYLOAD=$(cat <<EOF
{
    "token": "$ENROLLMENT_TOKEN",
    "public_key": "$PUBLIC_KEY",
    "name": "$PUBLISHER_NAME",
    "local_dns": "$LOCAL_DNS"
}
EOF
)

RESPONSE=$(curl -s -w "\n%{http_code}" \
    -X POST \
    -H "Content-Type: application/json" \
    -d "$ENROLL_PAYLOAD" \
    "${CONTROL_PLANE_URL}/api/v1/publishers/enroll")

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" != "200" ] && [ "$HTTP_CODE" != "201" ]; then
    echo "ERROR: Enrollment failed (HTTP $HTTP_CODE)"
    echo "Response: $BODY"
    # If token is already used, check if we have a valid config from a previous enrollment
    if [ -f "$CONFIG_FILE" ]; then
        echo "[!] Found existing WireGuard config — using previous enrollment."
        echo "[!] This can happen after a container restart where enrollment succeeded but tunnel setup failed."
        # Mark as enrolled so next restart skips enrollment entirely
        echo "publisher_id=unknown-recovered" > "$STATE_FILE"
        echo "enrolled_at=$(date -Iseconds)" >> "$STATE_FILE"
        echo "recovered=true" >> "$STATE_FILE"
        exit 0
    fi
    exit 1
fi

echo "[✓] Enrollment successful"

# ─── Parse response ───
BROKER_PUBKEY=$(echo "$BODY" | jq -r '.broker_public_key')
BROKER_ENDPOINT=$(echo "$BODY" | jq -r '.broker_endpoint')
TUNNEL_IP=$(echo "$BODY" | jq -r '.tunnel_ip')
BROKER_TUNNEL_IP=$(echo "$BODY" | jq -r '.broker_tunnel_ip')
PUBLISHER_ID=$(echo "$BODY" | jq -r '.publisher_id')
PUBLISHER_API_KEY=$(echo "$BODY" | jq -r '.publisher_api_key // ""')
ALLOWED_IPS=$(echo "$BODY" | jq -r '.allowed_ips // "10.200.0.0/16,10.100.0.0/16"')
CONNECTION_INFO_URL=$(echo "$BODY" | jq -r '.connection_info_url // ""')
POLL_FOR_CONNECTION=$(echo "$BODY" | jq -r '.poll_for_connection // false')

echo "    Publisher ID:    $PUBLISHER_ID"
echo "    Tunnel IP:       $TUNNEL_IP"

# ─── Poll for real broker connection info if needed ───
if [ "$POLL_FOR_CONNECTION" = "true" ] && [ -n "$CONNECTION_INFO_URL" ]; then
    echo "[*] Waiting for broker to provision namespace..."
    MAX_ATTEMPTS=60
    ATTEMPT=0
    while [ $ATTEMPT -lt $MAX_ATTEMPTS ]; do
        CONN_RESPONSE=$(curl -sf -H "X-API-Key: ${PUBLISHER_API_KEY:-}" "$CONNECTION_INFO_URL" 2>/dev/null || echo '{"ready":false}')
        READY=$(echo "$CONN_RESPONSE" | jq -r '.ready')
        if [ "$READY" = "true" ]; then
            BROKER_PUBKEY=$(echo "$CONN_RESPONSE" | jq -r '.broker_public_key')
            BROKER_ENDPOINT=$(echo "$CONN_RESPONSE" | jq -r '.broker_endpoint')
            BROKER_TUNNEL_IP=$(echo "$CONN_RESPONSE" | jq -r '.broker_tunnel_ip')
            TUNNEL_IP=$(echo "$CONN_RESPONSE" | jq -r '.tunnel_ip')
            ALLOWED_IPS=$(echo "$CONN_RESPONSE" | jq -r '.allowed_ips // "10.200.0.0/16,10.100.0.0/16"')
            echo "[✓] Broker namespace ready"
            echo "    Broker endpoint: $BROKER_ENDPOINT"
            echo "    Broker key:      ${BROKER_PUBKEY:0:20}..."
            break
        fi
        ATTEMPT=$((ATTEMPT + 1))
        sleep 5
    done
    if [ $ATTEMPT -ge $MAX_ATTEMPTS ]; then
        echo "ERROR: Timed out waiting for broker namespace (5 minutes). The reconciler may not be running."
        exit 1
    fi
else
    echo "    Broker endpoint: $BROKER_ENDPOINT"
fi

# ─── Generate WireGuard config ───
echo "[*] Generating WireGuard configuration..."

cat > "$CONFIG_FILE" << EOF
# WireZTNA Publisher — Auto-generated during enrollment
# Publisher: $PUBLISHER_NAME ($PUBLISHER_ID)
# Enrolled: $(date -Iseconds)

[Interface]
PrivateKey = $PRIVATE_KEY
Address = ${TUNNEL_IP}/32
ListenPort = $WG_PORT

[Peer]
# Broker
PublicKey = $BROKER_PUBKEY
Endpoint = $BROKER_ENDPOINT
AllowedIPs = ${ALLOWED_IPS}
PersistentKeepalive = 25
EOF

chmod 600 "$CONFIG_FILE"

# ─── Enable IP forwarding ───
echo "[*] Enabling IP forwarding..."
sysctl -w net.ipv4.ip_forward=1 > /dev/null 2>&1 || true
echo "net.ipv4.ip_forward=1" > /etc/sysctl.d/99-wireztna-publisher.conf 2>/dev/null || true

# ─── Start tunnel ───
echo "[*] Starting WireGuard tunnel..."
wg-quick up "$WG_INTERFACE"

# ─── Mark as enrolled ───
cat > "$STATE_FILE" << EOF
publisher_id=$PUBLISHER_ID
publisher_api_key=$PUBLISHER_API_KEY
enrolled_at=$(date -Iseconds)
broker_endpoint=$BROKER_ENDPOINT
tunnel_ip=$TUNNEL_IP
EOF
chmod 600 "$STATE_FILE"

echo ""
echo "═══════════════════════════════════════════"
echo "  Publisher enrolled and tunnel active!"
echo "═══════════════════════════════════════════"
echo ""
echo "  Status: $(wg show "$WG_INTERFACE" | head -5)"
echo ""

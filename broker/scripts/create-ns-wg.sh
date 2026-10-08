#!/bin/bash
# create-ns-wg.sh — Create a WireGuard interface inside a publisher's network namespace
#
# Creates a WireGuard interface whose UDP socket lives in the host namespace
# (so it can reach the internet/publisher endpoint) but whose cleartext interface
# lives in the publisher's network namespace (for isolated routing).
#
# Usage: sudo ./create-ns-wg.sh <publisher-id> <publisher-index> <publisher-pubkey> <tunnel-ip-broker> <tunnel-ip-publisher> <allowed-cidrs> [publisher-endpoint]
#
# Example:
#   sudo ./create-ns-wg.sh a1b2c3d4-e5f6-7890-abcd-ef1234567890 3 "AbCd...=" 10.100.3.1 10.100.3.2 "10.0.0.0/24,10.0.1.0/24" "203.0.113.5:51821"
#
# Parameters:
#   publisher-id        - UUID of the publisher
#   publisher-index     - Numeric index (for port calculation: 51820 + index)
#   publisher-pubkey    - Publisher's WireGuard public key
#   tunnel-ip-broker    - Broker's IP on this tunnel (e.g., 10.100.3.1)
#   tunnel-ip-publisher - Publisher's IP on this tunnel (e.g., 10.100.3.2)
#   allowed-cidrs       - Comma-separated CIDRs reachable via publisher
#   publisher-endpoint  - (Optional) Publisher's public endpoint if known
#
# Prerequisites:
#   - Namespace must already exist (run create-publisher-ns.sh first)
#   - wireguard-tools must be installed

set -euo pipefail

if [ $# -lt 6 ]; then
    echo "Usage: $0 <publisher-id> <publisher-index> <publisher-pubkey> <tunnel-ip-broker> <tunnel-ip-publisher> <allowed-cidrs> [publisher-endpoint]"
    echo ""
    echo "  publisher-id:        UUID of the publisher"
    echo "  publisher-index:     Numeric index (port = 51820 + index)"
    echo "  publisher-pubkey:    Publisher's WireGuard public key"
    echo "  tunnel-ip-broker:    Broker's tunnel IP (e.g., 10.100.3.1)"
    echo "  tunnel-ip-publisher: Publisher's tunnel IP (e.g., 10.100.3.2)"
    echo "  allowed-cidrs:       Comma-separated CIDRs (e.g., 10.0.0.0/24,10.0.1.0/24)"
    echo "  publisher-endpoint:  (Optional) Publisher's public IP:port"
    exit 1
fi

PUBLISHER_ID="$1"
PUBLISHER_INDEX="$2"
PUB_PUBKEY="$3"
TUNNEL_IP_BROKER="$4"
TUNNEL_IP_PUB="$5"
ALLOWED_CIDRS="$6"
PUB_ENDPOINT="${7:-}"

# Derive names from publisher ID (consistent with create-publisher-ns.sh)
ID_SHORT="${PUBLISHER_ID:0:8}"
ID_WG="${PUBLISHER_ID:0:6}"

NETNS="ns-${ID_SHORT}"
WG_IF="wg-${ID_WG}"
WG_PORT=$((51820 + PUBLISHER_INDEX))
KEY_DIR="/etc/wireguard"
PRIVATE_KEY_FILE="${KEY_DIR}/${WG_IF}.key"
PORT_FILE="${KEY_DIR}/${WG_IF}.port"  # Persists actual listen port (may differ from calculated if zombie detected)

echo "═══════════════════════════════════════════"
echo "  Creating WireGuard interface in namespace"
echo "  Publisher ID: $PUBLISHER_ID"
echo "  Namespace:   $NETNS"
echo "  Interface:   $WG_IF"
echo "  Base port:   $WG_PORT (may change if zombie detected)"
echo "  Tunnel IPs:  broker=$TUNNEL_IP_BROKER, publisher=$TUNNEL_IP_PUB"
echo "  Allowed CIDRs: $ALLOWED_CIDRS"
echo "═══════════════════════════════════════════"

# ─── Verify namespace exists ───
if ! ip netns list | grep -qw "$NETNS"; then
    echo "ERROR: Namespace '$NETNS' does not exist. Run create-publisher-ns.sh first."
    exit 1
fi

# ─── Generate tunnel keypair if needed ───
mkdir -p "$KEY_DIR"
if [ ! -f "$PRIVATE_KEY_FILE" ]; then
    wg genkey > "$PRIVATE_KEY_FILE"
    chmod 600 "$PRIVATE_KEY_FILE"
    echo "[*] Generated tunnel private key: $PRIVATE_KEY_FILE"
fi
PUB_KEY_LOCAL=$(wg pubkey < "$PRIVATE_KEY_FILE")
echo "[*] Broker tunnel public key: $PUB_KEY_LOCAL"

# ─── Remove existing interface if present ───
if ip netns exec "$NETNS" ip link show "$WG_IF" &>/dev/null; then
    echo "[*] Removing existing interface '$WG_IF' from namespace..."
    ip netns exec "$NETNS" ip link delete "$WG_IF"
    sleep 1  # Allow kernel time to release the UDP socket
fi
# Also check in host namespace (leftover from failed previous run)
if ip link show "$WG_IF" &>/dev/null; then
    echo "[*] Removing leftover interface '$WG_IF' from host..."
    ip link delete "$WG_IF"
    sleep 1  # Allow kernel time to release the UDP socket
fi

# ─── Zombie socket detection and cleanup ───
# When a WireGuard interface is deleted, its kernel UDP socket may persist as an
# orphan ("zombie") that blocks new interfaces from receiving packets on the same port.
# Detect and clean up before creating the new interface.
cleanup_zombie_socket() {
    local port="$1"
    # Check if there's a UDP socket bound to this port in the host namespace
    # A legitimate WG socket would have been removed with the interface above.
    # Anything remaining is a zombie.
    local zombie_count
    zombie_count=$(ss -ulnH sport = ":${port}" 2>/dev/null | wc -l)

    if [ "$zombie_count" -gt 0 ]; then
        echo "[!] WARNING: Found zombie UDP socket(s) on port $port — attempting cleanup..."

        # Method 1: Try ss --kill (requires iproute2 >= 5.10)
        if ss -ulnH sport = ":${port}" --kill &>/dev/null; then
            sleep 1
            # Verify it's gone
            local remaining
            remaining=$(ss -ulnH sport = ":${port}" 2>/dev/null | wc -l)
            if [ "$remaining" -eq 0 ]; then
                echo "[*] Zombie socket on port $port successfully killed"
                return 0
            fi
        fi

        # Method 2: If ss --kill didn't work, the socket is kernel-owned (WG creates
        # sockets directly in kernel space). These can't be killed from userspace.
        # Use an alternative port.
        echo "[!] Could not kill zombie socket on port $port (kernel-owned)"
        return 1
    fi
    return 0
}

# Try to clean zombie on the calculated port
if ! cleanup_zombie_socket "$WG_PORT"; then
    # Zombie survived — use alternative port (original + 100)
    OLD_PORT="$WG_PORT"
    WG_PORT=$((WG_PORT + 100))
    echo "[!] Falling back to alternative port: $WG_PORT (original $OLD_PORT blocked by zombie)"
    echo "[!] A broker reboot will clear the zombie socket on port $OLD_PORT"

    # Check the alternative port isn't also zombied (unlikely but defensive)
    if ! cleanup_zombie_socket "$WG_PORT"; then
        # Try +200
        WG_PORT=$((WG_PORT + 100))
        echo "[!] Port $((WG_PORT - 100)) also blocked — using $WG_PORT"
        cleanup_zombie_socket "$WG_PORT" || true  # last resort, proceed anyway
    fi
fi

# Persist the actual port used (so the reconciler/health monitor know which port this publisher uses)
echo "$WG_PORT" > "$PORT_FILE"
echo "[*] Listen port: $WG_PORT (saved to $PORT_FILE)"

# ─── Create WireGuard interface in host namespace ───
# The socket stays in the host namespace so the broker can reach the publisher
# over the internet. The cleartext side will be moved into the publisher namespace.
echo "[*] Creating WireGuard interface '$WG_IF' in host namespace..."
ip link add "$WG_IF" type wireguard

# ─── Configure WireGuard (in host namespace before moving) ───
echo "[*] Configuring WireGuard (port=$WG_PORT)..."
wg set "$WG_IF" \
    listen-port "$WG_PORT" \
    private-key "$PRIVATE_KEY_FILE"

# ─── Add publisher as peer ───
# AllowedIPs includes the publisher's tunnel IP + all CIDRs reachable via this publisher
ALLOWED_IPS="${TUNNEL_IP_PUB}/32"
IFS=',' read -ra CIDRS <<< "$ALLOWED_CIDRS"
for cidr in "${CIDRS[@]}"; do
    ALLOWED_IPS="${ALLOWED_IPS},${cidr}"
done

echo "[*] Adding publisher peer..."
if [ -n "$PUB_ENDPOINT" ]; then
    wg set "$WG_IF" peer "$PUB_PUBKEY" \
        allowed-ips "$ALLOWED_IPS" \
        endpoint "$PUB_ENDPOINT" \
        persistent-keepalive 25
else
    wg set "$WG_IF" peer "$PUB_PUBKEY" \
        allowed-ips "$ALLOWED_IPS" \
        persistent-keepalive 25
fi

# ─── Move interface into publisher namespace ───
echo "[*] Moving '$WG_IF' into namespace '$NETNS'..."
ip link set "$WG_IF" netns "$NETNS"

# ─── Configure interface inside namespace ───
echo "[*] Configuring interface inside namespace..."
ip netns exec "$NETNS" ip addr add "${TUNNEL_IP_BROKER}/32" dev "$WG_IF"
ip netns exec "$NETNS" ip link set "$WG_IF" up

# ─── Add routes for publisher's allowed CIDRs inside namespace ───
echo "[*] Adding routes for allowed CIDRs..."
for cidr in "${CIDRS[@]}"; do
    if [ "$cidr" = "0.0.0.0/0" ]; then
        # Full-tunnel mode: use the 0.0.0.0/1 + 128.0.0.0/1 trick to override default route
        # without deleting it (these /1 routes are more specific than /0)
        echo "[*] Full-tunnel routes via WG (0.0.0.0/1 + 128.0.0.0/1)"
        ip netns exec "$NETNS" ip route add 0.0.0.0/1 dev "$WG_IF" 2>/dev/null || true
        ip netns exec "$NETNS" ip route add 128.0.0.0/1 dev "$WG_IF" 2>/dev/null || true

        # CRITICAL: Add explicit route for client overlay network BACK to the host
        # via veth. Without this, reply packets (dst=10.200.x.x) would match the /1
        # routes and loop back into the WG interface instead of going back to wg-clients.
        VETH_NS="v${PUBLISHER_ID:0:6}-ns"
        HOST_IP="10.252.${PUBLISHER_INDEX}.1"
        echo "[*] Adding overlay return route: 10.200.0.0/16 via $HOST_IP dev $VETH_NS"
        ip netns exec "$NETNS" ip route add 10.200.0.0/16 via "$HOST_IP" dev "$VETH_NS" 2>/dev/null || true
    else
        ip netns exec "$NETNS" ip route add "$cidr" dev "$WG_IF" 2>/dev/null || true
    fi
done
ip netns exec "$NETNS" ip route add "${TUNNEL_IP_PUB}/32" dev "$WG_IF" 2>/dev/null || true

# ─── NAT masquerade for traffic going to publisher ───
echo "[*] Setting up NAT masquerade in namespace..."
ip netns exec "$NETNS" nft add table ip nat 2>/dev/null || true
ip netns exec "$NETNS" nft add chain ip nat postrouting '{ type nat hook postrouting priority 100 ; }' 2>/dev/null || true
ip netns exec "$NETNS" nft add rule ip nat postrouting oif "$WG_IF" masquerade 2>/dev/null || true

echo ""
echo "═══════════════════════════════════════════"
echo "  [✓] WireGuard tunnel created successfully"
echo ""
echo "  Interface:  $WG_IF (inside namespace '$NETNS')"
echo "  Socket:     UDP port $WG_PORT (in host namespace)"
echo "  Tunnel IPs: broker=$TUNNEL_IP_BROKER, publisher=$TUNNEL_IP_PUB"
echo "  Broker tunnel pubkey: $PUB_KEY_LOCAL"
echo ""
echo "  Give this pubkey to the publisher for its [Peer] config."
echo "═══════════════════════════════════════════"

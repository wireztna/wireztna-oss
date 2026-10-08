#!/bin/bash
# setup-ns-nat.sh — Configure DNAT/SNAT inside a publisher's network namespace
#
# Sets up NAT rules so that:
#   - DNAT (ingress): Traffic arriving with dst in virtual CIDR (10.252.N.0/24)
#     gets rewritten to the publisher's real exposed CIDR via netmap
#   - SNAT (egress): Return traffic from the real CIDR gets its source rewritten
#     back to the virtual CIDR so the client sees consistent addresses
#   - For multiple CIDRs, only the first /24 gets 1:1 netmap; additional CIDRs
#     get standard masquerade
#
# Usage: sudo ./setup-ns-nat.sh <publisher-id> <publisher-index> <exposed-cidrs>
#
# Example:
#   sudo ./setup-ns-nat.sh a1b2c3d4-e5f6-7890-abcd-ef1234567890 3 "10.0.0.0/24"
#   sudo ./setup-ns-nat.sh a1b2c3d4-e5f6-7890-abcd-ef1234567890 3 "10.0.0.0/24,192.168.1.0/24"
#
# Parameters:
#   publisher-id     - UUID of the publisher (used to derive namespace name)
#   publisher-index  - Numeric index (determines virtual CIDR: 10.252.{index}.0/24)
#   exposed-cidrs    - Comma-separated real CIDRs the publisher exposes
#
# Prerequisites:
#   - Namespace must already exist (run create-publisher-ns.sh first)
#   - nftables must be available

set -euo pipefail

if [ $# -lt 3 ]; then
    echo "Usage: $0 <publisher-id> <publisher-index> <exposed-cidrs>"
    echo ""
    echo "  publisher-id:    UUID of the publisher"
    echo "  publisher-index: Numeric index (virtual CIDR = 10.252.{index}.0/24)"
    echo "  exposed-cidrs:   Comma-separated real CIDRs (e.g., 10.0.0.0/24,192.168.1.0/24)"
    exit 1
fi

PUBLISHER_ID="$1"
PUBLISHER_INDEX="$2"
EXPOSED_CIDRS="$3"

# Derive namespace name (consistent with create-publisher-ns.sh)
ID_SHORT="${PUBLISHER_ID:0:8}"
NETNS="ns-${ID_SHORT}"

# Virtual CIDR for this publisher
VIRTUAL_CIDR="10.252.${PUBLISHER_INDEX}.0/24"

echo "═══════════════════════════════════════════"
echo "  Configuring NAT in namespace: $NETNS"
echo "  Publisher ID:    $PUBLISHER_ID"
echo "  Publisher Index: $PUBLISHER_INDEX"
echo "  Virtual CIDR:    $VIRTUAL_CIDR"
echo "  Exposed CIDRs:  $EXPOSED_CIDRS"
echo "═══════════════════════════════════════════"

# ─── Verify namespace exists ───
if ! ip netns list | grep -qw "$NETNS"; then
    echo "ERROR: Namespace '$NETNS' does not exist. Run create-publisher-ns.sh first."
    exit 1
fi

# ─── Parse exposed CIDRs ───
IFS=',' read -ra CIDRS <<< "$EXPOSED_CIDRS"
PRIMARY_CIDR="${CIDRS[0]}"

echo "[*] Primary exposed CIDR: $PRIMARY_CIDR"
echo "[*] Virtual CIDR mapping:  $VIRTUAL_CIDR → $PRIMARY_CIDR (netmap 1:1)"
if [ ${#CIDRS[@]} -gt 1 ]; then
    echo "[*] Additional CIDRs (masquerade):"
    for i in "${!CIDRS[@]}"; do
        if [ "$i" -gt 0 ]; then
            echo "      - ${CIDRS[$i]}"
        fi
    done
fi

# ─── Flush existing nat table for idempotency ───
echo "[*] Flushing existing nat table in namespace..."
ip netns exec "$NETNS" nft delete table ip nat 2>/dev/null || true

# ─── Create nftables nat table with chains ───
echo "[*] Creating nftables NAT rules..."
ip netns exec "$NETNS" nft add table ip nat
ip netns exec "$NETNS" nft add chain ip nat prerouting '{ type nat hook prerouting priority dstnat; policy accept; }'
ip netns exec "$NETNS" nft add chain ip nat postrouting '{ type nat hook postrouting priority srcnat; policy accept; }'

# ─── DNAT: virtual CIDR → primary real CIDR (netmap 1:1) ───
# Packets arriving with destination in virtual CIDR get rewritten to real CIDR
echo "[*] Adding DNAT rule: $VIRTUAL_CIDR → $PRIMARY_CIDR"
ip netns exec "$NETNS" nft add rule ip nat prerouting ip daddr "$VIRTUAL_CIDR" dnat to "$PRIMARY_CIDR"

# ─── SNAT: primary real CIDR → virtual CIDR (return traffic) ───
# Return traffic from real CIDR gets source rewritten to virtual CIDR
echo "[*] Adding SNAT rule: $PRIMARY_CIDR → $VIRTUAL_CIDR"
ip netns exec "$NETNS" nft add rule ip nat postrouting ip saddr "$PRIMARY_CIDR" snat to "$VIRTUAL_CIDR"

# ─── Handle additional CIDRs with masquerade ───
if [ ${#CIDRS[@]} -gt 1 ]; then
    echo "[*] Adding masquerade rules for additional CIDRs..."
    for i in "${!CIDRS[@]}"; do
        if [ "$i" -gt 0 ]; then
            EXTRA_CIDR="${CIDRS[$i]}"
            echo "    - Masquerade for $EXTRA_CIDR"
            ip netns exec "$NETNS" nft add rule ip nat postrouting ip daddr "$EXTRA_CIDR" masquerade
        fi
    done
fi

# ─── Verify rules were applied ───
echo ""
echo "[*] NAT rules applied in namespace '$NETNS':"
ip netns exec "$NETNS" nft list table ip nat

echo ""
echo "═══════════════════════════════════════════"
echo "  [✓] NAT configured successfully"
echo ""
echo "  DNAT (netmap): $VIRTUAL_CIDR → $PRIMARY_CIDR"
echo "  SNAT (netmap): $PRIMARY_CIDR → $VIRTUAL_CIDR"
if [ ${#CIDRS[@]} -gt 1 ]; then
    echo "  Masquerade:    additional CIDRs"
fi
echo ""
echo "  Traffic flow:"
echo "    Client → 10.252.${PUBLISHER_INDEX}.x (virtual)"
echo "         → DNAT → real IP in $PRIMARY_CIDR"
echo "         → Publisher network"
echo "         → SNAT → 10.252.${PUBLISHER_INDEX}.x (virtual)"
echo "         → Client"
echo "═══════════════════════════════════════════"

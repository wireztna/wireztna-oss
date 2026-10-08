#!/bin/bash
# list-namespaces.sh — List all active WireZTNA site namespaces with status
#
# Usage: sudo ./list-namespaces.sh

set -euo pipefail

echo "═══════════════════════════════════════════════════════════════"
echo "  WireZTNA Active Site Namespaces"
echo "═══════════════════════════════════════════════════════════════"
printf "%-20s %-18s %-15s %-10s\n" "NAMESPACE" "VETH (host)" "HOST IP" "WG PEERS"
echo "───────────────────────────────────────────────────────────────"

for ns in $(ip netns list 2>/dev/null | awk '{print $1}'); do
    VETH_HOST="veth-${ns}-h"
    
    # Get host-side IP
    HOST_IP=$(ip -4 addr show "$VETH_HOST" 2>/dev/null | grep -oP 'inet \K[\d.]+' || echo "N/A")
    
    # Count WireGuard peers in namespace
    WG_PEERS=$(ip netns exec "$ns" wg show all peers 2>/dev/null | wc -l || echo "0")
    
    printf "%-20s %-18s %-15s %-10s\n" "$ns" "$VETH_HOST" "$HOST_IP" "$WG_PEERS"
done

echo "───────────────────────────────────────────────────────────────"
echo "Total namespaces: $(ip netns list 2>/dev/null | wc -l)"

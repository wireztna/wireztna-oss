#!/bin/bash
# apply-rules.sh — Atomically apply nftables rules
#
# Usage: sudo ./apply-rules.sh [rules-file]
# Default rules file: /etc/nftables.d/wireztna-policies.nft

set -euo pipefail

RULES_FILE="${1:-/etc/nftables.d/wireztna-policies.nft}"

if [ ! -f "$RULES_FILE" ]; then
    echo "ERROR: Rules file not found: $RULES_FILE"
    exit 1
fi

echo "[*] Validating nftables rules..."
if ! nft -c -f "$RULES_FILE"; then
    echo "ERROR: Rules validation failed. Not applying."
    exit 1
fi

echo "[*] Flushing existing wireztna table..."
nft delete table inet wireztna 2>/dev/null || true

echo "[*] Applying rules from: $RULES_FILE"
nft -f "$RULES_FILE"

echo "[✓] nftables rules applied successfully"
echo ""
nft list table inet wireztna

#!/usr/bin/env bash
#
# test-namespace-overlap.sh
#
# Automated control-plane validation for overlapping CIDR support.
# Tests that two publishers with the same exposed_cidrs get different virtual_cidrs,
# and that a client config includes both virtual CIDRs in AllowedIPs.
#
# Usage:
#   ./tests/test-namespace-overlap.sh [BASE_URL]
#
# Defaults to http://localhost:8443 if no BASE_URL is provided.

set -euo pipefail

BASE_URL="${1:-http://localhost:8443}"
PASS=0
FAIL=0

# ─── Helpers ───

red()   { printf "\033[31m%s\033[0m\n" "$*"; }
green() { printf "\033[32m%s\033[0m\n" "$*"; }

assert_eq() {
    local desc="$1" expected="$2" actual="$3"
    if [ "$expected" = "$actual" ]; then
        green "  PASS: $desc"
        PASS=$((PASS + 1))
    else
        red "  FAIL: $desc (expected='$expected', got='$actual')"
        FAIL=$((FAIL + 1))
    fi
}

assert_ne() {
    local desc="$1" val1="$2" val2="$3"
    if [ "$val1" != "$val2" ]; then
        green "  PASS: $desc"
        PASS=$((PASS + 1))
    else
        red "  FAIL: $desc (both are '$val1', expected different)"
        FAIL=$((FAIL + 1))
    fi
}

assert_contains() {
    local desc="$1" haystack="$2" needle="$3"
    if echo "$haystack" | grep -q "$needle"; then
        green "  PASS: $desc"
        PASS=$((PASS + 1))
    else
        red "  FAIL: $desc ('$needle' not found in output)"
        FAIL=$((FAIL + 1))
    fi
}

api() {
    local method="$1" path="$2"
    shift 2
    curl -sf -X "$method" "${BASE_URL}${path}" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $TOKEN" \
        "$@"
}

# ─── Wait for control plane ───

echo "=== Namespace Overlap Integration Test ==="
echo "Target: $BASE_URL"
echo ""

echo "[0] Waiting for control plane..."
for i in $(seq 1 30); do
    if curl -sf "$BASE_URL/health" > /dev/null 2>&1; then
        echo "    Control plane is healthy."
        break
    fi
    if [ "$i" -eq 30 ]; then
        red "    Control plane not reachable after 30s. Aborting."
        exit 1
    fi
    sleep 1
done

# ─── Step 1: Auth setup ───

echo ""
echo "[1] Setting up admin account..."

# Try setup first; if it fails (already set up), try login
TOKEN=$(curl -sf -X POST "$BASE_URL/api/v1/auth/setup" \
    -H "Content-Type: application/json" \
    -d '{"username":"admin","password":"overlap-test-pass"}' 2>/dev/null | jq -r '.access_token // empty')

if [ -z "$TOKEN" ]; then
    TOKEN=$(curl -sf -X POST "$BASE_URL/api/v1/auth/login" \
        -H "Content-Type: application/json" \
        -d '{"username":"admin","password":"overlap-test-pass"}' | jq -r '.access_token // empty')
fi

if [ -z "$TOKEN" ]; then
    red "    Failed to authenticate. Aborting."
    exit 1
fi
echo "    Authenticated successfully."

# ─── Step 2: Create two publishers with overlapping CIDRs ───

echo ""
echo "[2] Creating two publishers with overlapping CIDRs (10.0.0.0/24)..."

PUB1=$(api POST /api/v1/publishers -d '{
    "name": "overlap-test-alpha",
    "location": "Frankfurt",
    "description": "Test publisher alpha",
    "exposed_cidrs": ["10.0.0.0/24"]
}')

PUB2=$(api POST /api/v1/publishers -d '{
    "name": "overlap-test-beta",
    "location": "London",
    "description": "Test publisher beta",
    "exposed_cidrs": ["10.0.0.0/24"]
}')

PUB1_ID=$(echo "$PUB1" | jq -r '.id')
PUB2_ID=$(echo "$PUB2" | jq -r '.id')
PUB1_INDEX=$(echo "$PUB1" | jq -r '.publisher_index')
PUB2_INDEX=$(echo "$PUB2" | jq -r '.publisher_index')
PUB1_VCIDR=$(echo "$PUB1" | jq -r '.virtual_cidr')
PUB2_VCIDR=$(echo "$PUB2" | jq -r '.virtual_cidr')

echo "    Publisher 1: id=$PUB1_ID, index=$PUB1_INDEX, virtual_cidr=$PUB1_VCIDR"
echo "    Publisher 2: id=$PUB2_ID, index=$PUB2_INDEX, virtual_cidr=$PUB2_VCIDR"

# ─── Step 3: Assert unique indices and virtual CIDRs ───

echo ""
echo "[3] Verifying unique publisher_index and virtual_cidr..."

assert_ne "publisher_index values are different" "$PUB1_INDEX" "$PUB2_INDEX"
assert_ne "virtual_cidr values are different" "$PUB1_VCIDR" "$PUB2_VCIDR"
assert_eq "publisher 1 virtual_cidr matches expected pattern" "10.252.${PUB1_INDEX}.0/24" "$PUB1_VCIDR"
assert_eq "publisher 2 virtual_cidr matches expected pattern" "10.252.${PUB2_INDEX}.0/24" "$PUB2_VCIDR"

# ─── Step 4: Create user, group, assign publishers ───

echo ""
echo "[4] Creating user, group, and assignments..."

USER=$(api POST /api/v1/users -d '{
    "username": "overlap-tester",
    "email": "overlap@test.local",
    "password": "testerpass123"
}')
USER_ID=$(echo "$USER" | jq -r '.id')
echo "    User: id=$USER_ID"

GROUP=$(api POST /api/v1/groups -d '{
    "name": "overlap-test-group",
    "description": "Group with access to both overlapping publishers"
}')
GROUP_ID=$(echo "$GROUP" | jq -r '.id')
echo "    Group: id=$GROUP_ID"

# Add user to group
api POST "/api/v1/groups/$GROUP_ID/members" \
    -d "{\"user_ids\": [\"$USER_ID\"]}" > /dev/null

# Add both publishers to group
api POST "/api/v1/groups/$GROUP_ID/publishers" \
    -d "{\"publisher_ids\": [\"$PUB1_ID\", \"$PUB2_ID\"]}" > /dev/null

echo "    User added to group, both publishers assigned."

# ─── Step 5: Get client config and verify AllowedIPs ───

echo ""
echo "[5] Generating client config and checking AllowedIPs..."

CLIENT_CONFIG=$(api GET "/api/v1/clients/$USER_ID/config")

echo "    Config excerpt:"
echo "$CLIENT_CONFIG" | grep -i "AllowedIPs" | sed 's/^/        /'

assert_contains "AllowedIPs includes publisher 1 virtual CIDR ($PUB1_VCIDR)" "$CLIENT_CONFIG" "$PUB1_VCIDR"
assert_contains "AllowedIPs includes publisher 2 virtual CIDR ($PUB2_VCIDR)" "$CLIENT_CONFIG" "$PUB2_VCIDR"

# ─── Cleanup ───

echo ""
echo "[6] Cleaning up test resources..."

api DELETE "/api/v1/groups/$GROUP_ID/publishers/$PUB1_ID" > /dev/null 2>&1 || true
api DELETE "/api/v1/groups/$GROUP_ID/publishers/$PUB2_ID" > /dev/null 2>&1 || true
api DELETE "/api/v1/groups/$GROUP_ID/members/$USER_ID" > /dev/null 2>&1 || true
api DELETE "/api/v1/groups/$GROUP_ID" > /dev/null 2>&1 || true
api DELETE "/api/v1/users/$USER_ID" > /dev/null 2>&1 || true
api DELETE "/api/v1/publishers/$PUB1_ID" > /dev/null 2>&1 || true
api DELETE "/api/v1/publishers/$PUB2_ID" > /dev/null 2>&1 || true

echo "    Cleanup complete."

# ─── Summary ───

echo ""
echo "=== Results ==="
green "  Passed: $PASS"
if [ "$FAIL" -gt 0 ]; then
    red "  Failed: $FAIL"
    exit 1
else
    echo "  Failed: 0"
    green "  All tests passed!"
fi

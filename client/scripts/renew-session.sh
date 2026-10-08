#!/bin/bash
# renew-session.sh — WireZTNA client session renewal (Linux & macOS)
#
# Authenticates with the control plane and obtains a fresh PSK to keep
# the WireGuard tunnel alive. Without a valid PSK, the broker rejects
# the handshake and the tunnel dies.
#
# Compatible with: Linux (bash 4+), macOS (bash 3.2+ / zsh)
#
# This script is designed to:
# 1. Run automatically (cron/launchd/systemd timer) before session expiry
# 2. Run on-demand when the tunnel is detected as down
#
# Required environment:
#   WIREZTNA_API_URL    - Control plane API URL (e.g., https://ztna.example.com)
#   WIREZTNA_USERNAME   - Username for authentication
#   WIREZTNA_PASSWORD   - Password (or set interactively)
#
# Optional:
#   WIREZTNA_TOKEN_FILE - Path to cache JWT (default: ~/.wireztna/token)
#   WG_INTERFACE        - WireGuard interface name (default: wg-wireztna)
#   WG_CONF_FILE        - WireGuard config file (auto-detected per OS)
#
# Future extensions:
#   - OIDC/IdP browser-based auth flow (replace user/pass with token exchange)
#   - MFA challenge handling
#   - Device posture reporting at renewal time

set -euo pipefail

# ─── OS Detection ───
OS="$(uname -s)"
case "$OS" in
    Linux)  PLATFORM="linux" ;;
    Darwin) PLATFORM="macos" ;;
    *)      echo "ERROR: Unsupported OS: $OS (use renew-session.ps1 for Windows)"; exit 1 ;;
esac

# ─── Configuration ───
API_URL="${WIREZTNA_API_URL:?ERROR: WIREZTNA_API_URL required}"
USERNAME="${WIREZTNA_USERNAME:-}"
TOKEN_DIR="${HOME}/.wireztna"
TOKEN_FILE="${WIREZTNA_TOKEN_FILE:-${TOKEN_DIR}/token}"
WG_INTERFACE="${WG_INTERFACE:-wg-wireztna}"

# Default config paths differ per OS
if [ "$PLATFORM" = "macos" ]; then
    WG_CONF_FILE="${WG_CONF_FILE:-/usr/local/etc/wireguard/${WG_INTERFACE}.conf}"
else
    WG_CONF_FILE="${WG_CONF_FILE:-/etc/wireguard/${WG_INTERFACE}.conf}"
fi

mkdir -p "$TOKEN_DIR"
chmod 700 "$TOKEN_DIR"

# ─── Helper functions ───

log() {
    echo "[$(date '+%H:%M:%S')] $*" >&2
}

# base64 decode is different on macOS vs Linux
b64decode() {
    if [ "$PLATFORM" = "macos" ]; then
        base64 -D 2>/dev/null || true
    else
        base64 -d 2>/dev/null || true
    fi
}

# sed in-place differs: macOS BSD sed requires '' after -i, GNU sed does not
sed_inplace() {
    if [ "$PLATFORM" = "macos" ]; then
        sed -i '' "$@"
    else
        sed -i "$@"
    fi
}

# ping timeout flag differs
ping_once() {
    local host="$1"
    if [ "$PLATFORM" = "macos" ]; then
        ping -c 1 -W 2000 "$host" &>/dev/null
    else
        ping -c 1 -W 2 "$host" &>/dev/null
    fi
}

# ─── macOS Split DNS ───
# Instead of letting wg-quick hijack all DNS, we use /etc/resolver/ to only
# route specific internal zones through the tunnel DNS. All other DNS queries
# continue using the system's default resolver (router, ISP, etc.)
#
# This requires knowing which zones should go through the tunnel. We query
# the control plane for configured zones, or fall back to a catch-all approach
# using a scoped resolver on the utun interface.

RESOLVER_DIR="/etc/resolver"
WIREZTNA_RESOLVER_TAG="# wireztna-managed"

setup_macos_split_dns() {
    local tunnel_dns="$1"

    log "Configuring macOS split DNS (tunnel DNS: $tunnel_dns)..."

    # Fetch internal zones from the control plane (if available)
    local zones=""
    if [ -n "${token:-}" ]; then
        local zone_response
        zone_response=$(curl -s -H "Authorization: Bearer $token" \
            "${API_URL}/api/v1/sessions/dns-zones" 2>/dev/null || true)
        zones=$(echo "$zone_response" | grep -o '"zones":\[[^]]*\]' | \
            grep -o '"[^"]*"' | tr -d '"' | grep -v '^zones$' || true)
    fi

    # Create resolver directory if it doesn't exist
    sudo mkdir -p "$RESOLVER_DIR"

    if [ -n "$zones" ]; then
        # Create a resolver file per internal zone
        while IFS= read -r zone; do
            [ -z "$zone" ] && continue
            local resolver_file="${RESOLVER_DIR}/${zone}"
            log "  Creating resolver: $resolver_file → $tunnel_dns"
            printf '%s\nnameserver %s\ntimeout 2\n' "$WIREZTNA_RESOLVER_TAG" "$tunnel_dns" | \
                sudo tee "$resolver_file" > /dev/null
        done <<< "$zones"
    else
        # Fallback: no zones from API — use the WireGuard interface as a scoped resolver
        # This routes DNS for the tunnel's overlay network through the tunnel
        # but leaves all other DNS untouched
        log "  No zones from API — configuring fallback resolver for overlay networks"

        # Create a resolver for the overlay domain (assumes broker serves *.ztna.internal)
        local resolver_file="${RESOLVER_DIR}/ztna.internal"
        printf '%s\nnameserver %s\ntimeout 2\n' "$WIREZTNA_RESOLVER_TAG" "$tunnel_dns" | \
            sudo tee "$resolver_file" > /dev/null

        log "  Created: $resolver_file → $tunnel_dns"
        log "  TIP: Configure DNS zones in the control plane for proper split DNS"
    fi

    log "Split DNS configured — local resolution preserved"
}

cleanup_macos_split_dns() {
    if [ "$PLATFORM" != "macos" ]; then
        return
    fi
    if [ ! -d "$RESOLVER_DIR" ]; then
        return
    fi

    log "Cleaning up wireztna resolver files..."
    # Only remove files we created (tagged with our comment)
    for f in "$RESOLVER_DIR"/*; do
        [ -f "$f" ] || continue
        if grep -q "$WIREZTNA_RESOLVER_TAG" "$f" 2>/dev/null; then
            log "  Removing: $f"
            sudo rm -f "$f"
        fi
    done
}

get_cached_token() {
    if [ -f "$TOKEN_FILE" ]; then
        local token
        token=$(cat "$TOKEN_FILE")
        # Basic check: token should be non-empty and have 3 JWT parts
        if [ -n "$token" ] && echo "$token" | grep -qE '^[^.]+\.[^.]+\.[^.]+$'; then
            # Check if token is expired (decode payload, check exp)
            local payload exp now
            payload=$(echo "$token" | cut -d. -f2 | b64decode)
            exp=$(echo "$payload" | grep -o '"exp":[0-9]*' | cut -d: -f2)
            now=$(date +%s)
            if [ -n "$exp" ] && [ "$now" -lt "$exp" ]; then
                echo "$token"
                return 0
            fi
        fi
    fi
    return 1
}

authenticate() {
    # FUTURE: Replace with OIDC browser flow or device code flow
    if [ -z "$USERNAME" ]; then
        read -rp "Username: " USERNAME
    fi

    local password="${WIREZTNA_PASSWORD:-}"
    if [ -z "$password" ]; then
        read -rsp "Password: " password
        echo
    fi

    log "Authenticating as $USERNAME..."

    local response http_code body
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "Content-Type: application/json" \
        -d "{\"username\": \"$USERNAME\", \"password\": \"$password\"}" \
        "${API_URL}/api/v1/auth/login")

    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" != "200" ]; then
        log "ERROR: Authentication failed (HTTP $http_code)"
        log "  $body"
        exit 1
    fi

    local token
    token=$(echo "$body" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)

    if [ -z "$token" ]; then
        log "ERROR: No token in response"
        exit 1
    fi

    # Cache token
    echo "$token" > "$TOKEN_FILE"
    chmod 600 "$TOKEN_FILE"

    log "Authentication successful — token cached"
    echo "$token"
}

# ─── Group Selection (for CIDR overlap resolution) ───

select_group() {
    local token="$1"

    # Fetch available groups
    local response
    response=$(curl -s -H "Authorization: Bearer $token" \
        "${API_URL}/api/v1/sessions/available-groups" 2>/dev/null || true)

    if [ -z "$response" ]; then
        echo ""
        return
    fi

    # Check if selection is recommended (CIDR overlap detected)
    local selection_recommended
    selection_recommended=$(echo "$response" | grep -o '"selection_recommended":[a-z]*' | cut -d: -f2)

    if [ "$selection_recommended" != "true" ]; then
        # No overlap — connect to all groups
        echo ""
        return
    fi

    # Show selector
    log "Overlapping networks detected between groups:"
    echo "" >&2
    echo "  [0] All groups (routing may be ambiguous for overlapping CIDRs)" >&2

    # Parse and display groups
    local idx=1
    while true; do
        local name
        name=$(echo "$response" | python3 -c "
import sys, json
d = json.load(sys.stdin)
groups = d.get('groups', [])
if $idx <= len(groups):
    g = groups[$idx - 1]
    online = g.get('online_publishers', 0)
    total = len(g.get('publishers', []))
    suffix = ''
    if online == 0:
        suffix = '  (no publishers online — unavailable)'
    elif total > 0:
        suffix = f'  ({online}/{total} online)'
    print(f\"{g['name']} ({', '.join(g['cidrs'])}){suffix}\")
else:
    print('')
" 2>/dev/null)

        if [ -z "$name" ]; then
            break
        fi

        echo "  [$idx] $name" >&2
        idx=$((idx + 1))
    done

    echo "" >&2

    # Check if WIREZTNA_GROUP is set (non-interactive mode)
    if [ -n "${WIREZTNA_GROUP:-}" ]; then
        # Special value "all" or empty means no group filter
        if [ "${WIREZTNA_GROUP}" = "all" ] || [ "${WIREZTNA_GROUP}" = "ALL" ]; then
            log "Using all groups (no filter)"
            echo ""
            return
        fi
        log "Using pre-selected group: $WIREZTNA_GROUP"
        # Find group ID by name or index, reject if no online publishers
        local selected_id
        selected_id=$(echo "$response" | python3 -c "
import sys, json
d = json.load(sys.stdin)
target = '${WIREZTNA_GROUP}'
for g in d.get('groups', []):
    if g['name'] == target or g['id'] == target or g['id'].startswith(target):
        if g.get('online_publishers', 0) == 0:
            print('UNAVAILABLE:' + g['name'], file=sys.stderr)
            sys.exit(1)
        print(g['id'])
        break
" 2>/dev/null)
        if [ $? -ne 0 ]; then
            log "ERROR: Group '${WIREZTNA_GROUP}' has no publishers online — cannot connect"
            exit 1
        fi
        echo "$selected_id"
        return
    fi

    # Interactive selection
    read -rp "  Select project [0=all]: " choice < /dev/tty
    choice="${choice:-0}"

    # Option 0 = all groups (no filter)
    if [ "$choice" = "0" ]; then
        log "Selected: All groups (unrestricted)"
        echo ""
        return
    fi

    local selected_id
    selected_id=$(echo "$response" | python3 -c "
import sys, json
d = json.load(sys.stdin)
groups = d.get('groups', [])
idx = int('${choice}') - 1
if 0 <= idx < len(groups):
    g = groups[idx]
    if g.get('online_publishers', 0) == 0:
        print(f\"ERROR: '{g[\"name\"]}' has no publishers online\", file=sys.stderr)
        sys.exit(1)
    print(g['id'])
" 2>/dev/null)

    if [ $? -ne 0 ] || [ -z "$selected_id" ]; then
        log "ERROR: Invalid selection or group unavailable (no publishers online)"
        exit 1
    fi

    if [ -n "$selected_id" ]; then
        local selected_name
        selected_name=$(echo "$response" | python3 -c "
import sys, json
d = json.load(sys.stdin)
groups = d.get('groups', [])
idx = int('${choice}') - 1
if 0 <= idx < len(groups):
    print(groups[idx]['name'])
" 2>/dev/null)
        log "Selected project: $selected_name"
    fi

    echo "$selected_id"
}

renew_psk() {
    local token="$1"
    local group_id="${2:-}"

    log "Requesting new PSK from control plane..."

    # Build URL with optional group_id
    local url="${API_URL}/api/v1/sessions/renew"
    if [ -n "$group_id" ]; then
        url="${url}?group_id=${group_id}"
    fi

    local response http_code body
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $token" \
        "$url")

    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "401" ] || [ "$http_code" = "403" ]; then
        # Token expired or user revoked — need to re-authenticate
        log "Token rejected (HTTP $http_code) — re-authenticating..."
        rm -f "$TOKEN_FILE"
        token=$(authenticate)
        # Retry renewal with fresh token
        response=$(curl -s -w "\n%{http_code}" \
            -X POST \
            -H "Content-Type: application/json" \
            -H "Authorization: Bearer $token" \
            "$url")
        http_code=$(echo "$response" | tail -1)
        body=$(echo "$response" | sed '$d')
    fi

    if [ "$http_code" != "200" ]; then
        log "ERROR: Session renewal failed (HTTP $http_code)"
        log "  $body"
        exit 1
    fi

    local psk ttl
    psk=$(echo "$body" | grep -o '"preshared_key":"[^"]*"' | cut -d'"' -f4)
    ttl=$(echo "$body" | grep -o '"ttl_seconds":[0-9]*' | cut -d: -f2)

    if [ -z "$psk" ]; then
        log "ERROR: No PSK in response"
        exit 1
    fi

    log "New PSK received (TTL: ${ttl}s)"
    echo "$psk"
}

apply_psk() {
    local psk="$1"

    log "Applying new PSK to WireGuard interface..."

    # Update the config file first
    if grep -q 'PresharedKey' "$WG_CONF_FILE"; then
        sed_inplace "s|^PresharedKey = .*|PresharedKey = $psk|" "$WG_CONF_FILE"
    else
        # Add PSK line after PublicKey in [Peer] section
        if [ "$PLATFORM" = "macos" ]; then
            sed -i '' "/^PublicKey = .*/a\\
PresharedKey = $psk
" "$WG_CONF_FILE"
        else
            sed -i "/^PublicKey = .*/a PresharedKey = $psk" "$WG_CONF_FILE"
        fi
    fi

    # ─── macOS split DNS handling ───
    # On macOS, wg-quick's DNS= directive hijacks ALL system DNS resolvers,
    # which breaks local/public DNS resolution. Instead, we:
    # 1. Strip DNS= from the conf before wg-quick up (so it doesn't touch resolvers)
    # 2. Configure split DNS via /etc/resolver/ for internal zones only
    # 3. Local/public DNS continues working through the default resolver
    local conf_dns=""
    if [ "$PLATFORM" = "macos" ]; then
        # Extract DNS value — handle potential double-prefix (DNS = DNS = x.x.x.x)
        conf_dns=$(grep -E '^DNS[[:space:]]*=' "$WG_CONF_FILE" | head -1 | sed 's/^DNS[[:space:]]*=[[:space:]]*//' | sed 's/^DNS[[:space:]]*=[[:space:]]*//' | xargs)
        if [ -n "$conf_dns" ]; then
            # Remove ALL DNS lines from conf so wg-quick doesn't override system DNS
            sed_inplace '/^DNS[[:space:]]*=/d' "$WG_CONF_FILE"
            log "Stripped DNS from conf (tunnel DNS: $conf_dns)"
        fi
    fi

    # Clean up any previous split DNS config before restarting
    cleanup_macos_split_dns

    # Restart the interface cleanly
    wg-quick down "$WG_CONF_FILE" 2>/dev/null || true
    sleep 1
    wg-quick up "$WG_CONF_FILE"

    # ─── Restore DNS line in conf and configure split DNS ───
    if [ "$PLATFORM" = "macos" ] && [ -n "$conf_dns" ]; then
        # Restore the DNS line in the conf file (for consistency/future use)
        # Use awk to insert after [Interface] — compatible with both BSD and GNU
        local tmpfile="${WG_CONF_FILE}.tmp"
        awk -v dns="DNS = $conf_dns" '/^\[Interface\]/{print; print dns; next}1' "$WG_CONF_FILE" > "$tmpfile"
        mv "$tmpfile" "$WG_CONF_FILE"

        # Configure macOS split DNS via /etc/resolver/
        setup_macos_split_dns "$conf_dns"
    fi

    # Update WG_INTERFACE to whatever wg-quick assigned (it may change on restart)
    WG_INTERFACE=$(wg show interfaces | head -1)

    log "PSK applied — interface restarted with new PSK"
}

check_tunnel_up() {
    # Try HTTP health check first (more reliable than ICMP through WG)
    # The broker API listens on the overlay IP too
    if curl -sf --max-time 2 "http://10.200.0.1:8443/health" >/dev/null 2>&1; then
        return 0
    fi
    # Fallback to ping
    ping_once "10.200.0.1"
}

# ─── Main ───

log "═══ WireZTNA Session Renewal ($PLATFORM) ═══"

# Step 1: Get or refresh JWT
token=""
if token=$(get_cached_token); then
    log "Using cached token"
else
    log "No valid cached token"
    token=$(authenticate)
fi

# Step 1.5: Select group/project if CIDR overlap exists
selected_group=""
selected_group=$(select_group "$token")

# Step 2: Request new PSK (with optional group selection)
psk=$(renew_psk "$token" "$selected_group")

# Step 3: Apply PSK to WireGuard
apply_psk "$psk"

# Step 4: Verify tunnel comes back (wait for re-handshake)
# The broker reconciler polls every 10s for config changes — the new PSK
# won't be applied to the broker's WG peer until the next poll cycle.
log "Waiting for broker to apply new PSK (reconciler polls every ~10s)..."
sleep 10
for i in $(seq 1 8); do
    if check_tunnel_up; then
        log "═══ Tunnel active — session renewed successfully ═══"
        exit 0
    fi
    log "  Waiting... (${i}/8)"
    sleep 5
done

log "WARNING: Tunnel did not come up within 50s — check broker reconciler"
exit 1

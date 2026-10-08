#!/bin/bash
# heartbeat.sh — Publisher heartbeat reporter
#
# Sends periodic health status to the control plane.
# Designed to run as a systemd timer or in a loop.
#
# Required environment:
#   CONTROL_PLANE_URL - Control plane API URL
#   PUBLISHER_API_KEY - API key received during enrollment
#   PUBLISHER_ID      - Publisher ID from enrollment
#
# Optional:
#   HEARTBEAT_INTERVAL - Seconds between heartbeats (default: 30)
#   WG_INTERFACE       - WireGuard interface name (default: wg-broker)

set -euo pipefail

CONTROL_PLANE_URL="${CONTROL_PLANE_URL:?ERROR: CONTROL_PLANE_URL required}"
PUBLISHER_ID="${PUBLISHER_ID:-}"
WG_INTERFACE="${WG_INTERFACE:-wg-broker}"
CONFIG_DIR="${CONFIG_DIR:-/etc/wireguard}"
STATE_FILE="${CONFIG_DIR}/.enrolled"

# ─── Load publisher ID from enrollment state if not set ───
if [ -z "$PUBLISHER_ID" ] && [ -f "$STATE_FILE" ]; then
    PUBLISHER_ID=$(grep "publisher_id=" "$STATE_FILE" | cut -d= -f2)
fi

# ─── Load publisher API key from enrollment state or env ───
if [ -z "${PUBLISHER_API_KEY:-}" ] && [ -f "$STATE_FILE" ]; then
    PUBLISHER_API_KEY=$(grep "publisher_api_key=" "$STATE_FILE" | cut -d= -f2 || true)
fi

if [ -z "$PUBLISHER_ID" ]; then
    echo "ERROR: PUBLISHER_ID not set and no enrollment state found"
    exit 1
fi

# ─── Gather metrics ───
gather_metrics() {
    local uptime_seconds
    uptime_seconds=$(awk '{print int($1)}' /proc/uptime)
    
    # WireGuard handshake age (seconds since last handshake)
    local handshake_age="null"
    local latest_handshake
    latest_handshake=$(wg show "$WG_INTERFACE" latest-handshakes 2>/dev/null | awk '{print $2}' | head -1)
    if [ -n "$latest_handshake" ] && [ "$latest_handshake" != "0" ]; then
        handshake_age=$(( $(date +%s) - latest_handshake ))
    fi
    
    # Interface traffic stats
    local rx_bytes tx_bytes
    rx_bytes=$(wg show "$WG_INTERFACE" transfer 2>/dev/null | awk '{sum+=$2} END {print sum+0}')
    tx_bytes=$(wg show "$WG_INTERFACE" transfer 2>/dev/null | awk '{sum+=$3} END {print sum+0}')
    
    # Peer count
    local peer_count
    peer_count=$(wg show "$WG_INTERFACE" peers 2>/dev/null | wc -l)
    
    # Auto-detect local DNS resolver from host
    # In Docker, /etc/resolv.conf contains the host's DNS (or Docker's DNS proxy at 127.0.0.11)
    # If 127.0.0.11 (Docker internal DNS), try to get the real upstream via /etc/resolv.conf on the host
    local local_dns=""
    local_dns=$(grep -m1 '^nameserver' /etc/resolv.conf 2>/dev/null | awk '{print $2}' || true)
    
    # Docker's internal DNS is 127.0.0.11 — not useful for external access
    # In that case, try the host's resolv.conf (mounted or available via Docker)
    if [ "$local_dns" = "127.0.0.11" ]; then
        # Try host resolv.conf if mounted
        if [ -f /host/etc/resolv.conf ]; then
            local_dns=$(grep -m1 '^nameserver' /host/etc/resolv.conf | awk '{print $2}' || true)
        else
            # Fallback: query Docker's DNS for a known internal domain to find the real resolver
            # On AWS: VPC DNS is always at CIDR+2, but we can't know the CIDR from inside
            # Best effort: use what Docker gives us
            local_dns=""
        fi
    fi

    # Build local_dns JSON value
    local local_dns_json="null"
    if [ -n "$local_dns" ]; then
        local_dns_json="\"$local_dns\""
    fi

    # Agent version — from env var or /etc/wireztna-version file (null if not available)
    local agent_version_json="null"
    local agent_ver="${AGENT_VERSION:-}"
    if [ -z "$agent_ver" ] && [ -f /etc/wireztna-version ]; then
        agent_ver=$(cat /etc/wireztna-version 2>/dev/null | tr -d '[:space:]')
    fi
    if [ -n "$agent_ver" ]; then
        agent_version_json="\"$agent_ver\""
    fi

    cat << EOF
{
    "publisher_id": "$PUBLISHER_ID",
    "timestamp": "$(date -Iseconds)",
    "uptime_seconds": $uptime_seconds,
    "handshake_age_seconds": $handshake_age,
    "rx_bytes": $rx_bytes,
    "tx_bytes": $tx_bytes,
    "peer_count": $peer_count,
    "wg_interface": "$WG_INTERFACE",
    "status": "online",
    "local_dns": $local_dns_json,
    "agent_version": $agent_version_json
}
EOF
}

# ─── Send heartbeat ───
send_heartbeat() {
    local metrics
    metrics=$(gather_metrics)
    
    local response
    response=$(curl -s -w "\n%{http_code}" \
        -X POST \
        -H "Content-Type: application/json" \
        -H "X-API-Key: ${PUBLISHER_API_KEY:-}" \
        -d "$metrics" \
        "${CONTROL_PLANE_URL}/api/v1/publishers/${PUBLISHER_ID}/heartbeat")
    
    local http_code
    http_code=$(echo "$response" | tail -1)
    
    if [ "$http_code" = "200" ] || [ "$http_code" = "204" ]; then
        return 0
    else
        echo "[WARN] Heartbeat failed (HTTP $http_code)" >&2
        return 1
    fi
}

# ─── Watchdog: detect stale handshake and auto-recover ───
# If the WG handshake is stale (>180s), the tunnel might be broken because:
# 1. The broker recreated the namespace on a different port (zombie socket cleanup)
# 2. The broker's namespace key changed (reconciler recreated)
# In either case, poll /connection-info to get the current broker endpoint/key
# and update the local WG config if needed.
WATCHDOG_THRESHOLD="${WATCHDOG_THRESHOLD:-180}"  # seconds of stale handshake before recovery
LAST_RECOVERY_ATTEMPT=0
RECOVERY_COOLDOWN=60  # Don't attempt recovery more than once per 60s

check_and_recover_tunnel() {
    local latest_handshake
    latest_handshake=$(wg show "$WG_INTERFACE" latest-handshakes 2>/dev/null | awk '{print $2}' | head -1)
    
    # If no handshake ever, or stale beyond threshold
    local now
    now=$(date +%s)
    local handshake_age=999999
    if [ -n "$latest_handshake" ] && [ "$latest_handshake" != "0" ]; then
        handshake_age=$(( now - latest_handshake ))
    fi

    if [ "$handshake_age" -lt "$WATCHDOG_THRESHOLD" ]; then
        return 0  # Tunnel healthy
    fi

    # Cooldown check — don't spam recovery attempts
    if [ $(( now - LAST_RECOVERY_ATTEMPT )) -lt "$RECOVERY_COOLDOWN" ]; then
        return 0
    fi
    LAST_RECOVERY_ATTEMPT="$now"

    echo "[WATCHDOG] Handshake stale (${handshake_age}s > ${WATCHDOG_THRESHOLD}s) — checking for endpoint change..."

    # Poll connection-info to see if broker changed port/key
    local conn_url="${CONTROL_PLANE_URL}/api/v1/publishers/${PUBLISHER_ID}/connection-info"
    local conn_response
    conn_response=$(curl -sf -H "X-API-Key: ${PUBLISHER_API_KEY:-}" "$conn_url" 2>/dev/null || echo '{"ready":false}')
    local ready
    ready=$(echo "$conn_response" | jq -r '.ready // false')

    if [ "$ready" != "true" ]; then
        echo "[WATCHDOG] Broker namespace not ready yet — will retry next cycle"
        return 1
    fi

    local new_endpoint
    new_endpoint=$(echo "$conn_response" | jq -r '.broker_endpoint // ""')
    local new_pubkey
    new_pubkey=$(echo "$conn_response" | jq -r '.broker_public_key // ""')

    if [ -z "$new_endpoint" ] || [ -z "$new_pubkey" ]; then
        echo "[WATCHDOG] connection-info returned incomplete data — skipping"
        return 1
    fi

    # Compare with current WG config
    local current_endpoint
    current_endpoint=$(wg show "$WG_INTERFACE" endpoints 2>/dev/null | awk '{print $2}' | head -1)
    # Resolve hostname in new_endpoint for comparison (wg show reports IP:port)
    local new_host new_port
    new_host=$(echo "$new_endpoint" | cut -d: -f1)
    new_port=$(echo "$new_endpoint" | cut -d: -f2)
    local resolved_ip
    resolved_ip=$(getent hosts "$new_host" 2>/dev/null | awk '{print $1}' | head -1 || echo "$new_host")
    local new_endpoint_resolved="${resolved_ip}:${new_port}"

    local current_peer_key
    current_peer_key=$(wg show "$WG_INTERFACE" peers 2>/dev/null | head -1)

    local changed=false

    if [ "$new_endpoint_resolved" != "$current_endpoint" ] && [ "$new_endpoint" != "$current_endpoint" ]; then
        echo "[WATCHDOG] Broker endpoint changed: $current_endpoint → $new_endpoint"
        changed=true
    fi

    if [ "$new_pubkey" != "$current_peer_key" ]; then
        echo "[WATCHDOG] Broker public key changed: ${current_peer_key:0:12}... → ${new_pubkey:0:12}..."
        changed=true
    fi

    if [ "$changed" = "true" ]; then
        echo "[WATCHDOG] Applying new broker config..."
        # Update the peer in-place (no need to restart the interface)
        local allowed_ips
        allowed_ips=$(wg show "$WG_INTERFACE" allowed-ips 2>/dev/null | awk '{print $2}' | tr '\n' ',' | sed 's/,$//')
        
        # Remove old peer and add new one
        if [ -n "$current_peer_key" ]; then
            wg set "$WG_INTERFACE" peer "$current_peer_key" remove 2>/dev/null || true
        fi
        wg set "$WG_INTERFACE" peer "$new_pubkey" \
            endpoint "$new_endpoint" \
            allowed-ips "${allowed_ips:-10.200.0.0/16,10.100.0.0/16}" \
            persistent-keepalive 25

        # Update config file for persistence across restarts
        local config_file="${CONFIG_DIR}/wg-broker.conf"
        if [ -f "$config_file" ]; then
            sed -i "s|^PublicKey = .*|PublicKey = $new_pubkey|" "$config_file"
            sed -i "s|^Endpoint = .*|Endpoint = $new_endpoint|" "$config_file"
            echo "[WATCHDOG] Updated $config_file"
        fi

        echo "[WATCHDOG] Recovery applied — waiting for handshake..."
    else
        # Endpoint and key are the same — just restart the interface to force re-handshake
        echo "[WATCHDOG] Config unchanged but tunnel stale — bouncing interface..."
        wg-quick down "$WG_INTERFACE" 2>/dev/null || ip link del "$WG_INTERFACE" 2>/dev/null || true
        sleep 2
        wg-quick up "$WG_INTERFACE" 2>/dev/null || true
        echo "[WATCHDOG] Interface bounced"
    fi
}

# ─── Main: single shot or loop ───
if [ "${1:-}" = "--loop" ]; then
    INTERVAL="${HEARTBEAT_INTERVAL:-30}"
    echo "[*] Starting heartbeat loop (interval: ${INTERVAL}s)"
    while true; do
        send_heartbeat || true
        # Run watchdog check every heartbeat cycle
        check_and_recover_tunnel || true
        sleep "$INTERVAL"
    done
else
    send_heartbeat
fi

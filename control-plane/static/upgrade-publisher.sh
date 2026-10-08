#!/bin/bash
# WireZTNA Publisher — Upgrade (Docker)
#
# Rebuilds and restarts the publisher container with fresh scripts from the broker.
# Enrollment state is preserved in the wireztna-config volume — no re-enrollment needed.
#
# Usage:
#   curl -sf http://<broker>/api/v1/publishers/upgrade.sh | sudo bash
#
# What it does:
#   1. Reads current publisher config from the running container
#   2. Stops and removes the container (NOT the volume)
#   3. Downloads fresh scripts from the broker (enroll.sh, heartbeat.sh, entrypoint.sh)
#   4. Rebuilds the Docker image with updated scripts
#   5. Starts a new container with the same config
#   6. Verifies the publisher comes back online
#
# What it preserves:
#   - WireGuard keys and enrollment state (wireztna-config volume)
#   - Publisher identity (publisher_id, tunnel_ip, broker endpoint)
#   - No re-enrollment needed
#
# What it updates:
#   - Publisher scripts (heartbeat.sh, enroll.sh, entrypoint.sh)
#   - Base image (Alpine + dependencies)
#   - Agent version string

set -euo pipefail

CONTAINER_NAME="wireztna-publisher"
VOLUME_NAME="wireztna-config"
CONTROL_PLANE_URL="${CONTROL_PLANE_URL:-__CONTROL_PLANE_URL__}"
NEW_VERSION="${PUBLISHER_VERSION:-__PUBLISHER_VERSION__}"
IMAGE_NAME="wireztna-publisher:${NEW_VERSION}"

echo "═══════════════════════════════════════════════"
echo "  WireZTNA Publisher — Upgrade"
echo "═══════════════════════════════════════════════"
echo ""
echo "  Broker:      $CONTROL_PLANE_URL"
echo "  New version: $NEW_VERSION"
echo ""

# ─── Pre-flight checks ───

if ! command -v docker &>/dev/null; then
    echo "ERROR: Docker not found. Is this a Docker publisher host?"
    exit 1
fi

# Check if container exists
if ! docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "ERROR: Container '$CONTAINER_NAME' not found."
    echo "       This script upgrades an existing Docker publisher."
    echo "       For first-time install, use install.sh instead."
    exit 1
fi

# Check volume exists (enrollment state)
if ! docker volume ls --format '{{.Name}}' | grep -q "^${VOLUME_NAME}$"; then
    echo "ERROR: Volume '$VOLUME_NAME' not found."
    echo "       The enrollment state would be lost. Aborting."
    exit 1
fi

# ─── Read current config from running container ───
echo "[*] Reading current publisher configuration..."

# Extract env vars from the existing container
CURRENT_ENV=$(docker inspect "$CONTAINER_NAME" --format '{{range .Config.Env}}{{println .}}{{end}}')

get_env() {
    echo "$CURRENT_ENV" | grep "^${1}=" | cut -d= -f2- || echo ""
}

PUB_NAME=$(get_env "PUBLISHER_NAME")
PUB_CONTROL_PLANE=$(get_env "CONTROL_PLANE_URL")
PUB_HEARTBEAT_INTERVAL=$(get_env "HEARTBEAT_INTERVAL")
PUB_WG_INTERFACE=$(get_env "WG_INTERFACE")
PUB_API_KEY=$(get_env "PUBLISHER_API_KEY")
CURRENT_VERSION=$(get_env "AGENT_VERSION")

# Read publisher ID from the enrollment state inside the volume
PUB_ID=$(docker exec "$CONTAINER_NAME" sh -c 'grep "publisher_id=" /etc/wireguard/.enrolled 2>/dev/null | cut -d= -f2' || echo "")

# Use container's control plane URL if we got the placeholder
if [ "$CONTROL_PLANE_URL" = "__CONTROL_PLANE_URL__" ] && [ -n "$PUB_CONTROL_PLANE" ]; then
    CONTROL_PLANE_URL="$PUB_CONTROL_PLANE"
fi

echo "  Publisher:       ${PUB_NAME:-unknown}"
echo "  Current version: ${CURRENT_VERSION:-unknown}"
echo "  Control plane:   ${CONTROL_PLANE_URL}"
echo ""

# ─── Check connectivity to broker ───
echo "[*] Checking broker connectivity..."
if ! curl -sf "${CONTROL_PLANE_URL}/api/v1/diagnostics/quick" >/dev/null 2>&1; then
    echo "ERROR: Cannot reach broker at ${CONTROL_PLANE_URL}"
    echo "       Check network connectivity and try again."
    exit 1
fi
echo "[✓] Broker reachable"

# ─── Download fresh scripts ───
WORK_DIR=$(mktemp -d)
echo "[*] Downloading updated scripts..."

DOWNLOAD_FAILED=false
for script in entrypoint.sh enroll.sh heartbeat.sh; do
    if curl -sf "${CONTROL_PLANE_URL}/api/v1/publishers/scripts/${script}" -o "${WORK_DIR}/${script}"; then
        echo "    ✓ ${script}"
    else
        echo "    ✗ ${script} — FAILED"
        DOWNLOAD_FAILED=true
    fi
done

if [ "$DOWNLOAD_FAILED" = "true" ]; then
    echo "ERROR: Failed to download some scripts. Aborting upgrade."
    echo "       The existing publisher is still running."
    rm -rf "$WORK_DIR"
    exit 1
fi
chmod +x "${WORK_DIR}"/*.sh

# ─── Build new image ───
echo "[*] Building updated publisher image ($IMAGE_NAME)..."

cat > "${WORK_DIR}/Dockerfile" << 'DOCKERFILE'
FROM alpine:3.19
RUN apk add --no-cache wireguard-tools iptables curl jq bash && echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf
ARG VERSION=unknown
RUN echo "$VERSION" > /etc/wireztna-version
COPY entrypoint.sh enroll.sh heartbeat.sh /opt/wireztna/
RUN chmod +x /opt/wireztna/*.sh
ENV CONFIG_DIR=/etc/wireguard
ENV WG_INTERFACE=wg-broker
ENV HEARTBEAT_INTERVAL=30
ENTRYPOINT ["/opt/wireztna/entrypoint.sh"]
DOCKERFILE

if ! docker build -t "$IMAGE_NAME" --build-arg "VERSION=${NEW_VERSION}" "$WORK_DIR" -q; then
    echo "ERROR: Docker build failed. The existing publisher is still running."
    rm -rf "$WORK_DIR"
    exit 1
fi
rm -rf "$WORK_DIR"
echo "[✓] Image built: $IMAGE_NAME"

# ─── Stop existing container ───
echo "[*] Stopping current publisher..."
docker stop "$CONTAINER_NAME" >/dev/null 2>&1 || true
docker rm "$CONTAINER_NAME" >/dev/null 2>&1 || true
echo "[✓] Container removed (volume preserved)"

# ─── Start new container ───
echo "[*] Starting upgraded publisher..."

# Build docker run args preserving original config
DOCKER_ARGS=(
    -d
    --name "$CONTAINER_NAME"
    --restart unless-stopped
    --cap-add NET_ADMIN
    --cap-add SYS_MODULE
    -e "CONTROL_PLANE_URL=${CONTROL_PLANE_URL}"
    -e "AGENT_VERSION=${NEW_VERSION}"
    -v /etc/resolv.conf:/host/etc/resolv.conf:ro
    -v "${VOLUME_NAME}:/etc/wireguard"
    --network host
)

# Preserve optional env vars if they were set
[ -n "$PUB_NAME" ] && DOCKER_ARGS+=(-e "PUBLISHER_NAME=${PUB_NAME}")
[ -n "$PUB_HEARTBEAT_INTERVAL" ] && DOCKER_ARGS+=(-e "HEARTBEAT_INTERVAL=${PUB_HEARTBEAT_INTERVAL}")
[ -n "$PUB_WG_INTERFACE" ] && DOCKER_ARGS+=(-e "WG_INTERFACE=${PUB_WG_INTERFACE}")
[ -n "$PUB_API_KEY" ] && DOCKER_ARGS+=(-e "PUBLISHER_API_KEY=${PUB_API_KEY}")

docker run "${DOCKER_ARGS[@]}" "$IMAGE_NAME" >/dev/null

# ─── Verify ───
echo "[*] Waiting for publisher to start..."
sleep 10

if docker ps --filter "name=${CONTAINER_NAME}" --filter status=running -q | grep -q .; then

    # ─── Auto-acquire API key if publisher doesn't have one ───
    if [ -z "$PUB_API_KEY" ] && [ -n "$PUB_ID" ]; then
        echo "[*] No API key found — requesting one via upgrade-key..."
        UPGRADE_RESPONSE=$(curl -sf -X POST \
            "${CONTROL_PLANE_URL}/api/v1/publishers/${PUB_ID}/upgrade-key" 2>/dev/null || echo "")

        if [ -n "$UPGRADE_RESPONSE" ]; then
            NEW_KEY=$(echo "$UPGRADE_RESPONSE" | grep -o '"publisher_api_key":"[^"]*"' | cut -d'"' -f4)
            if [ -n "$NEW_KEY" ]; then
                # Inject key into enrollment state file inside the volume
                docker exec "$CONTAINER_NAME" sh -c "echo 'publisher_api_key=${NEW_KEY}' >> /etc/wireguard/.enrolled && chmod 600 /etc/wireguard/.enrolled"
                # Restart to pick up the key
                docker restart "$CONTAINER_NAME" >/dev/null 2>&1
                sleep 5
                PUB_API_KEY="$NEW_KEY"
                echo "[✓] API key obtained and injected (wpk_...${NEW_KEY: -6})"
            else
                echo "[!] upgrade-key returned unexpected response. Assign key manually via admin rotate-key."
                echo "    Response: $UPGRADE_RESPONSE"
            fi
        else
            # upgrade-key might fail if IP doesn't match or publisher already has key
            echo "[!] Could not auto-acquire API key (upgrade-key returned empty/error)."
            echo "    This is normal if the publisher's IP isn't registered yet."
            echo "    Ask an admin to run rotate-key for this publisher from the UI."
        fi
    elif [ -n "$PUB_API_KEY" ]; then
        echo "[✓] API key already configured"
    fi

    echo ""
    echo "═══════════════════════════════════════════════"
    echo "  [✓] Publisher upgraded successfully!"
    echo "═══════════════════════════════════════════════"
    echo ""
    echo "  Container:     $CONTAINER_NAME"
    echo "  Previous ver:  ${CURRENT_VERSION:-unknown}"
    echo "  New version:   $NEW_VERSION"
    echo "  Status:        $(docker inspect $CONTAINER_NAME --format '{{.State.Status}}')"
    echo ""
    echo "  View logs:  docker logs $CONTAINER_NAME"
    echo "  Verify:     docker logs $CONTAINER_NAME 2>&1 | grep -i 'heartbeat\|enrolled\|tunnel'"
    echo ""
else
    echo ""
    echo "  [!] Publisher may have failed to start. Checking logs..."
    echo ""
    docker logs "$CONTAINER_NAME" 2>&1 | tail -15
    echo ""
    echo "  The old container has been removed. To rollback:"
    echo "    docker run -d --name $CONTAINER_NAME --restart unless-stopped \\"
    echo "      --cap-add NET_ADMIN --cap-add SYS_MODULE \\"
    echo "      -e CONTROL_PLANE_URL='${CONTROL_PLANE_URL}' \\"
    echo "      -e AGENT_VERSION='${CURRENT_VERSION:-0.4.0}' \\"
    [ -n "$PUB_NAME" ] && echo "      -e PUBLISHER_NAME='${PUB_NAME}' \\"
    echo "      -v /etc/resolv.conf:/host/etc/resolv.conf:ro \\"
    echo "      -v ${VOLUME_NAME}:/etc/wireguard \\"
    echo "      --network host \\"
    echo "      wireztna-publisher:${CURRENT_VERSION:-0.4.0}"
    exit 1
fi

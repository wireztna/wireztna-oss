#!/bin/bash
# WireZTNA Publisher — Auto-installer
# Usage: curl -sf http://<broker>:8443/api/v1/publishers/install.sh | ENROLLMENT_TOKEN=<token> bash
#
# Or with all options:
#   curl -sf http://<broker>:8443/api/v1/publishers/install.sh | \
#     ENROLLMENT_TOKEN=<token> \
#     PUBLISHER_NAME=my-publisher \
#     CONTROL_PLANE_URL=http://<broker>:8443 \
#     bash

set -euo pipefail

# ─── Configuration ───
ENROLLMENT_TOKEN="${ENROLLMENT_TOKEN:?ERROR: ENROLLMENT_TOKEN is required. Set it before piping to bash.}"
CONTROL_PLANE_URL="${CONTROL_PLANE_URL:-__CONTROL_PLANE_URL__}"
PUBLISHER_NAME="${PUBLISHER_NAME:-$(hostname)}"
AGENT_VERSION="__AGENT_VERSION__"
IMAGE_NAME="wireztna-publisher:${AGENT_VERSION}"

echo "═══════════════════════════════════════════════"
echo "  WireZTNA Publisher — Auto-installer"
echo "═══════════════════════════════════════════════"
echo ""
echo "  Control Plane: $CONTROL_PLANE_URL"
echo "  Publisher:     $PUBLISHER_NAME"
echo "  Version:       $AGENT_VERSION"
echo ""

# ─── Detect OS ───
detect_os() {
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        echo "$ID"
    elif command -v lsb_release &>/dev/null; then
        lsb_release -si | tr '[:upper:]' '[:lower:]'
    else
        echo "unknown"
    fi
}

OS=$(detect_os)
echo "[*] Detected OS: $OS"

# ─── Install Docker if not present ───
if command -v docker &>/dev/null; then
    echo "[✓] Docker already installed: $(docker --version | head -1)"
else
    echo "[*] Installing Docker..."
    case "$OS" in
        amzn|amazon)
            yum install -y docker
            systemctl enable docker
            systemctl start docker
            ;;
        ubuntu|debian)
            apt-get update -qq
            apt-get install -y -qq docker.io
            systemctl enable docker
            systemctl start docker
            ;;
        centos|rhel|rocky|alma|fedora)
            if command -v dnf &>/dev/null; then
                dnf install -y docker
            else
                yum install -y docker
            fi
            systemctl enable docker
            systemctl start docker
            ;;
        *)
            echo "ERROR: Unsupported OS '$OS'. Install Docker manually and re-run."
            exit 1
            ;;
    esac
    echo "[✓] Docker installed"
fi

# ─── Enable IP forwarding ───
echo "[*] Enabling IP forwarding..."
sysctl -w net.ipv4.ip_forward=1 >/dev/null
echo "net.ipv4.ip_forward=1" > /etc/sysctl.d/99-wireztna.conf

# ─── Download and build publisher image ───
WORK_DIR=$(mktemp -d)
echo "[*] Preparing publisher image in $WORK_DIR..."

# Download scripts from the control plane
for script in entrypoint.sh enroll.sh heartbeat.sh; do
    curl -sf "${CONTROL_PLANE_URL}/api/v1/publishers/scripts/${script}" -o "${WORK_DIR}/${script}" || {
        echo "ERROR: Cannot download ${script} from ${CONTROL_PLANE_URL}"
        echo "       Make sure the control plane is reachable."
        rm -rf "$WORK_DIR"
        exit 1
    }
done
chmod +x "${WORK_DIR}"/*.sh

# Create Dockerfile
cat > "${WORK_DIR}/Dockerfile" << 'DOCKERFILE'
FROM alpine:3.19
RUN apk add --no-cache wireguard-tools iptables curl jq bash && echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf
ARG VERSION=__AGENT_VERSION__
RUN echo "$VERSION" > /etc/wireztna-version
COPY entrypoint.sh enroll.sh heartbeat.sh /opt/wireztna/
RUN chmod +x /opt/wireztna/*.sh
ENV CONFIG_DIR=/etc/wireguard
ENV WG_INTERFACE=wg-broker
ENV HEARTBEAT_INTERVAL=30
ENTRYPOINT ["/opt/wireztna/entrypoint.sh"]
DOCKERFILE

# Build
docker build -t "$IMAGE_NAME" "$WORK_DIR" -q
rm -rf "$WORK_DIR"
echo "[✓] Image built: $IMAGE_NAME"

# ─── Stop existing publisher if running ───
if docker ps -a --format '{{.Names}}' | grep -q '^wireztna-publisher$'; then
    echo "[*] Stopping existing publisher container..."
    docker rm -f wireztna-publisher >/dev/null 2>&1
fi

# ─── Run publisher ───
echo "[*] Starting publisher container..."
docker run -d \
    --name wireztna-publisher \
    --restart unless-stopped \
    --cap-add NET_ADMIN \
    --cap-add SYS_MODULE \
    -e ENROLLMENT_TOKEN="$ENROLLMENT_TOKEN" \
    -e CONTROL_PLANE_URL="$CONTROL_PLANE_URL" \
    -e PUBLISHER_NAME="$PUBLISHER_NAME" \
    -e AGENT_VERSION="$AGENT_VERSION" \
    -v /etc/resolv.conf:/host/etc/resolv.conf:ro \
    -v wireztna-config:/etc/wireguard \
    --network host \
    "$IMAGE_NAME" >/dev/null

# ─── Wait for enrollment ───
echo "[*] Waiting for enrollment to complete..."
sleep 15

# Check status
if docker ps --filter name=wireztna-publisher --filter status=running -q | grep -q .; then
    echo ""
    echo "═══════════════════════════════════════════════"
    echo "  [✓] Publisher deployed successfully!"
    echo "═══════════════════════════════════════════════"
    echo ""
    echo "  Container:  wireztna-publisher"
    echo "  Status:     $(docker inspect wireztna-publisher --format '{{.State.Status}}')"
    echo "  Name:       $PUBLISHER_NAME"
    echo ""
    echo "  View logs:  docker logs wireztna-publisher"
    echo "  Check UI:   ${CONTROL_PLANE_URL} → Publishers"
    echo ""
else
    echo ""
    echo "  [!] Publisher may have failed. Check logs:"
    echo "      docker logs wireztna-publisher"
    echo ""
    docker logs wireztna-publisher 2>&1 | tail -10
    exit 1
fi

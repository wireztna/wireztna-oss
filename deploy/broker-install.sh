#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════════
# WireZTNA Broker — Full Installation Script
# ═══════════════════════════════════════════════════════════════════════════════
#
# Deploys a complete WireZTNA broker for a new tenant.
# Creates: WireGuard overlay, API, reconciler, health monitor, DNS proxy, UI, nginx.
#
# Target OS: Amazon Linux 2023 / RHEL 9 (dnf) or Ubuntu 22.04+ / Debian 12+ (apt)
#
# Usage:
#   sudo ./broker-install.sh                      # interactive (prompts for tenant name)
#   sudo TENANT_NAME=acme ./broker-install.sh     # non-interactive
#
# DNS requirement (set BEFORE running this script):
#   {tenant}.wireztna.com     → A record → <this host IP> (Cloudflare proxied, SSL Flexible)
#   wg-{tenant}.wireztna.com  → A record → <this host IP> (DNS-only, grey cloud)
#
# After installation:
#   1. Configure SMTP in /opt/wireztna/.env (for OTP email login)
#   2. Create admin: curl -X POST http://127.0.0.1:8443/api/v1/auth/setup -H "Content-Type: application/json" -d '{"email":"admin@company.com","password":"changeme","username":"admin"}'
#   3. Open https://{tenant}.wireztna.com and login
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

# ─── Tenant Configuration ───
if [ -z "${TENANT_NAME:-}" ]; then
    echo ""
    echo "  WireZTNA — New Tenant Setup"
    echo "  ─────────────────────────────"
    echo ""
    echo "  Enter the tenant identifier (lowercase, no spaces)."
    echo "  This determines the public URLs:"
    echo "    UI:  https://{tenant}.wireztna.com"
    echo "    WG:  wg-{tenant}.wireztna.com:51820"
    echo ""
    read -p "  Tenant name: " TENANT_NAME
    echo ""
fi

# Validate tenant name
if ! echo "$TENANT_NAME" | grep -qE '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$'; then
    echo "ERROR: Tenant name must be lowercase alphanumeric (with optional hyphens)"
    exit 1
fi

# ─── Configuration ───
INSTALL_DIR="${INSTALL_DIR:-/opt/wireztna}"
BROKER_WG_PORT="${BROKER_WG_PORT:-51820}"
BROKER_OVERLAY_IP="${BROKER_OVERLAY_IP:-10.200.0.1}"
BROKER_OVERLAY_MASK="${BROKER_OVERLAY_MASK:-16}"
API_PORT="${API_PORT:-8443}"
UI_PORT="${UI_PORT:-3000}"
SECRET_KEY="${SECRET_KEY:-$(openssl rand -hex 32)}"
BROKER_API_KEY="${BROKER_API_KEY:-$(openssl rand -hex 16)}"
PUBLIC_IP="${PUBLIC_IP:-$(curl -sf http://checkip.amazonaws.com || echo "127.0.0.1")}"

# Derived from tenant name
UI_DOMAIN="${TENANT_NAME}.wireztna.com"
WG_DOMAIN="wg-${TENANT_NAME}.wireztna.com"

echo "═══════════════════════════════════════════════════════════════"
echo "  WireZTNA Broker — Installing tenant: $TENANT_NAME"
echo "═══════════════════════════════════════════════════════════════"
echo "  Install dir:        $INSTALL_DIR"
echo "  Public IP:          $PUBLIC_IP"
echo "  UI URL:             https://$UI_DOMAIN"
echo "  WG endpoint:        $WG_DOMAIN:$BROKER_WG_PORT"
echo "  Broker overlay IP:  $BROKER_OVERLAY_IP/$BROKER_OVERLAY_MASK"
echo "═══════════════════════════════════════════════════════════════"
echo ""
echo "  DNS records required (Cloudflare wireztna.com zone):"
echo "    $UI_DOMAIN   → A → $PUBLIC_IP (proxied, orange)"
echo "    $WG_DOMAIN   → A → $PUBLIC_IP (DNS-only, grey)"
echo ""
read -p "  Press Enter to continue (or Ctrl+C to abort)..."
echo ""

# ─── Detect package manager ───
detect_os() {
    if command -v dnf &>/dev/null; then
        echo "dnf"
    elif command -v apt-get &>/dev/null; then
        echo "apt"
    else
        echo "ERROR: Unsupported package manager. Need dnf or apt." >&2
        exit 1
    fi
}

PKG_MANAGER=$(detect_os)
echo "[*] Detected package manager: $PKG_MANAGER"

# ─── Install system packages ───
echo "[*] Installing system packages..."

if [ "$PKG_MANAGER" = "dnf" ]; then
    dnf install -y \
        wireguard-tools iproute nftables conntrack-tools nginx \
        python3.11 python3.11-pip \
        jq openssl socat \
        --allowerasing 2>&1 | tail -5

    if ! command -v node &>/dev/null; then
        curl -fsSL https://rpm.nodesource.com/setup_20.x | bash -
        dnf install -y nodejs 2>&1 | tail -3
    fi

    PYTHON_BIN="/usr/bin/python3.11"

elif [ "$PKG_MANAGER" = "apt" ]; then
    apt-get update -q
    apt-get install -y \
        wireguard-tools iproute2 nftables conntrack nginx \
        python3.11 python3.11-venv python3-pip \
        jq openssl socat curl

    if ! command -v node &>/dev/null; then
        curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
        apt-get install -y nodejs
    fi

    PYTHON_BIN="/usr/bin/python3.11"

    if ! command -v python3.11 &>/dev/null; then
        PYTHON_BIN=$(which python3)
        PY_MINOR=$($PYTHON_BIN --version | grep -oP '3\.(\d+)' | cut -d. -f2)
        if [ "${PY_MINOR:-0}" -lt 10 ]; then
            echo "ERROR: Python 3.10+ required"
            exit 1
        fi
    fi
fi

echo "[+] Python: $($PYTHON_BIN --version)"
echo "[+] Node: $(node --version)"
echo "[+] nginx: $(nginx -v 2>&1 | awk '{print $3}')"

# ─── Install Python dependencies ───
echo "[*] Installing Python dependencies..."
$PYTHON_BIN -m pip install --break-system-packages -q \
    -r "$INSTALL_DIR/control-plane/requirements.txt" 2>&1 | tail -3
echo "[+] Python packages installed"

# ─── Create directories ───
mkdir -p "$INSTALL_DIR/data"
mkdir -p "$INSTALL_DIR/downloads"
mkdir -p /etc/wireztna
chmod +x "$INSTALL_DIR"/broker/scripts/*.sh 2>/dev/null || true

# ─── Generate .env ───
ENV_FILE="$INSTALL_DIR/.env"
if [ ! -f "$ENV_FILE" ]; then
    echo "[*] Generating .env..."
    cat > "$ENV_FILE" << EOF
# WireZTNA Broker — Tenant: $TENANT_NAME
# Generated: $(date -Iseconds)

# Core
DATABASE_URL=sqlite+aiosqlite:///${INSTALL_DIR}/data/wireztna.db
SECRET_KEY=${SECRET_KEY}
JWT_ALGORITHM=HS256
JWT_EXPIRY_HOURS=24
SESSION_TTL_HOURS=8

# Broker identity
BROKER_ID=broker-01
BROKER_WG_PORT=${BROKER_WG_PORT}
BROKER_OVERLAY_IP=${BROKER_OVERLAY_IP}
BROKER_OVERLAY_NETWORK=10.200.0.0/16
BROKER_TUNNEL_NETWORK=10.100.0.0/16
BROKER_POLL_INTERVAL=10
BROKER_API_KEY=${BROKER_API_KEY}
CONTROL_PLANE_URL=http://127.0.0.1:${API_PORT}
SCRIPTS_DIR=${INSTALL_DIR}/broker/scripts

# Public endpoints (must match DNS)
BROKER_PUBLIC_ENDPOINT=${WG_DOMAIN}
UI_PUBLIC_DOMAIN=${UI_DOMAIN}

# Health monitor
HEALTH_CHECK_INTERVAL=15
HANDSHAKE_TIMEOUT=180

# SMTP (configure for OTP email login)
SMTP_HOST=
SMTP_PORT=587
SMTP_USER=
SMTP_PASSWORD=
SMTP_FROM=support@wireztna.com
SMTP_FROM_NAME=WireZTNA
SMTP_USE_TLS=true

# OTP
OTP_LENGTH=6
OTP_TTL_SECONDS=300
OTP_MAX_ATTEMPTS=3
EOF
    chmod 600 "$ENV_FILE"
    echo "[+] .env created (edit SMTP settings before first use)"
else
    echo "[+] .env already exists — preserving"
fi

# ─── WireGuard setup ───
echo "[*] Configuring WireGuard overlay..."
mkdir -p /etc/wireguard
chmod 700 /etc/wireguard

if [ ! -f /etc/wireguard/broker.key ]; then
    wg genkey | tee /etc/wireguard/broker.key | wg pubkey > /etc/wireguard/broker.pub
    chmod 600 /etc/wireguard/broker.key
    echo "[+] Broker keypair generated"
else
    wg pubkey < /etc/wireguard/broker.key > /etc/wireguard/broker.pub
    echo "[+] Existing broker keypair found"
fi

if ! ip link show wg-clients &>/dev/null; then
    ip link add dev wg-clients type wireguard
    wg set wg-clients listen-port "$BROKER_WG_PORT" private-key /etc/wireguard/broker.key
    ip addr add "${BROKER_OVERLAY_IP}/${BROKER_OVERLAY_MASK}" dev wg-clients
    ip link set wg-clients up
    echo "[+] wg-clients interface UP"
else
    echo "[+] wg-clients already exists"
fi

# IP forwarding + conntrack accounting
sysctl -w net.ipv4.ip_forward=1 > /dev/null
sysctl -w net.netfilter.nf_conntrack_acct=1 > /dev/null 2>&1 || true
cat > /etc/sysctl.d/99-wireztna.conf << EOF
net.ipv4.ip_forward=1
net.netfilter.nf_conntrack_acct=1
EOF

# ─── nginx ───
echo "[*] Configuring nginx..."
cat > /etc/nginx/conf.d/wireztna.conf << EOF
server {
    listen 80;
    server_name _;

    client_max_body_size 10m;

    # API
    location /api/ {
        proxy_pass http://127.0.0.1:${API_PORT};
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 30s;
    }

    # Health (no auth — for LB/Cloudflare health checks)
    location /health {
        proxy_pass http://127.0.0.1:${API_PORT};
    }

    # SvelteKit UI (everything else)
    location / {
        proxy_pass http://127.0.0.1:${UI_PORT};
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
EOF

# Remove default nginx site if it conflicts
rm -f /etc/nginx/conf.d/default.conf 2>/dev/null || true
rm -f /etc/nginx/sites-enabled/default 2>/dev/null || true

nginx -t 2>&1 && echo "[+] nginx config valid" || { echo "ERROR: nginx config invalid"; exit 1; }

# ─── Systemd services ───
echo "[*] Creating systemd services..."

cat > /etc/systemd/system/wireztna-wg.service << EOF
[Unit]
Description=WireZTNA WireGuard Overlay Interface
After=network.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/bash -c 'ip link show wg-clients 2>/dev/null || (ip link add dev wg-clients type wireguard && wg set wg-clients listen-port ${BROKER_WG_PORT} private-key /etc/wireguard/broker.key && ip addr add ${BROKER_OVERLAY_IP}/${BROKER_OVERLAY_MASK} dev wg-clients && ip link set wg-clients up && sysctl -w net.ipv4.ip_forward=1)'
ExecStop=/bin/bash -c 'ip link del wg-clients 2>/dev/null || true'

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/wireztna-api.service << EOF
[Unit]
Description=WireZTNA Control Plane API
After=network.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}/control-plane
EnvironmentFile=${INSTALL_DIR}/.env
ExecStart=${PYTHON_BIN} -m uvicorn app.main:app --host 127.0.0.1 --port ${API_PORT}
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/wireztna-reconciler.service << EOF
[Unit]
Description=WireZTNA Broker Reconciler
After=wireztna-api.service wireztna-wg.service
Wants=wireztna-api.service

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=${INSTALL_DIR}/.env
ExecStart=${PYTHON_BIN} -m broker.agent.reconciler
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/wireztna-health.service << EOF
[Unit]
Description=WireZTNA Health Monitor
After=wireztna-api.service wireztna-wg.service
Wants=wireztna-api.service

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=${INSTALL_DIR}/.env
ExecStart=${PYTHON_BIN} -m broker.agent.health_monitor
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/wireztna-dns.service << EOF
[Unit]
Description=WireZTNA DNS Proxy
After=wireztna-api.service wireztna-wg.service
Wants=wireztna-api.service

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=${INSTALL_DIR}/.env
ExecStart=${PYTHON_BIN} -m broker.agent.dns_proxy
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/wireztna-ui.service << EOF
[Unit]
Description=WireZTNA Web UI
After=wireztna-api.service

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}/web-ui
Environment=PORT=${UI_PORT}
Environment=HOST=127.0.0.1
Environment=PROTOCOL_HEADER=X-Forwarded-Proto
Environment=HOST_HEADER=X-Forwarded-Host
ExecStart=/usr/bin/node build
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

echo "[+] Systemd services created"

# ─── Enable and start ───
echo "[*] Starting services..."
systemctl daemon-reload
systemctl enable wireztna-wg wireztna-api wireztna-reconciler wireztna-health wireztna-dns wireztna-ui nginx

systemctl start wireztna-wg
systemctl start wireztna-api
sleep 3

if curl -sf http://127.0.0.1:${API_PORT}/health > /dev/null; then
    echo "[+] API healthy"
else
    echo "[!] WARNING: API not responding — check: journalctl -u wireztna-api -n 30"
fi

systemctl start wireztna-reconciler wireztna-health wireztna-dns wireztna-ui
systemctl restart nginx

sleep 2

# ─── Final output ───
echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "  WireZTNA Broker — Installation Complete"
echo "  Tenant: $TENANT_NAME"
echo "═══════════════════════════════════════════════════════════════"
echo ""
echo "  Services:"
printf "    wireztna-api:        %s\n" "$(systemctl is-active wireztna-api)"
printf "    wireztna-ui:         %s\n" "$(systemctl is-active wireztna-ui)"
printf "    wireztna-reconciler: %s\n" "$(systemctl is-active wireztna-reconciler)"
printf "    wireztna-health:     %s\n" "$(systemctl is-active wireztna-health)"
printf "    wireztna-dns:        %s\n" "$(systemctl is-active wireztna-dns)"
printf "    wireztna-wg:         %s\n" "$(systemctl is-active wireztna-wg)"
printf "    nginx:               %s\n" "$(systemctl is-active nginx)"
echo ""
echo "  URLs:"
echo "    Admin UI:     https://$UI_DOMAIN"
echo "    API:          https://$UI_DOMAIN/api/v1/"
echo "    WG endpoint:  $WG_DOMAIN:$BROKER_WG_PORT (UDP)"
echo ""
echo "  Broker Public Key: $(cat /etc/wireguard/broker.pub)"
echo ""
echo "  NEXT STEPS:"
echo "    1. Edit /opt/wireztna/.env → add SMTP credentials (Mailjet)"
echo "       Then: systemctl restart wireztna-api"
echo ""
echo "    2. Create admin user:"
echo "       curl -X POST http://127.0.0.1:8443/api/v1/auth/setup \\"
echo "         -H 'Content-Type: application/json' \\"
echo "         -d '{\"email\":\"admin@company.com\",\"password\":\"changeme\",\"username\":\"admin\"}'"
echo ""
echo "    3. Open https://$UI_DOMAIN and login"
echo ""
echo "    4. Deploy client binaries to /opt/wireztna/downloads/"
echo ""
echo "═══════════════════════════════════════════════════════════════"

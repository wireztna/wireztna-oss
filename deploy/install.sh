#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════════
# WireZTNA Community Edition — Self-contained Broker Installer
# ═══════════════════════════════════════════════════════════════════════════════
#
# Deploys a complete WireZTNA broker on a single Linux host with a PUBLIC IP and
# NO domain, served over plain HTTP on port 80. No cloud provider or managed DNS.
#
# Creates: WireGuard overlay, API, reconciler, health monitor, DNS proxy, UI, nginx.
#
# Target OS: Amazon Linux 2023 / RHEL 9 (dnf) or Ubuntu 22.04+ / Debian 12+ (apt)
#
# The broker host builds NOTHING. This installer consumes a PRE-BUILT UI
# (web-ui/build/) and prebuilt client/publisher/wzctl binaries assembled into the
# release tree. Build those on a dev box first — see docs/install.md.
#
# Usage:
#   sudo ./install.sh                         # auto-detect the public IP
#   sudo ./install.sh --ip 203.0.113.10       # explicit public IP (no-domain mode)
#   sudo PUBLIC_IP=203.0.113.10 ./install.sh  # same, via env
#
# After installation:
#   1. (optional) Configure SMTP in /opt/wireztna/.env to enable email OTP login.
#   2. Create the first admin (the installer can prompt you), or:
#      curl -X POST http://127.0.0.1:8443/api/v1/auth/setup -H 'Content-Type: application/json' \
#        -d '{"email":"admin@example.com","password":"<strong-password>","username":"admin"}'
#   3. Open http://<PUBLIC_IP>/ and log in with username + password.
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

# ─── Parse arguments ───
PUBLIC_IP="${PUBLIC_IP:-}"
while [ $# -gt 0 ]; do
    case "$1" in
        --ip)
            PUBLIC_IP="${2:-}"
            shift 2
            ;;
        --ip=*)
            PUBLIC_IP="${1#*=}"
            shift
            ;;
        -h|--help)
            echo "Usage: sudo ./install.sh [--ip <PUBLIC_IP>]"
            echo ""
            echo "  --ip <PUBLIC_IP>   Public IP for the broker endpoint (no-domain mode)."
            echo "                     If omitted, the installer auto-detects it."
            exit 0
            ;;
        *)
            echo "ERROR: Unknown argument: $1" >&2
            echo "Usage: sudo ./install.sh [--ip <PUBLIC_IP>]" >&2
            exit 1
            ;;
    esac
done

# ─── Configuration ───
INSTALL_DIR="${INSTALL_DIR:-/opt/wireztna}"
BROKER_WG_PORT="${BROKER_WG_PORT:-51820}"
BROKER_OVERLAY_IP="${BROKER_OVERLAY_IP:-10.200.0.1}"
BROKER_OVERLAY_MASK="${BROKER_OVERLAY_MASK:-16}"
API_PORT="${API_PORT:-8443}"
UI_PORT="${UI_PORT:-3000}"
SECRET_KEY="${SECRET_KEY:-$(openssl rand -hex 32)}"
BROKER_API_KEY="${BROKER_API_KEY:-$(openssl rand -hex 16)}"

# Auto-detect the public IP when not provided.
if [ -z "$PUBLIC_IP" ]; then
    PUBLIC_IP="$(curl -sf http://checkip.amazonaws.com || echo "")"
fi
if [ -z "$PUBLIC_IP" ]; then
    echo "ERROR: Could not determine the public IP. Re-run with: sudo ./install.sh --ip <PUBLIC_IP>" >&2
    exit 1
fi

# No-domain mode: HTTP, bare IP endpoint, empty UI domain.
BROKER_PUBLIC_ENDPOINT="$PUBLIC_IP"
BROKER_PUBLIC_SCHEME="http"
UI_PUBLIC_DOMAIN=""

echo "═══════════════════════════════════════════════════════════════"
echo "  WireZTNA Community Edition — Broker Install (bare IP, no domain)"
echo "═══════════════════════════════════════════════════════════════"
echo "  Install dir:        $INSTALL_DIR"
echo "  Public IP:          $PUBLIC_IP"
echo "  Admin UI:           http://$PUBLIC_IP/"
echo "  WG endpoint:        $PUBLIC_IP:$BROKER_WG_PORT (UDP)"
echo "  Broker overlay IP:  $BROKER_OVERLAY_IP/$BROKER_OVERLAY_MASK"
echo "═══════════════════════════════════════════════════════════════"
echo ""
echo "  Open these inbound ports on the host firewall / security group:"
echo "    80/TCP      API + Admin UI (plain HTTP)"
echo "    $BROKER_WG_PORT/UDP   WireGuard clients"
echo "    51821+/UDP  Publisher tunnels (one per namespace)"
echo ""

# ─── Verify pre-built artifacts (this host builds NOTHING) ───
if [ ! -d "$INSTALL_DIR/web-ui/build" ]; then
    echo "ERROR: Missing pre-built UI at $INSTALL_DIR/web-ui/build" >&2
    echo "       Build the UI on a dev box (npm ci && npm run build) and assemble the" >&2
    echo "       release tree before running this installer. See docs/install.md." >&2
    exit 1
fi

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

# ─── Install Python dependencies (into an isolated virtualenv) ───
# A dedicated venv avoids PEP 668 "externally-managed-environment" errors
# (Ubuntu/Debian pip refuses system-wide installs) and never pollutes the
# system Python. The systemd units below use ${PYTHON_BIN}, so we repoint it
# at the venv interpreter after creating it.
VENV_DIR="$INSTALL_DIR/.venv"
echo "[*] Creating Python virtualenv at $VENV_DIR..."
"$PYTHON_BIN" -m venv "$VENV_DIR"
PYTHON_BIN="$VENV_DIR/bin/python"

echo "[*] Installing Python dependencies..."
"$PYTHON_BIN" -m pip install --upgrade -q pip
"$PYTHON_BIN" -m pip install -q \
    -r "$INSTALL_DIR/control-plane/requirements.txt" 2>&1 | tail -3
echo "[+] Python packages installed (venv: $VENV_DIR)"

# ─── Create directories (downloads is ADDITIVE — never rm -rf) ───
mkdir -p "$INSTALL_DIR/data"
mkdir -p "$INSTALL_DIR/downloads"
mkdir -p /etc/wireztna
chmod +x "$INSTALL_DIR"/broker/scripts/*.sh 2>/dev/null || true

# ─── Generate .env ───
ENV_FILE="$INSTALL_DIR/.env"
if [ ! -f "$ENV_FILE" ]; then
    echo "[*] Generating .env..."
    cat > "$ENV_FILE" << EOF
# WireZTNA Community Edition — Broker
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

# Public endpoint / scheme (bare IP, HTTP — no domain)
BROKER_PUBLIC_ENDPOINT=${BROKER_PUBLIC_ENDPOINT}
BROKER_PUBLIC_SCHEME=${BROKER_PUBLIC_SCHEME}
PUBLIC_BASE_URL=
UI_PUBLIC_DOMAIN=${UI_PUBLIC_DOMAIN}
CORS_EXTRA_ORIGINS=

# Authentication (CE default: username + password; email OTP needs SMTP below)
AUTH_PROVIDER=local

# Health monitor
HEALTH_CHECK_INTERVAL=15
HANDSHAKE_TIMEOUT=180

# SMTP (OPTIONAL — leave empty to disable email OTP and onboarding emails)
SMTP_HOST=
SMTP_PORT=587
SMTP_USER=
SMTP_PASSWORD=
SMTP_FROM=
SMTP_FROM_NAME=WireZTNA
SMTP_USE_TLS=true

# OTP (used only when SMTP is configured)
OTP_LENGTH=6
OTP_TTL_SECONDS=300
OTP_MAX_ATTEMPTS=3

# Access-pass limits (CE default: unlimited on self-host)
PLAN_LIMITS_ENABLED=false
EOF
    chmod 600 "$ENV_FILE"
    echo "[+] .env created (configure SMTP later to enable email OTP)"
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

    # Health (no auth — for LB/uptime checks)
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

# ─── Optional: create the first admin ───
if [ -t 0 ]; then
    echo ""
    echo "  Create the first admin now? (leave email blank to skip)"
    read -r -p "    Admin email: " ADMIN_EMAIL
    if [ -n "$ADMIN_EMAIL" ]; then
        read -r -p "    Admin username [admin]: " ADMIN_USERNAME
        ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"
        read -r -s -p "    Admin password: " ADMIN_PASSWORD
        echo ""
        if [ -n "$ADMIN_PASSWORD" ]; then
            SETUP_PAYLOAD=$(jq -n \
                --arg email "$ADMIN_EMAIL" \
                --arg username "$ADMIN_USERNAME" \
                --arg password "$ADMIN_PASSWORD" \
                '{email: $email, username: $username, password: $password}')
            if curl -sf -X POST "http://127.0.0.1:${API_PORT}/api/v1/auth/setup" \
                -H 'Content-Type: application/json' \
                -d "$SETUP_PAYLOAD" > /dev/null; then
                echo "    [+] Admin created: $ADMIN_EMAIL"
            else
                echo "    [!] Admin creation failed (it may already exist). Create it later via /api/v1/auth/setup."
            fi
        else
            echo "    [!] Empty password — skipping admin creation."
        fi
    fi
fi

# ─── Final output ───
echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "  WireZTNA Community Edition — Installation Complete"
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
echo "    Admin UI:     http://$PUBLIC_IP/"
echo "    API:          http://$PUBLIC_IP/api/v1/"
echo "    WG endpoint:  $PUBLIC_IP:$BROKER_WG_PORT (UDP)"
echo ""
echo "  Broker Public Key: $(cat /etc/wireguard/broker.pub)"
echo ""
echo "  NEXT STEPS:"
echo "    1. (optional) Edit /opt/wireztna/.env → add SMTP credentials to enable"
echo "       email OTP login. Then: systemctl restart wireztna-api"
echo ""
echo "    2. If you skipped it above, create the first admin:"
echo "       curl -X POST http://127.0.0.1:${API_PORT}/api/v1/auth/setup \\"
echo "         -H 'Content-Type: application/json' \\"
echo "         -d '{\"email\":\"admin@example.com\",\"password\":\"changeme\",\"username\":\"admin\"}'"
echo ""
echo "    3. Open http://$PUBLIC_IP/ and log in with username + password."
echo ""
echo "    4. Place prebuilt client binaries in /opt/wireztna/downloads/ (additive)."
echo ""
echo "═══════════════════════════════════════════════════════════════"

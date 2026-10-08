#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════════
# broker-update.sh — Deploy code updates to a running WireZTNA broker
# ═══════════════════════════════════════════════════════════════════════════════
#
# Usage:
#   sudo ./broker-update.sh                         # Just rebuild UI + restart services
#   sudo ./broker-update.sh /path/to/update.tar.gz  # Extract tarball, rebuild, restart
#   sudo ./broker-update.sh --pull                  # Git pull, rebuild, restart
#
# This script:
#   1. Backs up the database
#   2. Applies the code update (if specified)
#   3. Installs any new Python/Node dependencies
#   4. Rebuilds the Web UI with correct VITE_API_URL
#   5. Restarts all systemd services in dependency order
#   6. Verifies the health endpoint responds
#
# IMPORTANT: The web UI MUST be built with VITE_API_URL set to the broker's
# public IP. Without this, the UI will hardcode localhost and be unreachable
# from remote browsers. This script handles it automatically.
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

# ─── Configuration ───
INSTALL_DIR="${INSTALL_DIR:-/opt/wireztna}"
ENV_FILE="${INSTALL_DIR}/.env"
BACKUP_DIR="${INSTALL_DIR}/backups"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

# ─── Colors for output ───
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log()  { echo -e "${GREEN}[✓]${NC} $*"; }
warn() { echo -e "${YELLOW}[!]${NC} $*"; }
err()  { echo -e "${RED}[✗]${NC} $*" >&2; }
info() { echo -e "[*] $*"; }

# ─── Pre-flight checks ───
if [ "$(id -u)" -ne 0 ]; then
    err "This script must be run as root (use sudo)"
    exit 1
fi

if [ ! -d "$INSTALL_DIR" ]; then
    err "Install directory not found: $INSTALL_DIR"
    exit 1
fi

# ─── Detect PUBLIC_IP ───
# Priority: .env file → AWS instance metadata → checkip service
detect_public_ip() {
    # 1. Try .env file
    if [ -f "$ENV_FILE" ]; then
        local env_ip
        env_ip=$(grep -oP '(?<=PUBLIC_IP=).+' "$ENV_FILE" 2>/dev/null || true)
        if [ -n "$env_ip" ]; then
            echo "$env_ip"
            return
        fi
    fi

    # 2. Try AWS instance metadata (IMDSv2)
    local token
    token=$(curl -sf -X PUT "http://169.254.169.254/latest/api/token" \
        -H "X-aws-ec2-metadata-token-ttl-seconds: 5" --connect-timeout 2 2>/dev/null || true)
    if [ -n "$token" ]; then
        local meta_ip
        meta_ip=$(curl -sf -H "X-aws-ec2-metadata-token: $token" \
            "http://169.254.169.254/latest/meta-data/public-ipv4" --connect-timeout 2 2>/dev/null || true)
        if [ -n "$meta_ip" ]; then
            echo "$meta_ip"
            return
        fi
    fi

    # 3. Fallback to checkip services
    local check_ip
    check_ip=$(curl -sf http://checkip.amazonaws.com --connect-timeout 5 2>/dev/null || true)
    if [ -n "$check_ip" ]; then
        echo "$check_ip"
        return
    fi

    # Last resort
    err "Could not detect public IP. Set PUBLIC_IP in $ENV_FILE or pass as env var."
    exit 1
}

# ─── Detect API_PORT from .env or default ───
detect_api_port() {
    if [ -f "$ENV_FILE" ]; then
        local port
        port=$(grep -oP '(?<=CONTROL_PLANE_PORT=).+' "$ENV_FILE" 2>/dev/null || true)
        if [ -n "$port" ]; then
            echo "$port"
            return
        fi
        # Also check API_PORT directly
        port=$(grep -oP '(?<=API_PORT=).+' "$ENV_FILE" 2>/dev/null || true)
        if [ -n "$port" ]; then
            echo "$port"
            return
        fi
    fi
    echo "8443"
}

PUBLIC_IP=$(detect_public_ip)
API_PORT=$(detect_api_port)

echo "═══════════════════════════════════════════════════════════════"
echo "  WireZTNA Broker — Update Deployment"
echo "═══════════════════════════════════════════════════════════════"
echo "  Install dir:   $INSTALL_DIR"
echo "  Public IP:     $PUBLIC_IP"
echo "  API port:      $API_PORT"
echo "  VITE_API_URL:  http://${PUBLIC_IP}:${API_PORT}"
echo "  Timestamp:     $TIMESTAMP"
echo "═══════════════════════════════════════════════════════════════"
echo ""

# ─── Step 1: Backup database ───
info "Backing up database..."
mkdir -p "$BACKUP_DIR"

# Find database file from .env
DB_PATH=""
if [ -f "$ENV_FILE" ]; then
    DB_PATH=$(grep 'DATABASE_URL' "$ENV_FILE" | grep -oP '(?<=sqlite.*:///)\S+' 2>/dev/null || true)
fi
# Default location
DB_PATH="${DB_PATH:-${INSTALL_DIR}/data/wireztna.db}"

if [ -f "$DB_PATH" ]; then
    cp "$DB_PATH" "${BACKUP_DIR}/wireztna-${TIMESTAMP}.db"
    log "Database backed up to ${BACKUP_DIR}/wireztna-${TIMESTAMP}.db"
else
    warn "Database not found at $DB_PATH — skipping backup"
fi

# ─── Step 2: Apply code update (if specified) ───
UPDATE_ARG="${1:-}"

if [ -n "$UPDATE_ARG" ]; then
    if [ "$UPDATE_ARG" = "--pull" ] || [ "$UPDATE_ARG" = "-p" ]; then
        info "Pulling latest code from git..."
        cd "$INSTALL_DIR"
        git pull --ff-only
        log "Git pull complete"

    elif [ -f "$UPDATE_ARG" ]; then
        info "Extracting update from: $UPDATE_ARG"
        # Extract tarball over the install directory
        tar -xzf "$UPDATE_ARG" -C "$INSTALL_DIR" --strip-components=0
        log "Tarball extracted to $INSTALL_DIR"

    else
        err "Argument is not a valid file or recognized flag: $UPDATE_ARG"
        echo "Usage: $0 [/path/to/update.tar.gz | --pull]"
        exit 1
    fi
fi

# ─── Step 3: Install/update dependencies ───
info "Checking Python dependencies..."
if [ -f "$INSTALL_DIR/control-plane/requirements.txt" ]; then
    # Detect python binary
    PYTHON_BIN=$(command -v python3.11 || command -v python3)
    $PYTHON_BIN -m pip install --break-system-packages -q \
        -r "$INSTALL_DIR/control-plane/requirements.txt" 2>&1 | tail -3
    log "Python dependencies up to date"
fi

info "Checking Node dependencies..."
cd "$INSTALL_DIR/web-ui"
if [ -f "package.json" ]; then
    npm install --legacy-peer-deps --silent 2>&1 | tail -3
    log "Node dependencies up to date"
fi

# ─── Step 4: Rebuild Web UI with correct VITE_API_URL ───
# CRITICAL: Without VITE_API_URL, the UI builds with localhost and remote
# browsers cannot reach the API.
info "Building Web UI with VITE_API_URL=http://${PUBLIC_IP}:${API_PORT}..."
cd "$INSTALL_DIR/web-ui"

# Write .env for the build (Vite reads this automatically)
cat > .env << EOF
# Auto-generated by broker-update.sh on $(date -Iseconds)
# This ensures the web UI connects to the correct API endpoint
VITE_API_URL=http://${PUBLIC_IP}:${API_PORT}
EOF

# Build with the env var also set explicitly (belt and suspenders)
VITE_API_URL="http://${PUBLIC_IP}:${API_PORT}" npm run build 2>&1 | tail -5
log "Web UI built successfully"

# ─── Step 5: Restart services in dependency order ───
info "Restarting services..."

# Stop in reverse order (dependents first)
systemctl stop wireztna-ui wireztna-health wireztna-reconciler wireztna-api 2>/dev/null || true
log "Services stopped"

# Reload systemd in case any service files changed
systemctl daemon-reload

# Start in dependency order
systemctl start wireztna-api
info "Waiting for API to be ready..."
sleep 3

# Start remaining services
systemctl start wireztna-reconciler
systemctl start wireztna-health
systemctl start wireztna-ui
log "Services started"

# ─── Step 6: Health check ───
info "Verifying health endpoint..."
HEALTH_OK=false
for i in $(seq 1 10); do
    if curl -sf "http://127.0.0.1:${API_PORT}/health" > /dev/null 2>&1; then
        HEALTH_OK=true
        break
    fi
    sleep 1
done

echo ""
echo "═══════════════════════════════════════════════════════════════"
if [ "$HEALTH_OK" = true ]; then
    echo -e "  ${GREEN}Update Complete — All services healthy${NC}"
else
    echo -e "  ${YELLOW}Update Complete — Health check failed (API may still be starting)${NC}"
    echo "  Check: journalctl -u wireztna-api -n 50"
fi
echo "═══════════════════════════════════════════════════════════════"
echo ""
echo "  Services:"
printf "    wireztna-api:        %s\n" "$(systemctl is-active wireztna-api 2>/dev/null || echo 'unknown')"
printf "    wireztna-ui:         %s\n" "$(systemctl is-active wireztna-ui 2>/dev/null || echo 'unknown')"
printf "    wireztna-reconciler: %s\n" "$(systemctl is-active wireztna-reconciler 2>/dev/null || echo 'unknown')"
printf "    wireztna-health:     %s\n" "$(systemctl is-active wireztna-health 2>/dev/null || echo 'unknown')"
echo ""
echo "  Web UI: http://${PUBLIC_IP}:3000"
echo "  API:    http://${PUBLIC_IP}:${API_PORT}"
echo ""
echo "  DB backup: ${BACKUP_DIR}/wireztna-${TIMESTAMP}.db"
echo ""
echo "═══════════════════════════════════════════════════════════════"

# WireZTNA Publisher — Deployment Guide

## Publisher Versions Overview

There are **two publisher implementations**, each with its own versioning:

| Implementation | Current Version | Stack | Deploy method | Status |
|---------------|----------------|-------|---------------|--------|
| **Docker publisher** | **0.4.1** | Alpine container + Bash scripts | `docker run` or `install-publisher.sh` | Deployed on SODZTNALXPR01 + publisher-scloud47 |
| **Go binary publisher** | **0.5.0** | Static Go binary (~8MB, zero dependencies) | `wireztna-publisher install` (systemd) | Deployed on publisher-azure-go |

### What gets downloaded today

- The **auto-installer** (`curl .../install.sh | bash`) deploys the **Docker publisher v0.4.1**.
- The **upgrade script** (`curl .../upgrade.sh | bash`) upgrades an existing Docker publisher in-place (preserves enrollment).
- The **Go binary** (v0.5.0) has no automated download endpoint. It is built locally and deployed manually.
- The control plane `downloads.py` router only serves **client** binaries, not publisher binaries.

### Version reporting

Both publishers report their version to the control plane via heartbeats:
- Docker publisher: reads `AGENT_VERSION` environment variable (must be set manually)
- Go publisher: reads version from build-time ldflags injection (`pkg/version.Version`)

The version is visible in the admin UI on the Publishers page.

---

## Option A: Docker Publisher (v0.4.0)

The original publisher implementation. Runs as a Docker container with Alpine, wireguard-tools, and iptables.

### Prerequisites

- Docker installed on the target machine
- The machine can reach the broker at `203.0.113.10:8443` (TCP) and `:51821` (UDP)
- The machine has access to the private network you want to expose (e.g., VPC)
- An enrollment token generated from the admin UI

### Step 1: Generate Enrollment Token (Admin)

In the Web UI:
1. **Publishers** → **Create Enrollment Token**
2. Set a name (e.g., "publisher-prod-vpc") and optionally pre-configure exposed CIDRs
3. Copy the token value

### Step 2: Build the Publisher Image

On your dev machine (or use a registry):

```bash
cd publisher/
docker build -t wireztna-publisher:0.4.0 .
```

Or pull from your registry if published.

### Step 3: Deploy on Target Machine

```bash
docker run -d \
  --name wireztna-publisher \
  --restart unless-stopped \
  --cap-add NET_ADMIN \
  --cap-add SYS_MODULE \
  --sysctl net.ipv4.ip_forward=1 \
  -e ENROLLMENT_TOKEN="<your-enrollment-token>" \
  -e CONTROL_PLANE_URL="http://203.0.113.10:8443" \
  -e PUBLISHER_NAME="publisher-prod-vpc" \
  -e AGENT_VERSION="0.4.0" \
  -e HEARTBEAT_INTERVAL=30 \
  -v /etc/resolv.conf:/host/etc/resolv.conf:ro \
  -v wireztna-config:/etc/wireguard \
  --network host \
  wireztna-publisher:0.4.0
```

### Required environment variables

| Variable | Description |
|----------|-------------|
| `ENROLLMENT_TOKEN` | One-time token from the admin UI |
| `CONTROL_PLANE_URL` | Broker API URL (e.g., `http://203.0.113.10:8443`) |

### Optional environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PUBLISHER_NAME` | hostname | Friendly name shown in the UI |
| `AGENT_VERSION` | — | Version string reported in heartbeats (shown in UI) |
| `HEARTBEAT_INTERVAL` | 30 | Seconds between heartbeat reports |
| `PUBLISHER_WG_PORT` | 51821 | Local WireGuard listen port |
| `LOCAL_DNS` | auto-detect | Internal DNS server IP to expose for split DNS |
| `WG_INTERFACE` | wg-broker | WireGuard interface name |

### Important flags

- `--cap-add NET_ADMIN` — required to create WireGuard interfaces
- `--cap-add SYS_MODULE` — required for WG kernel module (or use `wireguard-go`)
- `--network host` — required so the publisher can route traffic to the local VPC
- `-v wireztna-config:/etc/wireguard` — persists enrollment state across container restarts
- `-v /etc/resolv.conf:/host/etc/resolv.conf:ro` — auto-detects VPC DNS resolver

### Step 4: Verify

```bash
# Check container logs
docker logs wireztna-publisher

# Expected output:
# [✓] Enrollment successful
# [✓] Tunnel started
# [*] Starting heartbeat reporter...
# Publisher running. Waiting for traffic...

# Verify WireGuard tunnel is active
docker exec wireztna-publisher wg show

# Check in the admin UI:
# Publishers page → your publisher should show "online"
```

### Step 5: Configure in Admin UI

After enrollment, go to the admin UI:
1. **Publishers** → your new publisher appears with status "online"
2. Edit to set **exposed_cidrs** (e.g., `10.50.0.0/16`)
3. Optionally set **dns_zones** (e.g., `compute.internal`) and verify **dns_server** was auto-detected
4. **Groups** → assign the publisher to a group

---

## Option B: Go Binary Publisher (v0.5.0)

A standalone static binary with no Docker dependency. Uses wgctrl + netlink for zero-dependency WireGuard management. Installs as a systemd service.

### Prerequisites

- Linux host (amd64 or arm64)
- Root access (or sudo)
- systemd (optional — can run manually without it)
- The machine can reach the broker at port 8443 (TCP) and the assigned WG port (UDP)
- An enrollment token generated from the admin UI

### Step 1: Build the Binary

From a development machine:

```bash
cd publisher/wireztna-publisher-go/

# Build for linux/amd64
make build-linux

# Or for both amd64 and arm64
make build-all
```

The binary is created at `bin/wireztna-publisher-linux-amd64` (or `arm64`).

### Step 2: Copy to Target Machine

```bash
scp bin/wireztna-publisher-linux-amd64 user@target:/tmp/wireztna-publisher
ssh user@target "sudo mv /tmp/wireztna-publisher /usr/local/bin/ && sudo chmod +x /usr/local/bin/wireztna-publisher"
```

### Step 3: Install and Enroll

```bash
sudo wireztna-publisher install \
  --token "http://<broker>:8443/api/v1/publishers/enroll?token=<enrollment-token>" \
  --name "publisher-azure-go"
```

This single command:
1. Generates a WireGuard keypair
2. Enrolls with the control plane
3. Creates the WireGuard tunnel
4. Configures firewall rules (iptables masquerade + IP forwarding)
5. Installs and starts a systemd service

#### Install flags

| Flag | Default | Description |
|------|---------|-------------|
| `--token` | (required) | Enrollment URL with token |
| `--name` | hostname | Publisher name shown in the UI |
| `--port` | 51821 | WireGuard listen port |
| `--heartbeat-interval` | 30 | Seconds between heartbeats |
| `--dns` | auto-detect | Local DNS server IP |
| `--no-service` | false | Skip systemd service installation |

### Step 4: Verify

```bash
# Check service status
sudo wireztna-publisher status

# Expected output:
# WireZTNA Publisher v0.5.0
#   Publisher ID:  <uuid>
#   Status:        running
#   Tunnel IP:     10.100.X.2
#   Broker:        203.0.113.10:51827
#   ...

# Run diagnostics
sudo wireztna-publisher diagnose

# Check service logs
journalctl -u wireztna-publisher -f
```

### Step 5: Configure in Admin UI

Same as the Docker publisher — set exposed_cidrs, dns_zones, and assign to a group.

### Managing the Go publisher

```bash
# View status
sudo wireztna-publisher status

# Run diagnostics
sudo wireztna-publisher diagnose

# Stop the service
sudo systemctl stop wireztna-publisher

# Uninstall completely (removes service, config, and tunnel)
sudo wireztna-publisher uninstall
```

### Updating the Go publisher

```bash
# Build the new binary
cd publisher/wireztna-publisher-go/
make build-linux

# Copy to target and restart
scp bin/wireztna-publisher-linux-amd64 user@target:/tmp/wireztna-publisher
ssh user@target "sudo systemctl stop wireztna-publisher && sudo mv /tmp/wireztna-publisher /usr/local/bin/ && sudo systemctl start wireztna-publisher"
```

The enrollment state is preserved in `/etc/wireztna-publisher/` — no re-enrollment needed.

---

## Choosing Between Docker and Go Publisher

| Criteria | Docker (v0.4.0) | Go Binary (v0.5.0) |
|----------|-----------------|---------------------|
| **Dependencies** | Docker required | None (static binary) |
| **Binary size** | Alpine image (~50MB) | ~8MB |
| **Install method** | `docker run` | `wireztna-publisher install` |
| **Service management** | Docker restart policy | systemd native |
| **Auto-recovery** | Container restart on failure | Built-in watchdog (stale handshake detection) |
| **Version reporting** | Manual (`AGENT_VERSION` env var) | Automatic (compiled into binary) |
| **CLI tools** | None (scripts only) | `status`, `diagnose`, `uninstall` |
| **Exit node support** | Basic (iptables in scripts) | Full (masquerade for exit node traffic) |
| **Auto-installer** | Yes (`install-publisher.sh`) | No (manual binary deploy) |
| **Current deployments** | AWS publishers | Azure publisher |

The Go publisher is the recommended choice for new deployments. The Docker publisher continues to work and is fully supported.

---

## Troubleshooting

| Problem | Solution |
|---------|----------|
| "ENROLLMENT_TOKEN is required" | First run needs the token. Set `-e ENROLLMENT_TOKEN=...` (Docker) or `--token` (Go) |
| "Enrollment failed (HTTP 401)" | Token expired or already used. Generate a new one in the UI |
| Publisher shows "offline" in UI | Check UDP reachability from publisher to broker. Check logs |
| DNS not detected | Docker: mount host resolv.conf. Go: use `--dns` flag |
| Container restarts in loop | Check `docker logs`. Common: WG interface conflict |
| Tunnel active but no traffic | Check exposed_cidrs in UI. Docker: verify `--network host` is set |

## Architecture Note

Both publisher implementations follow the same lifecycle:
1. **Enroll** with the control plane (get broker public key + tunnel IP)
2. **Create** a WireGuard tunnel to the broker
3. **Forward** traffic between the WG tunnel and the local network (iptables masquerade)
4. **Report** heartbeats every 30s (status, handshake age, traffic stats, version)

Traffic flow: `Client → Broker → WG tunnel → Publisher → Local Network`

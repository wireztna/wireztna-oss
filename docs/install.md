# Install — WireZTNA Community Edition (bare IP, no domain)

CE runs on a single Linux host with a **public IP** and **no domain**, served over
plain HTTP on port 80. No AWS / S3 / SSM / Cloudflare / DNS is required.

The broker host **builds nothing**. You build the UI and the Go binaries once on a
dev box, assemble a release tree, copy it to the broker, and run `install.sh`.

## Prerequisites

- A Linux VPS with a **public IP** (Ubuntu 22.04+ or Amazon Linux 2023).
- Root/sudo access. The WireGuard kernel module available.
- Open inbound ports: `80/TCP` (API + UI), `51820/UDP` (WireGuard clients),
  `51821+/UDP` (publisher tunnels, per namespace).
- A separate dev box (your laptop/CI) with Node 20 and Go 1.22+ to build artifacts.

No domain, no TLS certificate, and no AWS/Cloudflare account are required.

## 1. Build artifacts on a dev box

The broker is memory-constrained and never compiles anything. Build on your dev box:

**Web UI** (SvelteKit, adapter-node — produces `web-ui/build/`):

```bash
cd web-ui
npm ci
npm run build          # output -> web-ui/build/   (do NOT set VITE_API_URL)
```

> **Never set `VITE_API_URL`.** The UI resolves the API from the browser origin at
> runtime, so one build works for any IP or host.

**Go binaries** (client, publisher, wzctl — one `go build` per module):

```bash
# from the repo root
(cd client/wireztna-go            && go build ./...)   # desktop/TUI client
(cd publisher/wireztna-publisher-go && go build ./...)  # publisher
(cd proxy/wireztna-proxy          && go build ./...)   # wzctl + proxy
```

Produce the binaries you want to distribute (optionally cross-compile with
`GOOS`/`GOARCH`), e.g. `wzctl`, `wireztna-publisher`, and the desktop/TUI client.

## 2. Assemble the release tree

Copy the source tree plus the pre-built outputs into the install layout the
installer expects (defaults to `/opt/wireztna`):

```
<release>/
├── control-plane/            # Python source + requirements.txt
├── broker/                   # agent + scripts
├── web-ui/
│   └── build/                # <-- pre-built UI from step 1 (REQUIRED)
├── deploy/
│   ├── install.sh            # CE installer (this file documents it)
│   └── nginx/ce.conf
└── downloads/                # prebuilt client/publisher/wzctl binaries (ADDITIVE)
```

Place the Go binaries from step 1 into `downloads/`. The `downloads/` directory is
**additive** — the installer only `mkdir -p`'s it and never deletes it, so you can
drop in more binaries later without a reinstall.

Copy the assembled tree to the broker host, e.g. `scp -r ./release user@host:/opt/wireztna`.

## 3. Install the broker

On the broker host (the tree is at `/opt/wireztna` by default):

```bash
cd /opt/wireztna/deploy
sudo ./install.sh --ip <YOUR_PUBLIC_IP>
```

`--ip` is optional — without it the installer auto-detects the public IP via
`checkip.amazonaws.com`. In this no-domain mode the installer sets
`BROKER_PUBLIC_ENDPOINT=<ip>`, `BROKER_PUBLIC_SCHEME=http`, and an empty
`UI_PUBLIC_DOMAIN`, and skips all DNS/Cloudflare steps.

The installer will:
- verify `web-ui/build/` exists (it refuses to build on the host),
- install dependencies, generate `SECRET_KEY` and `BROKER_API_KEY` (`openssl rand`)
  and the WireGuard broker keypair,
- write an nginx `:80` reverse proxy,
- create and start the six systemd services (wg, api, reconciler, health, dns, ui),
- optionally prompt you to create the first admin.

## 4. Create the first admin (if not prompted)

```bash
curl -X POST http://127.0.0.1:8443/api/v1/auth/setup \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","username":"admin","password":"<strong-password>"}'
```

Then open the admin UI at `http://<YOUR_PUBLIC_IP>/` and log in with username + password.

## Alternative: docker-compose

For a Linux host with Docker, `docker-compose.yml` runs the same stack (control-plane,
web-ui, broker-agent, and an nginx `:80` reverse proxy). Copy `.env.example` to `.env`,
set `BROKER_PUBLIC_ENDPOINT`, then `docker compose up -d`. The `broker-agent` service
uses host networking and `NET_ADMIN`/`SYS_ADMIN`, so it only works on a **Linux host**.

## 5. Deploy a publisher

Inside the private network you want to reach:

```bash
sudo ./wireztna-publisher install \
  --token "http://<YOUR_PUBLIC_IP>/api/v1/publishers/enroll?token=<token>"
sudo systemctl start wireztna-publisher
```

## 6. Give users access

Create users and groups in the admin UI, assign publishers to groups, and share enroll
links. For ephemeral CLI access (CI, automation), use `wzctl` — see the `proxy/` README.

## Optional: HTTPS

CE serves plain HTTP by design. If you want TLS, put a reverse proxy (Caddy, nginx with
certbot, or Cloudflare) in front and set `BROKER_PUBLIC_SCHEME=https` (or `PUBLIC_BASE_URL`)
so generated links use `https://`/`wss://`.

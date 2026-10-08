# deploy (CE)

Self-contained broker installer. No AWS / S3 / SSM / Cloudflare required — runs on any
Linux VPS with a public IP, over plain HTTP on port 80.

## Entrypoint

- **`install.sh`** — the CE installer. No-domain mode:

  ```bash
  sudo ./install.sh --ip <PUBLIC_IP>
  ```

  Without `--ip` the installer auto-detects the public IP. It sets
  `BROKER_PUBLIC_ENDPOINT=<ip>`, `BROKER_PUBLIC_SCHEME=http`, an empty
  `UI_PUBLIC_DOMAIN`, and skips every DNS/Cloudflare step.

- `broker-install.sh` / `broker-update.sh` — the original domain-based scripts, kept
  for reference. `install.sh` is the CE entrypoint.

## The broker host builds nothing

`install.sh` consumes a **pre-built** `web-ui/build/` and prebuilt
client/publisher/wzctl binaries from `downloads/`. It runs no `npm run build` or
`go build` on the host (small VPS instances OOM). Build those on a dev box and
assemble the release tree first.

→ Full dev-box build + release-assembly + `install.sh --ip` flow:
**[../docs/install.md](../docs/install.md)**.

## What `install.sh` does

- `--ip <PUBLIC_IP>` no-domain mode (see above); skips all DNS/Cloudflare prompts.
- Verifies `web-ui/build/` exists (refuses to build on the host).
- Installs dependencies (wireguard-tools, nftables, nginx, python3.11, node20).
- Generates `SECRET_KEY` and `BROKER_API_KEY` (`openssl rand`) and the WG broker keypair.
- Writes the nginx `:80` reverse proxy (API + UI + health).
- Creates and starts the six systemd units (wg, api, reconciler, health, dns, ui).
- Enables IP forwarding.
- Optionally prompts for the first admin and POSTs it to
  `http://127.0.0.1:8443/api/v1/auth/setup`.

**Convention:** the broker `downloads/` directory is additive only — the installer
`mkdir -p`'s it and never `rm -rf`'s it.

## docker-compose

`../docker-compose.yml` runs the same stack in containers with an nginx `:80` reverse
proxy and **no `VITE_API_URL`** (the UI resolves the API from the browser origin). The
`nginx` service mounts [`nginx/ce.conf`](nginx/ce.conf). The `broker-agent` service uses
host networking and `NET_ADMIN`/`SYS_ADMIN`, so it only works on a **Linux host**.

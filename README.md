<div align="center">

<img src="https://avatars.githubusercontent.com/u/309480116?s=200&v=4" alt="WireZTNA" width="120" height="120" />

# WireZTNA — Community Edition

### Open Source, Self-hosted, ZTNA Solution based on WireGuard

Replace a flat VPN with fine-grained, per-user access to private resources —
**User → Group → Publisher → exposed resources** — on a single host with just a public IP.

<br/>

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![WireGuard](https://img.shields.io/badge/built%20on-WireGuard-88171A.svg)](https://www.wireguard.com/)
[![Python](https://img.shields.io/badge/control--plane-Python%203.11%20·%20FastAPI-3776AB.svg?logo=python&logoColor=white)](https://fastapi.tiangolo.com/)
[![Go](https://img.shields.io/badge/agents-Go-00ADD8.svg?logo=go&logoColor=white)](https://go.dev/)
[![SvelteKit](https://img.shields.io/badge/web--ui-SvelteKit-FF3E00.svg?logo=svelte&logoColor=white)](https://kit.svelte.dev/)
[![Self-hosted](https://img.shields.io/badge/self--hosted-yes-success.svg)](docs/install.md)

<br/>

**[🌐 Website](https://wireztna.com)** ·
**[💬 Discord](https://discord.gg/vqC82g2G)** ·
**[📘 Docs](docs/install.md)** ·
**[🏗️ Architecture](docs/architecture.md)** ·
**[🤝 Contributing](CONTRIBUTING.md)**

<br/>

### ⭐ If WireZTNA is useful to you, please star the repo — it genuinely helps the project grow.

<br/>

</div>

---

This is the **Community Edition (CE)**: the full WireZTNA platform, Apache-2.0 licensed,
designed to run on **a single Linux host with just a public IP — no domain required**.

> Looking for a hosted service, a web admin for teams, SSO, HA, or support?
> Those live in the commercial edition. CE gives you the complete self-hosted platform.

## Join the community

- 💬 **Discord** — questions, help, and discussion: **https://discord.gg/vqC82g2G**
- 🌐 **Website** — https://wireztna.com
- 🐛 **Issues** — bug reports and feature requests go in GitHub Issues.
- ⭐ **Star the repo** if you find it useful — visibility helps attract contributors.

---

## What you get

- **Broker** — the control plane + overlay relay (WireGuard, nftables enforcement, per-publisher namespace isolation).
- **Publisher** — a lightweight agent you drop into each private network (Go binary or Docker). Connects **outbound**; no inbound ports to open.
- **Clients** — connect over the overlay and reach only what policy allows.
- **`wzctl`** — a CLI for ephemeral, scoped access to a single `host:port` (TTL + revocation + audit). Great for CI and automation.
- **Admin web UI** — manage users, groups, publishers, and access.

The access model, enforcement, and data path are identical to the platform WireZTNA runs
in production. CE differs only in **packaging and defaults** (bare-IP, HTTP, password login
by default), not in capability.

---

## Design principles for CE

1. **Bare IP, no domain.** Everything works over `http://<public-ip>/`. TLS is optional and
   external (put Caddy / a reverse proxy / Cloudflare in front if you want HTTPS).
2. **No email required.** First-class **username + password** login. Email OTP and SSO/OIDC
   are optional and only activate when you configure them.
3. **One generic binary per OS/arch.** Clients, publisher, and `wzctl` take the broker URL at
   runtime (enroll URL / `--broker` flag). There is **no per-broker rebuild**.
4. **No vendor lock-in.** No AWS/S3/SSM/Cloudflare required. Runs on any Linux VPS.
5. **Self-hosted means yours.** No usage limits, no phone-home, no license key.

---

## Quick start (overview)

> Full, copy-pasteable instructions live in [`docs/install.md`](docs/install.md).

```bash
# On your broker host (a Linux VPS with a public IP):
sudo ./install.sh --ip <YOUR_PUBLIC_IP>

# Create the first admin (the installer can prompt for this):
#   → opens the admin UI at http://<YOUR_PUBLIC_IP>/

# Deploy a publisher inside the private network you want to reach:
sudo ./wireztna-publisher install --token "http://<YOUR_PUBLIC_IP>/api/v1/publishers/enroll?token=<token>"

# Enroll a client / use wzctl for ephemeral access:
wzctl register --broker http://<YOUR_PUBLIC_IP> --email you@example.com --save
wzctl connect  --broker http://<YOUR_PUBLIC_IP> --publisher <id> --target 10.0.0.10 --port 5432 --local-port 5432
```

---

## Tech stack

| Layer | Technology |
|-------|-----------|
| Data plane | [WireGuard](https://www.wireguard.com/), nftables, Linux network namespaces |
| Control plane | [Python 3.11](https://www.python.org/), [FastAPI](https://fastapi.tiangolo.com/), SQLAlchemy (async), SQLite |
| Agents (publisher / client / wzctl) | [Go](https://go.dev/) (wgctrl, netlink) |
| Admin web UI | [SvelteKit](https://kit.svelte.dev/), [Vite](https://vitejs.dev/) |
| Reverse proxy | [nginx](https://nginx.org/) (`:80`) |

---

## Repository layout

```
.
├── control-plane/   # FastAPI control plane (API, auth, policy, config generation)
├── broker/          # Broker agent (reconciler, health monitor, DNS proxy) + scripts
├── web-ui/          # SvelteKit admin dashboard
├── client/          # Go client (CLI/TUI)
├── publisher/       # Publisher (Go binary + Docker)
├── proxy/           # wzctl — ephemeral scoped access CLI
├── deploy/          # Self-contained broker installer (no AWS/S3/SSM)
├── docs/            # Install, configuration, architecture, operations
├── LICENSE          # Apache License 2.0
└── NOTICE           # Attribution notices
```

---

## Documentation

- [Install (bare IP, no domain)](docs/install.md)
- [Configuration reference](docs/configuration.md)
- [Architecture](docs/architecture.md)
- [Operations](docs/operations.md)

## Requirements

| Component | Minimum |
|-----------|---------|
| Broker host | Linux (Ubuntu 22.04+ / Amazon Linux 2023), WireGuard kernel module, a public IP |
| Publisher host | Linux kernel ≥ 5.6 (WireGuard built-in), or Docker |
| Client | macOS 12+, Linux (kernel ≥ 5.6 or wireguard-go), Windows 10 1809+/11 |

## Contributing

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). Join the
[Discord](https://discord.gg/vqC82g2G) to discuss ideas before opening a large PR.

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

"WireGuard" is a registered trademark of Jason A. Donenfeld. This project is not sponsored by
or affiliated with Jason A. Donenfeld or the WireGuard project.

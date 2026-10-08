# Configuration reference — WireZTNA CE

> **Status: draft/placeholder.** The authoritative, annotated list of every setting lives in
> [`../.env.example`](../.env.example). This page will expand each setting with guidance.

Highlights of CE defaults (see `.env.example` for the full list):

- `BROKER_PUBLIC_ENDPOINT` — your host's public IP (or domain). Used for the WireGuard
  endpoint and all generated links.
- `BROKER_PUBLIC_SCHEME` — `http` by default. Set `https` only if TLS is terminated in front.
- `AUTH_PROVIDER` — `local` by default (username + password). Email OTP activates only when
  `SMTP_HOST` is set; SSO/OIDC only when `OIDC_ENABLED=true`.
- `SMTP_*` — empty by default (no email). Password login is the default path.
- `PLAN_LIMITS_ENABLED` — `false` by default. Self-hosted brokers are unlimited; volume
  limits only make sense on a public multi-user service.
- `DATABASE_URL` — SQLite (single-writer; do not run multi-worker uvicorn).

For the network/overlay settings (`BROKER_OVERLAY_*`, `BROKER_TUNNEL_*`, `BROKER_WG_PORT`),
the defaults are safe unless they collide with your existing LAN ranges.

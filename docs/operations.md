# Operations — WireZTNA CE

> **Status: placeholder.** Day-to-day operational guidance (service management, backups,
> upgrades, troubleshooting) will be adapted to the CE/self-hosted context in the final step.

## Services (systemd)

A CE broker runs these units (created by the installer):

- `wireztna-wg` — the client-facing WireGuard interface
- `wireztna-api` — the FastAPI control plane (uvicorn, internal :8443)
- `wireztna-reconciler` — converges kernel state from desired policy
- `wireztna-health` — peer/publisher health monitor
- `wireztna-dns` — per-client DNS proxy
- `wireztna-ui` — the admin web UI (internal :3000)

nginx on `:80` fronts the API and UI.

## Backups

Back up the SQLite database (`DATABASE_URL`) and the broker's `.env` (contains
`SECRET_KEY`, `BROKER_API_KEY`, and the WireGuard broker keys). The `downloads/`
directory is additive — never delete it wholesale.

## Upgrades

Replace binaries and the prebuilt UI from a new release tarball, then restart the services.
Never run `npm run build` on a small broker host (it can OOM); build UI artifacts elsewhere
and ship the prebuilt output.

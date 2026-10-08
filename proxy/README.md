# proxy — wzctl (CE)

`wzctl`: a CLI for ephemeral, scoped access to a single `host:port` over a WebSocket
tunnel — TTL, revocation, and audit built in. Independent of the WireGuard client; ideal
for CI pipelines and automation.

> **Status: populated.** The CE source is in this directory. See `../docs/ce-build-plan.md`.

**Stack:** Go (WebSocket client + local TCP relay).

**CE-specific changes applied:**
- Every command takes `--broker <url>` at runtime; broker URL is not compiled in.
- Genericize the download URL printed by `wzctl token` (point at the CE releases location,
  not a brand domain).
- Help examples use a neutral broker URL (e.g. `broker.example.com`), not a brand domain.

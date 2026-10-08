# Architecture — WireZTNA CE

> **Status: placeholder.** This will be adapted from the main project's architecture doc,
> trimmed to the CE scope, in the final packaging step.

## Overview

```
┌─────────┐       WireGuard        ┌──────────┐      WireGuard      ┌───────────┐
│  Client │◄─────(overlay)────────►│  Broker  │◄────(per-pub ns)───►│ Publisher │
│10.200.x │                        │10.200.0.1│                     │ LAN/VPC   │
└─────────┘                        └──────────┘                     └───────────┘
                                        │
                                   Control Plane
                                   FastAPI + SvelteKit (admin UI)
```

- **Broker** — control plane + overlay relay. Per-publisher Linux network namespace isolation;
  nftables enforcement of `User → Group → Publisher → exposed resources`.
- **Publisher** — outbound-connecting agent inside each private network.
- **Client / wzctl** — reach only what policy allows; `wzctl` adds ephemeral scoped access.

## Access model

```
User → (member of) → Group → (assigned) → Publisher → exposed_cidrs + published_apps
                                                     ↓
                                        nftables: per-client mark + forward rules,
                                        longest-prefix match, DNS routing hints
```

The data path and enforcement are identical to the production platform. CE changes are limited
to packaging and defaults (bare IP, HTTP, password-first login).

See the full technical details in the main project documentation; this page will be expanded
for CE in the final step.

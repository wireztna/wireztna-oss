# broker (CE)

Broker agent and infrastructure scripts: the reconciler, health monitor, and DNS proxy,
plus the Bash scripts that manage network namespaces, WireGuard interfaces, and nftables.

> **Status: populated.** The CE source is in this directory. See `../docs/ce-build-plan.md`.

**Stack:** Python (agent), Bash (scripts).

**CE notes:**
- The reconciler/enforcement engine is copied unchanged — it already works on a bare IP.
- No owner-environment coupling in shipping code here; nothing to de-hardcode.

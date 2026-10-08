# web-ui (CE)

SvelteKit admin dashboard: users, groups, publishers, access, and debug tools.

> **Status: populated.** The CE source is in this directory. See `../docs/ce-build-plan.md`.

**Stack:** SvelteKit, Svelte, Vite, adapter-node.

**CE-specific changes applied:**
- Login page defaults to **username + password**; the OTP step appears only when email is enabled.
- Add a first-run "create admin" screen that calls `POST /api/v1/auth/setup`.
- The access-pass command in `portal/+page.svelte` derives its `wss://`/`ws://` broker URL
  from `window.location` (ws/wss from page protocol) instead of a hardcoded brand domain.
- `VITE_API_URL` is NOT baked; the UI resolves the API from the browser origin at runtime.

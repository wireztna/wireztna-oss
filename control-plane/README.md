# control-plane (CE)

FastAPI control plane: API, authentication, policy, and WireGuard config generation.

> **Status: populated.** The CE source is in this directory, with the de-hardcoding,
> configurable-scheme, and password-default-login changes applied. See `../docs/ce-build-plan.md`.

**Stack:** Python 3.11, FastAPI, SQLAlchemy (async), SQLite.

**CE-specific changes applied:**
- Remove hardcoded owner origins from CORS (`main.py`); derive from settings + `CORS_EXTRA_ORIGINS`.
- `ONBOARDING_FROM` derived from `SMTP_FROM` (no brand literal) in `email_service.py`.
- Introduce `PUBLIC_BASE_URL` / `BROKER_PUBLIC_SCHEME` (default `http`) and route all
  server-generated URLs through it (publishers/users/me/access_passes).
- Expose `email_enabled` on the public auth-config endpoint so the UI can default to password.
- Add an endpoint to set/reset a user's password without SMTP.

# Contributing to WireZTNA Community Edition

Thanks for your interest in contributing. This project is Apache-2.0 licensed and welcomes
issues and pull requests.

## Ground rules

- **License of contributions.** By submitting a contribution you agree it is licensed under the
  Apache License 2.0, consistent with [LICENSE](LICENSE). Do not submit code you are not
  authorized to license this way.
- **Scope.** CE is the self-hosted platform. Please keep contributions focused on the
  self-hosted experience (bare-IP deployment, installer, docs, bug fixes, platform features).
- **No secrets.** Never commit credentials, keys, tokens, or environment-specific values
  (public IPs, account IDs, internal domains). Use placeholders and environment variables.

## Development setup

See [docs/install.md](docs/install.md) for standing up a broker, and the per-component
READMEs for building each piece:

- `control-plane/` — Python 3.11, FastAPI (`pip install -r requirements.txt`)
- `web-ui/` — SvelteKit (`npm install && npm run build`)
- `client/` — Go 1.22 (`make build`)
- `publisher/` — Go (`make build-linux`)
- `proxy/` — Go — `wzctl` (`make build`)

## Pull requests

1. Keep PRs small and focused on one change.
2. Describe what changed, why, and how you verified it.
3. If you change a file, state it clearly in the PR (and keep modified-file notices intact,
   per the Apache-2.0 redistribution terms).
4. Don't bake deployment-specific values into code. Configuration belongs in env/settings.

## Reporting security issues

Please do not open public issues for security vulnerabilities. See
[docs/security.md](docs/security.md) for how to report privately.

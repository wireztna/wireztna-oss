# client (CE)

Go client (CLI/TUI) for macOS, Linux, and Windows.

> **Status: populated.** The CE source is in this directory. See `../docs/ce-build-plan.md`.

**Stack:** Go 1.22, wgctrl, bubbletea.

**CE notes:**
- The broker URL is supplied at runtime via `wireztna enroll "<url>"` and written to
  `~/.wireztna/config.yaml`. The only build-time `ldflags` are version strings.
- **One generic binary per OS/arch works against any broker — no per-broker rebuild.**
- `make build-all` produces all platform binaries for a release.

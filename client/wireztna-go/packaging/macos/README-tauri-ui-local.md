# macOS arm64 Tauri UI-only packaging evidence

This directory describes a **local-only UI preview**, not a functional WireZTNA pilot. The Tauri shell has no core, privileged helper, installer, authenticated native IPC transport, tunnel ownership, public signing identity, notarization, or stapling. Production remains fail-closed with `TAURI_DESKTOP_IPC_READINESS=not_implemented` and `DESKTOP_IPC_NOT_READY`.

## Local flow

From this directory on a native Apple Silicon host:

1. `./tauri-ui-local-preflight.sh` inspects already-present tools and inputs without installing anything. It derives and executes Rust 1.98.1 directly from `rust-toolchain.toml` (never through ambient shims), verifies the `aarch64-apple-darwin` host, requires the exact local macOS 15.4 SDK and validates its `SDKSettings.plist`, then records schema provenance, source commit/dirty state, SDK/toolchain identity, and lockfile hashes.
2. `./build-tauri-ui-local.sh` asks the pinned local Tauri CLI for an arm64 `.app`, applies ad-hoc signing, verifies it, and writes limitation-bearing evidence without overwriting prior evidence. Evidence binds the complete bundle content, lockfiles, schema draft, commit, and dirty state when available. It does not run or install the app.
3. `./create-tauri-ui-local-dmg.sh APP EVIDENCE OUTPUT-local-only.dmg` accepts only matching local UI evidence, rechecks the complete bundle hash, and emits a local DMG plus propagated evidence. It does not establish release eligibility.

`create-dmg.sh` remains disabled. No command here uses `sudo`, installs under a system prefix, launches the app, registers a service, publicly signs, notarizes, or staples.

## IPC v2 draft provenance and gate

App and optional DMG sidecar evidence use schema version 3 and require/propagate the exact SDK root/version plus pinned Rust channel/host. Older schema-2 sidecars are rejected rather than silently upgraded.

`ipc-v2-wire.snapshot.json` is an exact-object UI draft with provenance `ui_draft_pending_go_exporter` and `canonical:false`. It was prepared from the final read-only controller `2015c9ad1ec110167f493f70fab780b3845960c3` and IPC `dae1ab53f77e2928435c2e2743e2fa348669b543` heads after their fix commits arrived. It records the Go stream cursor exactly as `stream_id` + `epoch` + `sequence`, but remains a hand-maintained draft and therefore makes no canonical or compatibility-evidence claim.

`verify-ipc-v2-snapshot.sh` requires output whose provenance is `go_exporter` and whose canonical flag is true. While the checked-in expectation remains the UI draft, verification deliberately stays blocked even if a hand-edited candidate has identical bytes. The owning Go wave must add an exporter and replace the draft with exporter output before the exact comparison can become passing evidence. A later match still does not activate Tauri IPC or change desktop readiness.

## Legacy plist integration blocker

The insecure legacy launchd plist is intentionally absent and must not be restored or activated. If an external installer, release job, or integrator still references that plist, the local UI-only artifact is blocked for that integration until the external reference is removed or replaced by a separately reviewed authenticated service design.

# Toolkit Gate evidence

This directory is the local evidence boundary for comparing `tauri-svelte` and `wails-svelte`. The machine-readable gate definition lives at `src/lib/toolkit-gate/matrix.json`; its structural and honesty constraints are enforced by `src/lib/toolkit-gate/matrix.test.ts`.

Task 1.3 defines the targets, criteria, protocol, and thresholds only. Tasks 2.4 and 2.5 remain responsible for running measurements, recording evidence, and making the ADR-backed toolkit decision. There are currently no measurements and no selected candidate.

## Target matrix

Both candidates must be exercised with the same mock IPC scenarios and representative Connection screen on:

- macOS Apple Silicon `arm64`; local/ad-hoc signing may be recorded only as engineering-pilot evidence.
- Ubuntu 24.04 LTS on `amd64` and `arm64`: stock GNOME 46 and KDE Plasma 5.27, each on Wayland and X11.

macOS Intel (`x86_64`) is excluded. Evidence from macOS arm64, another desktop, another display protocol, or another architecture never transfers to an unmeasured row. The Ubuntu rows define toolkit behavior only and do not create a package or platform-support claim.

## Mandatory behavior and thresholds

`tray-menu` and `normal-window` are independent criteria. `tray-menu` passes only with recorded working tray/menu capability; an absent or unsupported tray fails that criterion and the normal-window fallback cannot substitute for it. `normal-window` requires separate observations for tray-present and tray-absent scenarios, with the latter covering the complete window-and-notification fallback.

- Cold startup to an interactive normal window: p95 must be at most 2 seconds, using at least 10 complete cold starts.
- Idle resident set size after stabilization: maximum RSS must be at most 150 MiB.
- Idle process CPU after stabilization: median must be at most 1% and p95 at most 3%.
- Reproducibility requires at least two clean, dependency-locked builds with matching normalized artifact SHA-256 digests and the normalization method recorded.

Percentiles use nearest-rank: express `p` as a fraction between zero and one, sort samples ascending, and select one-based rank `ceil(p * n)`. Before every startup attempt, terminate and confirm absence of the previous complete candidate process tree, restore the recorded application-profile and mock-transport baselines, wait for the recorded idle baseline, and launch a new root process. Evidence must record OS page-cache preparation/state; the protocol does not assume a universal cache-reset tool. A failed launch, timeout, missing startup sample, or removed outlier invalidates the run.

Idle measurements use a 60-second stabilization period followed by exactly 300 valid samples over 300 seconds at one-second intervals against a steady mock transport. Each timestamp covers the candidate's complete recursive process tree, including webview processes and every child/descendant, with membership refreshed for each sample. RSS and CPU are summed across that tree per timestamp; the gate then uses maximum RSS, median CPU, and nearest-rank p95 CPU. A missing sample, collector error, inaccessible process, or incomplete tree invalidates the run and cannot produce `pass`.

The protocol deliberately does not prescribe one cross-platform measurement tool. Every evidence artifact must record the actual tool name, version, and collection method or command used.

All behavior, IPC, accessibility, and build criteria in the JSON matrix are mandatory on every required target. VoiceOver is used on macOS and Orca on Ubuntu; screen-reader and keyboard journeys require recorded human evidence.

## Status and evidence rules

The only result states are `not_run`, `pass`, `fail`, and `blocked`.

- A missing result is `not_run`, never `pass`.
- `blocked` and `fail` never count as `pass`.
- `pass` requires non-empty relative file references that are descendants of `toolkit-gate/evidence/`; absolute paths, traversal, the directory itself, and references outside that boundary are invalid.
- Evidence must include exact environment metadata and the measurement tool name/version/method used. Partial collection and missing required metadata fail closed.
- A mitigation has no special status. It must be bounded and testable, then re-run; only fresh passing evidence may produce `pass`.
- Any non-null `decision.selectedCandidate` must identify a catalog candidate.
- A `pass` decision requires a selected candidate, an ADR path, and exactly one explicit passing result with valid evidence for every required target × mandatory criterion cell of that candidate.
- Toolkit selection and its ADR are deferred to task 2.5. Do not infer a winner from candidate order or partial results.

Evidence records should be immutable JSON or reviewable text and include the candidate, target ID, criterion ID, exact OS/image version, architecture, desktop package version, display protocol, toolkit/toolchain versions, applied protocol, raw samples or observations, measurement tool name/version/method, status, evidence digest, operator, and timestamp. Do not place secrets, tokens, keys, PSKs, absolute user paths, release claims, or publication metadata here.

## Validation

Run from `client/wireztna-desktop/` without starting a watcher:

```bash
npm test -- --run src/lib/toolkit-gate/matrix.test.ts
npm run check
npm run build
```

The evidence directory is intentionally empty except for `.gitkeep` until task 2.4 records real observations.

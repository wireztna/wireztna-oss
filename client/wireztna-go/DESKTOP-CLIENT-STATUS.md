# WireZTNA Desktop Client — Status & Roadmap

## Current State (v0.9.30 candidate, September 2026)

The source tree is at v0.9.30 (local Windows latency-fix candidate). The broker currently exposes Windows v0.9.29 (MSI), macOS/Linux v0.9.28, and Android v0.1.4. Older versioned artifacts remain in S3 for rollback. The professional desktop modernization described below is local and unreleased; it has not changed those production artifacts.

### Windows — PRODUCTION v0.9.29 / LATENCY-FIX CANDIDATE v0.9.30

| Component | Status | Notes |
|-----------|--------|-------|
| MSI installer | ✅ Working | Installs the privileged service/helper, native tray, authentication helper, CLI, and PATH integration |
| WireGuard dataplane | ✅ Stable | WireGuard NT kernel driver through signed `wireguard.dll`; lifecycle in `internal/wgnt/`, configuration/status through `wgctrl` |
| Windows Service / helper | ✅ Working | Owns the adapter and privileged network state; runs as LocalSystem or elevated helper as appropriate |
| System tray | ✅ Working | Connect, disconnect, project/group switch, and VPN/split switch through named-pipe IPC |
| Authentication helper | ✅ Working | Self-contained WPF `wireztna-auth.exe` handles enrollment, login, and OTP |
| IPC | ✅ Working | Tray and TUI delegate tunnel operations through `\\.\pipe\wireztna` |
| TUI | ✅ Working | Uses helper/service IPC instead of creating an independent adapter |
| DNS | ✅ Working | NRPT split-DNS rules are created and removed with the tunnel lifecycle |
| Exit node | ✅ Working | Uses `/1` AllowedIPs/routes to avoid the WireGuard NT `0.0.0.0/0` kill-switch behavior |

WireGuard NT replaced the legacy wintun + in-process wireguard-go backend in v0.9.24. Wintun is no longer the production Windows dataplane.

### macOS — CLI/TUI PRODUCTION (v0.9.28)

| Component | Status | Notes |
|-----------|--------|-------|
| CLI binaries | ✅ Working | Published v0.9.28 includes legacy amd64 and arm64 CLI binaries; the new professional desktop scope is arm64-only |
| TUI | ✅ Working | Runs directly with `sudo`; no Unix service socket is required |
| VPN/split and group changes | ✅ Working | Direct Unix path renews the session and recreates the tunnel without IPC |
| Explicit disconnect | ✅ Working | Intent is persisted so status/renewal does not silently adopt or reconnect the tunnel |
| Teardown | ✅ Working | `wg-quick down` resolves the real `utunN`, restores full-tunnel routes, removes broker exclusion state, and restores DNS |
| Desktop tray | 🔨 Built, unvalidated | Legacy code exists but has not been promoted as a supported production artifact |
| Professional arm64 preview | 🧪 UI-only local evidence | Native arm64 Tauri `.app` built locally and verified ad-hoc; evidence explicitly says no core/helper, no install/run, IPC not ready, and no functional-pilot/public eligibility |
| Public GA | ⛔ Blocked | No Developer ID, required entitlements, hardened-runtime/notarization/stapling evidence, or public-trust artifact |

### Linux — CLI/TUI PRODUCTION (v0.9.28)

| Component | Status | Notes |
|-----------|--------|-------|
| CLI binaries | ✅ Working | `wireztna-0.9.28-linux-amd64` and `wireztna-0.9.28-linux-arm64` |
| TUI | ✅ Working | Runs directly with `sudo` or the required network capabilities; no Unix service socket is required |
| VPN/split and group changes | ✅ Working | Direct Unix path tears down DNS/routes before recreating the interface |
| Explicit disconnect | ✅ Working | Persisted disconnect state prevents unwanted reconnect/adoption |
| Session renewal | ✅ Working | Mutations are serialized and stale renewal results are rejected by generation |
| Desktop tray | ❌ Not distributed | Cross-build requires CGo/GTK dependencies |
| DEB E0 contract | 🧪 Build-only, fail-closed | Reproducible Python-stdlib builder/verifier accepts only explicitly supplied real desktop ELF prebuilts; local contract passes and positive E0 deliberately skips because none were supplied |
| Service/package lifecycle | ⛔ Blocked | Legacy Unix IPC/`CmdQuit` is excluded; secure service activation waits for desktop-core DX-02 |

## Professional Desktop Modernization — Local Checkpoint

| Area | Delivered locally | Still required before product support |
|------|-------------------|---------------------------------------|
| Core | Platform-neutral ports plus transactional controller with serialized intent, disconnect precedence, desired/applied journal, reverse rollback, health gate, kernel lease, recovery gate, snapshots/events; inert IPC v2 library with strict bounded framing, deadlines, idempotency, stream replay/resync, and command classes | Controller fault/property tests, canonical generated wire fixture, UID/SID peer transports, SecretStore/owner migration, existing entry-point wiring, and complete DX-00/DX-02 gates |
| UI | Pinned Svelte/Tauri shell, strict IPC-v2 TS decoder, operation-ID/terminal+healthy completion, stream identity/resync handling, close-to-hide/quit-only shell behavior, 31 passing tests, zero Svelte diagnostics, and production fail-closed readiness | Canonical Go-exported schema, authenticated native transport, remaining shell/product/accessibility/localization journeys, toolkit ADR, and human/platform validation |
| macOS | Darwin backend split and provisional ADR; native Apple Silicon preflight; local arm64 UI-only `.app` built with `VITE_API_URL` unset, ad-hoc signature, content hashes, no core/helper, and no install/run | Secure helper/peer auth, controller adapters, SecretStore, functional `.pkg`, lifecycle/runtime evidence, and controlled pilot; Developer ID/notarization remains externally blocked |
| Linux | Exact support matrix plus reproducible Python-stdlib DEB builder/verifier, immutable input snapshots, exact build-only evidence model, and amd64/arm64 backend cross-compilation | Real desktop ELF prebuilts, positive E0 DEBs, secure service/IPC, assets/lifecycle scripts, install/upgrade/uninstall/runtime matrix, and complete DEB_Gate |
| Windows/release | Canonical WiX source and guarded builder; same-version/downgrade policy; strict platform-evidence validation/eligibility tests; builder proven to exit 78 with only a blocked report and no MSI while BU/GRGW + legacy `CmdQuit` remain | SID-authenticated IPC v2 transport, legacy command removal, Windows-host MSI inspection, Authenticode/SmartScreen evidence, platform consumption/trust/updater/download/publication work |

No new dataplane, service installation, package installation, privileged host mutation, AWS/EC2/SSM/S3 operation, public artifact, or channel promotion was performed. The local macOS output is an ignored UI preview, not a distributable client.

## Architecture

### Windows managed path

```text
tray / TUI / CLI
       │ named-pipe IPC (\\.\pipe\wireztna)
       ▼
privileged service or elevated helper
       │
       ├── session renewal and state ownership
       ├── WireGuard NT adapter (`wireguard.dll` + `wgctrl`)
       ├── IP and route management
       └── NRPT DNS lifecycle
```

Only the privileged owner creates or mutates the Windows tunnel. The tray and TUI observe the same state and cannot create competing adapters.

### Current macOS/Linux direct path

```text
privileged CLI / TUI
       │ direct calls (no Unix IPC service)
       ▼
session renewal → DNS cleanup → tunnel teardown/recreate → DNS setup
       │
       ├── macOS: wg-quick + wireguard-go (`utunN` at runtime)
       └── Linux: kernel WireGuard + iproute2/resolvectl
```

On Unix, pressing `q` or Ctrl-C leaves a healthy tunnel connected. `disconnect` or the TUI disconnect action records explicit intent and tears the tunnel down. Mode/group changes and expired-session recovery use the direct lifecycle. This remains the production path; the new managed desktop architecture is not active.

## Configuration and IPC

| Platform | Primary shared path | Standalone fallback | Tunnel command path |
|----------|---------------------|---------------------|---------------------|
| Windows | `C:\ProgramData\WireZTNA\` | `%USERPROFILE%\.wireztna\` | Named-pipe IPC to service/helper |
| macOS | `/etc/wireztna/client/` when installed | `~/.wireztna/` | Direct privileged CLI/TUI |
| Linux | `/etc/wireztna/client/` when installed | `~/.wireztna/` | Direct privileged CLI/TUI |

The active JSON IPC protocol remains a legacy Windows concern for existing installations. It still carries connect/disconnect/status/switch/exit-node/group operations and the unsafe `CmdQuit`; new service installation and MSI production are therefore fail-closed. There is no active `/var/run/wireztna.sock` dependency on macOS or Linux, and CLI service activation there now fails before starting IPC. IPC v2 exists only as a compiled transport-neutral library: no listener, socket/named-pipe path, UID/SID adapter, controller service wiring, legacy fallback, or production UI readiness is enabled.

## Recent Release Progress

| Version | Date | Result |
|---------|------|--------|
| v0.9.24 | 2026-08-31 | Promoted WireGuard NT to stable Windows dataplane |
| v0.9.25 | 2026-09-08 | Added native WPF authentication helper and published production MSI |
| v0.9.26 | 2026-09-08 | Made explicit Unix disconnect persistent and stopped status from adopting unrelated WG interfaces |
| v0.9.27 | 2026-09-08 | Added direct Unix VPN/split/group switching, symmetric DNS/route cleanup, and serialized renewals |
| v0.9.28 | 2026-09-08 | Fixed macOS teardown to use `wg-quick down` against the real `utunN`, restoring full-VPN routes and DNS |
| v0.9.29 | 2026-09-08 | Prevented GUI/TUI races from forcing Windows VPN back to split; added single owner and serialized IPC mutations; published MSI |
| v0.9.30 candidate | 2026-09-09 | Moves exit-node metadata lookup before full-tunnel cutover and removes duplicate Windows TUI state persistence |
| desktop wave 2 | 2026-09-11 | Local-only core/UI/macOS/Linux/release contracts integrated and validated; no version bump or publication |
| desktop wave 4 | 2026-09-11 | Transactional controller and inert IPC v2 integrated; UI contract and local arm64 UI-only bundle validated; Linux positive DEB skipped without prebuilts; Windows MSI deliberately blocked with no artifact |

The legacy v0.9.30 candidate passed its historical targeted Go/race/vet/Windows cross-build checks. Any pre-existing local MSI belongs to that legacy path and is not wave 4 evidence; the canonical professional MSI builder now remains blocked and must not be promoted.

## Known Limitations and External Gates

1. **desktop-core DX-02 remains incomplete.** The controller and transport-neutral IPC v2 library exist, but there are no exhaustive controller fault/property tests, canonical Go-exported wire fixture, UID/SID peer adapters, SecretStore/owner migration, or service/CLI/TUI wiring.
2. **IPC v2 is intentionally inert.** It exposes no listener or address and is not wired to `cmd/service`; production Tauri remains `TAURI_DESKTOP_IPC_READINESS='not_implemented'` and throws `DESKTOP_IPC_NOT_READY`.
3. **Legacy service activation is not a professional path.** Unix service/helper startup and all new service installs are blocked. Existing Windows runtime still has BU `GENERIC_READ|GENERIC_WRITE` named-pipe access plus `CmdQuit`, so MSI construction stops before candidate bytes/evidence.
4. **Toolkit_Gate has no outcome.** Wails parity, full shell/product surfaces, accessibility/performance measurements, human validation, and an ADR-backed winner are still absent.
5. **macOS evidence is UI-only.** The arm64 `.app` is locally ad-hoc signed and was not run or installed; it contains no core/helper. No `.pkg`, functional E0–E4, Developer ID, entitlements, hardened-runtime/notarization/stapling, or public GA evidence exists.
6. **Linux E0 remains unclaimed.** The stdlib contract passes and backend packages cross-compile for amd64/arm64, but no real desktop ELF prebuilts were supplied, so positive DEB construction/inspection correctly reports SKIP and no runtime row is validated.
7. **Release trust and updating remain incomplete.** Evidence schema and fail-closed eligibility exist, but there is no trust ADR/signature verifier, updater orchestration, platform artifact consumption, downloads integration, or publication tooling.
8. **Windows/macOS external trust gates remain real.** There is no Windows host/AuthentiCode/SmartScreen evidence and no Apple Developer ID/notarization capability; no runtime, installer, GA, or public-distribution claim is made.

## Build and Release

```bash
cd client/wireztna-go
make clean
make build-all
./bin/wireztna-darwin-arm64 --version
```

Use `make build-windows` only to prepare local Windows inputs. `make build-msi-windows` delegates to the canonical guarded builder and currently must stop with exit 78 plus a `*.host-blocked.json` report and no MSI until authenticated Windows IPC v2 peer authorization exists. The canonical version is `client/wireztna-go/VERSION`; no downloads integration or publication was changed here.

Production publication must follow `.kiro/steering/deployment.md`: build UI locally, use versioned S3 upload plus additive SSM extraction/copy, verify hashes and HTTP responses, and perform exact-path cleanup only. Never build the UI on the broker, expose port 8443, or use `packaging/upload-binaries.sh` while it contains `aws s3 sync --delete`.

## Key Files

| Concern | Files |
|---------|-------|
| Desktop controller and journal | `desktop/controller/` |
| Inert IPC v2 library | `internal/ipc/v2/` |
| Toolkit/UI contract | `../wireztna-desktop/src/lib/`, `../wireztna-desktop/src-tauri/` |
| Windows legacy service/IPC and guarded MSI | `cmd/service*.go`, `internal/ipc/`, `internal/winsvc/`, `packaging/windows/build-msi.sh` |
| Windows WireGuard NT lifecycle | `internal/tunnel/tunnel_windows.go`, `internal/wgnt/` |
| Platform tunnel lifecycle | `internal/tunnel/tunnel_common_unix.go`, `internal/tunnel/tunnel_darwin.go`, `internal/tunnel/tunnel_linux.go`, `internal/tunnel/tunnel_windows.go` |
| Platform DNS | `internal/dns/` |
| macOS UI-only local flow | `packaging/macos/tauri-ui-local-preflight.sh`, `packaging/macos/build-tauri-ui-local.sh`, `packaging/macos/README-tauri-ui-local.md` |
| Linux build-only DEB contract | `packaging/linux/deb_archive.py`, `packaging/linux/build_deb.py`, `packaging/linux/verify_deb.py`, `packaging/linux/package_contract_test.sh` |
| Release manifest/evidence | `release/manifest.go`, `release/evidence.go`, `release/*_test.go` |
| Release version | `VERSION`, `pkg/version/` |

## Next Steps

1. Add adversarial controller and IPC v2 tests, then export the canonical Go wire fixture; do not claim N-1 compatibility without a real codec/fixture.
2. Implement OS-authenticated Unix UID/Windows SID transport adapters, SecretStore/owner migration, and service wiring; keep all legacy activation paths blocked until DX-02 passes.
3. Complete the Tauri/Wails Toolkit_Gate, accessibility/product surfaces, and real DesktopClient bridge without weakening production fail-closed behavior.
4. Produce real Linux desktop ELF prebuilts and positive E0 DEBs, then validate package/runtime matrices only on explicitly approved hosts.
5. Build the functional macOS arm64 helper/package only after the secure architecture exists; public GA remains blocked without Developer ID/notarization.
6. Remove Windows BU/GRGW + `CmdQuit`, validate MSI on a Windows host, and add Authenticode/SmartScreen evidence before enabling the canonical builder.
7. Complete release trust, updater, downloads, and additive publication contracts before requesting any infrastructure/publication approval.

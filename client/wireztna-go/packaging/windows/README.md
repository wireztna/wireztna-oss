# WireZTNA Windows Installer

## Installed components

1. `wireztna.exe` — CLI and LocalSystem service.
2. `wireztna-desktop.exe` — native Win32 system tray application.
3. `wireztna-auth.exe` — self-contained WPF enrollment/login/OTP helper.
4. `wireguard.dll` — WireGuard NT kernel dataplane integration.
5. Windows Service `WireZTNA`, system PATH, Start Menu shortcut, and tray autostart.

WireGuard NT remains unchanged in v0.9.30. The native tray remains the daily mode controller, but the Windows package is **blocked**: `windows.ipc-v2.peer-auth` is failed and blocking. The named-pipe ACL grants BUILTIN\Users (`BU`) `GENERIC_READ|GENERIC_WRITE` (`GRGW`), and the service still accepts legacy `CmdQuit` without authenticated IPC v2 peer authorization.

## Runtime architecture

```text
wireztna-desktop.exe (standard user, Win32 tray)
        │ launches authentication when required
        ├──────────────► wireztna-auth.exe (standard user, WPF)
        │                          │
        └──────────────────────────┴── JSON over \\.\pipe\wireztna
                                       │
                              wireztna.exe service (LocalSystem)
                                       │
                              WireGuard NT kernel driver
```

The helper is self-contained and does not require a separately installed .NET runtime. WebView2 is retained inside the tray binary for one release as a fallback when an upgrade is incomplete, but is not the primary flow.

## Build status and requirements

The canonical cross-platform source contract is `wireztna-wixl.wxs`. `build-msi.sh` is currently fail-closed and emits only `wireztna-<VERSION>-windows-amd64.msi.host-blocked.json`; it does not invoke `wixl` or emit an MSI until an externally authenticated IPC v2 readiness receipt and trust contract are implemented. There is no boolean, flag, or environment override for this block.

The future build path requires Go 1.22+, .NET SDK 8.0.424, `go-winres` v0.3.3, Python 3, and `wixl`/msitools. Before `wixl`, it creates an exclusive private `mktemp` directory and snapshots exactly these five live inputs with no-follow opens and before/after identity checks: `wireztna.exe`, `wireztna-desktop.exe`, `wireztna-auth.exe`, `wireztna.ico`, and `wireguard.dll`. `wixl` receives only the immutable snapshot paths.

## Inspection and evidence

`verify-msi-e0.py` remains a future source-contract inspector; its legacy filename does not define an E0 release stage. It must not be treated as runtime qualification or as a substitute for an externally authenticated sandbox. The current failed peer-auth gate causes it to emit a separate host-blocked report and stop before snapshot, extraction, or PlatformEvidence.

When peer authentication becomes ready, the inspector still requires an externally configured/authenticated sandbox runner, policy, and receipt before extraction. It validates the exact Directory/Component graph, foreign-key mappings, operational table cardinalities, same-version/downgrade conditions, and absence of MSI permission/custom-action tables before creating an extraction directory. The permission-table check does **not** claim that runtime ACLs are narrow.

The generic Windows release profile is separate from the inspector. If it is applied to pre-existing exact MSI bytes, it emits build-only PlatformEvidence with `windows.msi.artifact-built` passed and `windows.ipc-v2.peer-auth` failed; it does not conceal the package-level block or substitute for source-contract inspection.

Any artifact evidence remains `build-only`: installation, upgrade, repair, uninstall, SCM/runtime behavior, Authenticode, SmartScreen, and publication eligibility are not asserted. The promotion evaluator requires `platform-qualified` plus `public_eligibility=true`, authenticated evidence, trusted identities, and policy, so build-only evidence is rejected for promotion.

## Upgrade and rollback

The MSI keeps UpgradeCode `D8E9F0A1-B2C3-4D5E-6F7A-8B9C0D1E2F3A`. The canonical `Upgrade` rows allow removal only of an older version and detect an installed same-or-newer version. The canonical `LaunchCondition` blocks both same-version replacement and downgrade while allowing maintenance of the installed ProductCode.

Rollback to an older release therefore requires uninstalling the newer MSI first and then installing the preserved older MSI. `C:\ProgramData\WireZTNA` is not owned by the MSI and must remain intact.

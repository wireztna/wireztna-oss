# WireZTNA Windows Client — v0.9.30

## Overview

The MSI installs:

- `wireztna.exe`: CLI and privileged Windows service.
- `wireztna-desktop.exe`: native Win32 tray application.
- `wireztna-auth.exe`: native WPF enrollment/login/OTP helper.
- `wireguard.dll`: WireGuard NT kernel dataplane.

Version 0.9.30 keeps the v0.9.29 TUI/tray/auth synchronization guarantees and reduces split-to-VPN transition latency: exit-node metadata is resolved before full-tunnel route/DNS cutover, the Windows service remains the sole runtime-state writer, and the WireGuard NT dataplane is unchanged.

## Requirements

- Windows 10 1809 or newer, or Windows 11.
- Administrator permission to install the MSI.

The WPF helper is self-contained; users do not need to install .NET or WebView2 for the primary authentication flow.

## Installation and first use

1. Download `wireztna-0.9.30-windows-amd64.msi` from Downloads.
2. Install it as Administrator.
3. Launch WireZTNA from Start Menu if the tray is not already running.
4. Paste the enrollment URL into the native setup window.
5. Request and enter the six-digit email code.
6. The helper confirms the real tunnel result and closes; daily operations continue from the tray.

The enrollment URL must use the DNS-only broker hostname supplied by the administrator.

## Daily operations

Use the tray for Connect, Disconnect, Project selection, VPN/Split Tunnel mode, traffic, and session lifetime. The WPF window opens only for enrollment or an expired/missing login token.

## Architecture

```text
User session
  wireztna-desktop.exe (native tray)
      ├─ launches wireztna-auth.exe only when authentication is required
      └─ reads status and controls sessions over \\.\pipe\wireztna

  wireztna-auth.exe (WPF, standard user, single-instance)
      └─ enrollment/login/OTP commands over the same named pipe; status reconciliation is read-only

LocalSystem
  wireztna.exe service
      └─ WireGuard NT kernel dataplane, routes, DNS, PSK renewal
```

If `wireztna-auth.exe` is missing after an incomplete upgrade or cannot start immediately, v0.9.30 can fall back to the legacy wizard for recovery. Reinstall the MSI rather than relying on that fallback permanently.

## Configuration

Shared configuration remains in `C:\ProgramData\WireZTNA\`:

- `config.yaml`
- `token`
- `state.json`
- `.installed`

Uninstall and upgrades preserve this directory.

## Troubleshooting

```powershell
sc.exe query WireZTNA
Get-Process wireztna-desktop, wireztna-auth -ErrorAction SilentlyContinue
Test-Path "C:\Program Files\WireZTNA\wireztna-auth.exe"
```

To restart only the user interface:

```powershell
Stop-Process -Name wireztna-auth,wireztna-desktop -Force -ErrorAction SilentlyContinue
Start-Process "C:\Program Files\WireZTNA\wireztna-desktop.exe"
```

This does not stop an established tunnel because the service owns the dataplane.

## Clean upgrade

```powershell
Get-Process wireztna-auth, wireztna-desktop -ErrorAction SilentlyContinue | Stop-Process -Force
Stop-Service WireZTNA -Force -ErrorAction SilentlyContinue
msiexec /i wireztna-0.9.30-windows-amd64.msi /qn /norestart /l*v wireztna-upgrade.log
Start-Service WireZTNA
Start-Process "C:\Program Files\WireZTNA\wireztna-desktop.exe"
```

## Rollback

To return to 0.9.24, uninstall 0.9.30 and install the preserved stable 0.9.24 MSI. Do not delete `C:\ProgramData\WireZTNA`; it contains enrollment and identity state.

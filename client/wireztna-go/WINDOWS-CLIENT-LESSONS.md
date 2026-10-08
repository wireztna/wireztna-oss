# WireZTNA Windows Client — Lessons Learned (Session July 24, 2026)

## Current Architecture (v0.9.10)

```
User Session (non-admin):
  wireztna-desktop.exe (tray, -H windowsgui, no console)
      │
      ├─ On launch: ensureSingleInstance() → Named Mutex prevents duplicates
      ├─ Checks IPC pipe (\\.\pipe\wireztna)
      ├─ If pipe not found → UAC prompt → launches wireztna.exe service (visible console)
      │
      └──(IPC)──► wireztna.exe service (elevated, visible console window)
                      │
                      ├─ Loads config from ~/.wireztna/ (fallback from C:\ProgramData\WireZTNA\)
                      ├─ Creates named pipe (\\.\pipe\wireztna)
                      ├─ Creates wintun adapter + wireguard-go tunnel
                      └─ Handles connect/disconnect/renew via IPC
```

## Critical Bugs Fixed & Why They Happened

### 1. UAC-launched service dies silently (`hide=true` / `SW_HIDE`)

**Symptom**: Tray shows "cannot connect open pipe" after UAC accepted.

**Root cause**: `wireztna.exe` is a **console program** (not built with `-H windowsgui`). When launched via `ShellExecuteEx` with `nShow = SW_HIDE` (0), Windows creates the process without a console window. Go's runtime and `fmt.Println` still work (they write to a null handle), BUT the process sometimes dies immediately or behaves unpredictably without a proper console allocation.

`SW_SHOWMINNOACTIVE` (7) also failed — it minimizes the window but on some configurations the console allocation still doesn't happen correctly when the parent is a GUI process (the tray, built with `-H windowsgui`).

**Fix**: Use `hide=false` → `SW_NORMAL` (1). The service opens a visible console window. It's ugly but guaranteed to work.

**File**: `cmd/desktop/service_ensure.go` — `startServiceElevated()` calls `elevation.RunElevated([]string{"service"}, false)`

**Future fix**: Convert the service to a proper Windows GUI application (no console needed) or redirect stdout/stderr to a log file before using SW_HIDE.

**NEVER DO**: `RunElevated(args, true)` for a console program launched from a GUI parent.

---

### 2. Service can't find config (`C:\ProgramData\WireZTNA\config.yaml` doesn't exist)

**Symptom**: Service starts, pipe created, but connect fails or service crashes on first command.

**Root cause**: User enrolled with `wireztna enroll` which writes config to `%USERPROFILE%\.wireztna\config.yaml`. The service looks in `C:\ProgramData\WireZTNA\` first (shared location). If it doesn't find config there, it must fall back to the user's home dir.

**Fix**: `runServiceMain()` in `cmd/service.go` now adds BOTH paths to viper:
```go
viper.AddConfigPath(cfgDir)          // C:\ProgramData\WireZTNA\
viper.AddConfigPath(userDir)         // C:\Users\<user>\.wireztna\
```

**File**: `cmd/service.go` — `runServiceMain()`

**NEVER DO**: Assume config is in ProgramData. Always fall back to `~/.wireztna/`.

---

### 3. Multiple tray icons (no single-instance protection)

**Symptom**: Every click on "Open WireZTNA" or Start Menu shortcut opens a new tray icon.

**Root cause**: No mutex or lock file to prevent multiple instances.

**Fix**: `ensureSingleInstance()` creates a Named Mutex (`Global\WireZTNA-Desktop-SingleInstance`) at process start. If the mutex already exists → `os.Exit(0)`.

**File**: `cmd/desktop/single_instance_windows.go`

**NEVER DO**: Rely on the IPC connection check to prevent duplicates. The mutex must be the FIRST thing in `main()`.

---

### 4. Close button doesn't work (WebView2 termination deadlock)

**Symptom**: Clicking "Close" button in wizard does nothing, or causes "not responding".

**Root cause attempts**:
- `wv.Dispatch(func() { wv.Terminate() })` — deadlocks (Terminate from inside event loop)
- Channel + goroutine calling `wv.Terminate()` — doesn't work reliably from non-UI thread
- `window.close()` from JavaScript — WebView2 ignores it for `SetHtml()` content (no navigation)
- `wv.Dispatch(func() { wv.Destroy() })` — also problematic

**Final fix**: Remove the Close button entirely. Users close with the window's X button (title bar). The X button is handled by the OS window manager and always works.

**NEVER DO**: Try to programmatically close a `go-webview2` window from within a binding callback. The library doesn't support this cleanly. Let the OS handle window close via the title bar X.

---

### 5. Encoding issues in tray tooltips/notifications (`â€"` instead of `—`)

**Symptom**: "Ready â€" right-click to connect" in the notification popup.

**Root cause**: The `getlantern/systray` library and Windows toast notifications pass through Win32 APIs that may not handle multi-byte UTF-8 correctly on all codepages. The emdash `—` (U+2014, bytes E2 80 94) gets interpreted as Windows-1252 where E2=â, 80=€, 94=".

**Fix**: Replace all Unicode special characters (emdash, arrows, etc.) with ASCII equivalents in user-visible text:
- `—` → `-`
- `↑` → use HTML entities only inside WebView HTML, never in systray/toast

**Files**: `cmd/desktop/main.go` — all `systray.SetTooltip()`, `AddMenuItem()`, `showNotification()` calls.

**NEVER DO**: Use non-ASCII characters in systray menu items, tooltips, or Windows toast notifications.

---

### 6. Window too small (buttons cut off)

**Symptom**: Connect/Disconnect buttons not visible without manually resizing.

**Fix**: Increased from 520→580 (wizard) and 480→560 (status).

**File**: `cmd/desktop/wizard_windows.go` — `runWindow()`

---

### 7. MSI doesn't replace existing binaries on upgrade

**Symptom**: Install new MSI → old version still running.

**Root cause**: `wixl` (WiX v3 cross-platform) doesn't handle `MajorUpgrade` + `RemoveExistingProducts` reliably. Also, if processes are running with files locked, MSI schedules replacement for next reboot (or fails silently).

**Workaround for users**:
```powershell
taskkill /F /IM wireztna-desktop.exe 2>$null
taskkill /F /IM wireztna.exe 2>$null
# Desinstalar desde Aplicaciones, luego instalar nueva versión
```

**Future fix**: Switch from `wixl` to proper WiX v4 on a Windows build machine, or use NSIS/Inno Setup which handle upgrades better.

**NEVER DO**: Assume MSI will replace running binaries. Always kill processes first.

---

### 8. Cloudflare caches download binaries

**Symptom**: Download new version from `https://broker.example.com/api/v1/downloads/...` but get old binary.

**Root cause**: Cloudflare proxy (orange record) caches responses. The `/downloads/` endpoint serves static files that don't change URL between versions (same filename).

**Workaround**: Use the DNS-only record: `http://wg-broker.example.com/api/v1/downloads/...`

**Future fix**: 
- Add `Cache-Control: no-cache` header to download responses
- Or include version in filename (already done for MSI: `wireztna-0.9.10-windows-amd64.msi`)
- Or purge Cloudflare cache after each deploy

**NEVER DO**: Assume HTTPS URL serves fresh content after a deploy. Always verify `--version` after download.

---

## Build Checklist (Windows MSI Release)

```bash
# 1. Update VERSION file
echo "X.Y.Z" > VERSION

# 2. Update WXS version
sed -i 's/Version="OLD"/Version="NEW"/' packaging/windows/wireztna-wixl.wxs
sed -i 's/Maximum="OLD"/Maximum="NEW"/' packaging/windows/wireztna-wixl.wxs

# 3. Update downloads.py CLIENT_VERSION
sed -i 's/CLIENT_VERSION", "OLD"/CLIENT_VERSION", "NEW"/' ../../control-plane/app/routers/downloads.py

# 4. Generate Windows resources (icon + manifest)
make winres  # Generates .syso files

# 5. Build Windows binaries
make build-windows  # bin/windows/wireztna.exe + wireztna-desktop.exe

# 6. Invoke only the canonical guarded MSI builder
mkdir -p dist
./packaging/windows/build-msi.sh "X.Y.Z" bin/windows "dist/wireztna-X.Y.Z-windows-amd64.msi"
# Current expected result: exit 78 plus
# dist/wireztna-X.Y.Z-windows-amd64.msi.host-blocked.json;
# no MSI is emitted until windows.ipc-v2.peer-auth is satisfied.

# 7. Build all platforms
make build-all

# 8. Package/upload only after the authenticated IPC readiness gate exists
# and the canonical builder has produced verified release evidence.

# 9. VERIFY on broker only after separately authorized publication:
strings /opt/wireztna/downloads/wireztna-windows-amd64.exe | grep "version.Version=X.Y.Z"
```

**CRITICAL**: `make build-all` must run AFTER updating VERSION. The Makefile reads VERSION at build time. If you run it before updating, you get binaries with the old version string.

---

## Test Checklist (Windows, after install)

1. `wireztna.exe --version` → correct version
2. Launch tray → UAC prompt appears → accept → console window with "[service] started" appears
3. Tray icon shows → right-click → Connect → connects (shows green icon + overlay IP in tooltip)
4. Launch tray again → second instance exits immediately (no duplicate icon)
5. Open wizard → X button closes it cleanly
6. Notification text is clean ASCII (no garbled characters)
7. All buttons visible without resize

---

## Files Modified This Session

| File | Change |
|------|--------|
| `cmd/desktop/service_ensure.go` | UAC with `hide=false`, increased wait to 10s, imports elevation package |
| `cmd/desktop/single_instance_windows.go` | NEW — Named Mutex for single instance |
| `cmd/desktop/single_instance_other.go` | NEW — no-op stub for non-Windows |
| `cmd/desktop/main.go` | ASCII-only text in tooltips/menus, call `ensureSingleInstance()` first |
| `cmd/desktop/wizard_windows.go` | Remove Close buttons, fix window height (580/560), channel-based close (unused now) |
| `cmd/service.go` | Add `UserConfigDir()` fallback for config loading |
| `internal/config/config.go` | Export `UserConfigDir()` |
| `internal/elevation/elevate_windows.go` | Add `swShowMinNoActive` constant (unused now, kept for future) |
| `winres/winres.json` | Update version to 0.9.10, use .png instead of .ico |
| `cmd/desktop/winres/winres.json` | Same |
| `Makefile` | Add `winres` target, `build-windows` depends on it |
| `packaging/windows/wireztna-wixl.wxs` | Add MajorUpgrade, MSIRESTARTMANAGERCONTROL, version bumps |

# WireZTNA macOS Apple Silicon unsigned package

This flow builds a generic **unsigned/ad-hoc `.pkg`** for Apple Silicon. It supports a fresh install or upgrade without embedding a workstation UID or home directory in the package. It is not Developer ID signed, notarized, or stapled; do not describe it as Gatekeeper-clean.

## Fixed contract

- Target: native `darwin/arm64`, minimum macOS 12; Rosetta is rejected.
- Build: non-root, macOS SDK `15.4`, Rust `1.98.1` arm64, Go `1.23.2` arm64, locked/offline application dependencies, and no `VITE_API_URL`.
- App: `/Applications/WireZTNA.app`, bundle ID `com.wireztna.desktop`.
- Root helper: `/Library/Application Support/WireZTNA/bin/wireztna`.
- Runtime: `/Library/Application Support/WireZTNA/runtime/{bash,wg,wg-quick,wireguard-go}` plus provenance, licenses, and SPDX JSON. It contains no Homebrew dylibs or user-controlled load paths.
- LaunchDaemon: `/Library/LaunchDaemons/com.wireztna.desktop-service.plist`, label `com.wireztna.desktop-service`.
- IPC: authenticated v2 only at `/var/run/wireztna/desktop-v2.sock`, owner mode `0600`.
- WireGuard state: `/Library/Application Support/WireZTNA/wireguard`, root-owned mode `0700`.
- Owner config: canonical `<console-user-home>/.wireztna`; package and uninstaller preserve it.

On upgrade, `preinstall` reads owner UID/config from the installed trusted plist and backs up app, helper, runtime, and plist before stopping the exact daemon. On fresh install, `postinstall` resolves `/dev/console` fail-closed to a non-root local account and canonical `/Users/...` home. It creates only the private `.wireztna` directory when absent.

The plist is materialized at install time with `plutil`. `KeepAlive.PathState` watches the exact owner `config.yaml`. A fresh unenrolled installation is loaded but remains inactive and has no privileged IPC socket; enrollment from the desktop app creates `config.yaml`, which activates launchd. Existing enrolled installs are validated, started, and checked for an owner-only socket.

## Build the pinned runtime

`runtime-lock.json` pins Bash 5.3.0, wireguard-tools `1.0.20260223`, wireguard-go `0.0.20250522`, source checksums/commits, architecture, minimum OS, and toolchain versions. The runtime builder rejects unexpected architectures, minimum OS versions above 12.0, non-system dylib dependencies, invalid ad-hoc signatures, source mismatches, and missing provenance.

Online source retrieval:

```sh
make macos-runtime
```

Offline build from an explicit source and Go module cache:

```sh
make macos-runtime \
  SOURCE_CACHE=/absolute/path/to/pinned-sources \
  GO_MODULE_CACHE=/absolute/path/to/go-mod-cache \
  OFFLINE=1
```

The source cache must contain `bash-5.3.tar.gz`, `wireguard-tools-git`, and `wireguard-go-git` at the locked commits. Output defaults to `dist/macos-runtime-arm64` and is never overwritten.

## Build the generic package

From `client/wireztna-go`:

```sh
make macos-pkg
```

Use `RUNTIME_DIR=/absolute/path` for a reviewed runtime other than the default generated output, and `OUTPUT=/absolute/path/name-unsigned.pkg` for an explicit non-existing destination. `macos-local-pkg` remains a compatibility alias. The default output is:

```text
dist/macos/wireztna-<version>-macos-arm64-unsigned.pkg
```

The builder validates runtime hashes/provenance, application version, arm64-only Mach-O files, macOS 12 deployment targets, ad-hoc signatures, and exact payload entries before publishing the output. It never invokes privilege elevation, Installer, launchd, S3, or network mutation. Only `build-runtime.sh` performs network retrieval, and only when `--offline` is not selected.

To remove generated runtime/package output only:

```sh
make macos-local-clean
```

This target never touches `/opt/wireztna/downloads`.

## Installation and rollback

Installation is an explicit administrator action through macOS Installer. The repository build and validation flow does not run `sudo`, `installer`, or mutating `launchctl` commands.

Upgrade rollback restores the exact previous app, helper, runtime, plist, and prior daemon active state after proving WireZTNA-owned network state is absent. Fresh-install rollback removes the generated plist and relies on Installer payload rollback; it never removes owner configuration or unrelated WireGuard/network state.

## Uninstall

`uninstall-local.sh` is a separate administrator action. It boots out only the exact label, proves owned network state absent, then transactionally stages/removes app, helper, packaged runtime, plist, IPC state, exact journals, and exact WireZTNA WireGuard files. It preserves owner configuration and forgets the package receipt only after the payload commit point.

## Static validation

```sh
bash -n packaging/macos/build-runtime.sh \
  packaging/macos/build-local-pkg.sh \
  packaging/macos/pkg-scripts/preinstall \
  packaging/macos/pkg-scripts/postinstall \
  packaging/macos/uninstall-local.sh
plutil -lint packaging/macos/pkg-scripts/com.wireztna.desktop-service.plist.in
go test ./packaging/macos
```

Developer ID signing, notarization, and stapling remain intentionally out of scope.

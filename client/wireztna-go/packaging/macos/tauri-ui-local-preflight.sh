#!/bin/bash -p
# Read-only readiness evidence for a local arm64 Tauri UI-only bundle.

set -euo pipefail
LC_ALL=C
PATH="/usr/bin:/bin:/usr/sbin:/sbin"
export LC_ALL PATH

if [ "$#" -ne 0 ]; then
    printf '%s\n' 'tauri-ui-local-preflight: no arguments are accepted' >&2
    exit 64
fi

SCRIPT_DIR=$(cd "$(/usr/bin/dirname "$0")" && pwd)
REPOSITORY_ROOT=$(cd "$SCRIPT_DIR/../../../.." && pwd)
DESKTOP_DIR="$REPOSITORY_ROOT/client/wireztna-desktop"

UNAME=/usr/bin/uname
SYSCTL=/usr/sbin/sysctl
SW_VERS=/usr/bin/sw_vers
GREP=/usr/bin/grep
CODESIGN=/usr/bin/codesign
HDIUTIL=/usr/bin/hdiutil
LIPO=/usr/bin/lipo
PLUTIL=/usr/bin/plutil
SHASUM=/usr/bin/shasum
GIT=/usr/bin/git
NODE=/opt/homebrew/bin/node
NPM=/opt/homebrew/bin/npm
EXPECTED_SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX15.4.sdk
SDKROOT=${SDKROOT:-$EXPECTED_SDKROOT}
RUST_TOOLCHAIN_FILE="$DESKTOP_DIR/rust-toolchain.toml"
RUST_CHANNEL=$(/usr/bin/sed -n 's/^[[:space:]]*channel[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$RUST_TOOLCHAIN_FILE" 2>/dev/null | /usr/bin/head -n 1 || :)
RUST_TOOLCHAIN_BIN="${HOME}/.rustup/toolchains/${RUST_CHANNEL}-aarch64-apple-darwin/bin"
CARGO="$RUST_TOOLCHAIN_BIN/cargo"
RUSTC="$RUST_TOOLCHAIN_BIN/rustc"
TAURI="$DESKTOP_DIR/node_modules/.bin/tauri"
SCHEMA="$SCRIPT_DIR/ipc-v2-wire.snapshot.json"
PACKAGE_LOCK="$DESKTOP_DIR/package-lock.json"
CARGO_LOCK="$DESKTOP_DIR/src-tauri/Cargo.lock"

kernel=$($UNAME -s 2>/dev/null || :)
architecture=$($UNAME -m 2>/dev/null || :)
hardware_arm64=$($SYSCTL -in hw.optional.arm64 2>/dev/null || :)
hardware_model=$($SYSCTL -n hw.model 2>/dev/null || :)
rosetta_translated=$($SYSCTL -in sysctl.proc_translated 2>/dev/null || :)
macos_version=$($SW_VERS -productVersion 2>/dev/null || :)
macos_build=$($SW_VERS -buildVersion 2>/dev/null || :)
sdk_version=unavailable
if [ -f "$SDKROOT/SDKSettings.plist" ] && [ -x "$PLUTIL" ]; then
    sdk_version=$($PLUTIL -extract Version raw -o - "$SDKROOT/SDKSettings.plist" 2>/dev/null || printf unavailable)
fi
cargo_version=unavailable
rustc_release=unavailable
rustc_host=unavailable
if [ -x "$CARGO" ]; then
    cargo_version=$("$CARGO" --version 2>/dev/null || printf unavailable)
fi
if [ -x "$RUSTC" ]; then
    rustc_verbose=$("$RUSTC" -vV 2>/dev/null || :)
    rustc_release=$(printf '%s\n' "$rustc_verbose" | /usr/bin/sed -n 's/^release: //p')
    rustc_host=$(printf '%s\n' "$rustc_verbose" | /usr/bin/sed -n 's/^host: //p')
    [ -n "$rustc_release" ] || rustc_release=unavailable
    [ -n "$rustc_host" ] || rustc_host=unavailable
fi

missing=
append_missing() {
    if [ -n "$missing" ]; then missing="$missing,"; fi
    missing="$missing\"$1\""
}

for entry in \
    "sw-vers:$SW_VERS" \
    "grep:$GREP" \
    "codesign:$CODESIGN" \
    "hdiutil:$HDIUTIL" \
    "lipo:$LIPO" \
    "plutil:$PLUTIL" \
    "shasum:$SHASUM" \
    "node:$NODE" \
    "npm:$NPM" \
    "cargo:$CARGO" \
    "rustc:$RUSTC" \
    "tauri-local:$TAURI"; do
    name=${entry%%:*}
    path=${entry#*:}
    [ -x "$path" ] || append_missing "$name"
done

[ "$SDKROOT" = "$EXPECTED_SDKROOT" ] && [ "$sdk_version" = "15.4" ] || append_missing "macos-sdk-15.4"
case "$cargo_version" in
    "cargo $RUST_CHANNEL "*) ;;
    *) append_missing "cargo-version-$RUST_CHANNEL" ;;
esac
[ "$rustc_release" = "$RUST_CHANNEL" ] || append_missing "rustc-version-$RUST_CHANNEL"
[ "$rustc_host" = "aarch64-apple-darwin" ] || append_missing "rustc-host-aarch64-apple-darwin"
[ -n "$hardware_model" ] || append_missing "hardware-model"
[ -n "$macos_version" ] || append_missing "macos-product-version"
[ -n "$macos_build" ] || append_missing "macos-build"

for entry in \
    "package-json:$DESKTOP_DIR/package.json" \
    "package-lock:$PACKAGE_LOCK" \
    "cargo-lock:$CARGO_LOCK" \
    "rust-toolchain:$RUST_TOOLCHAIN_FILE" \
    "tauri-config:$DESKTOP_DIR/src-tauri/tauri.conf.json" \
    "wire-v2-snapshot:$SCHEMA"; do
    name=${entry%%:*}
    path=${entry#*:}
    [ -f "$path" ] || append_missing "$name"
done

hash_if_present() {
    if [ -x "$SHASUM" ] && [ -f "$1" ]; then
        "$SHASUM" -a 256 "$1" | /usr/bin/cut -d ' ' -f 1
    else
        printf unavailable
    fi
}

package_lock_sha=$(hash_if_present "$PACKAGE_LOCK")
cargo_lock_sha=$(hash_if_present "$CARGO_LOCK")
schema_sha=$(hash_if_present "$SCHEMA")
schema_provenance=$($PLUTIL -extract provenance raw -o - "$SCHEMA" 2>/dev/null || printf unavailable)
schema_canonical=$($PLUTIL -extract canonical raw -o - "$SCHEMA" 2>/dev/null || printf unavailable)
source_commit=unavailable
source_dirty=unavailable
source_dirty_json=null
if [ -x "$GIT" ] && "$GIT" -C "$REPOSITORY_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    source_commit=$($GIT -C "$REPOSITORY_ROOT" rev-parse HEAD)
    if [ -n "$($GIT -C "$REPOSITORY_ROOT" status --porcelain --untracked-files=normal)" ]; then
        source_dirty=true
    else
        source_dirty=false
    fi
    source_dirty_json=$source_dirty
fi

config_ready=false
if [ -x "$GREP" ] \
   && "$GREP" -Eq '"active"[[:space:]]*:[[:space:]]*true' "$DESKTOP_DIR/src-tauri/tauri.conf.json" \
   && "$GREP" -Eq '"targets"[[:space:]]*:[[:space:]]*\[[[:space:]]*"app"[[:space:]]*\]' "$DESKTOP_DIR/src-tauri/tauri.conf.json" \
   && "$GREP" -Eq "TAURI_DESKTOP_IPC_READINESS[^=]*=[[:space:]]*'not_implemented'" "$DESKTOP_DIR/src/lib/shell/tauri.ts" \
   && ! "$GREP" -Fq 'desktop_ipc_' "$DESKTOP_DIR/src-tauri/src/main.rs" \
   && ! "$GREP" -Fq '/var/run/wireztna.sock' "$DESKTOP_DIR/src-tauri/src/main.rs" \
   && [ "$schema_provenance" = ui_draft_pending_go_exporter ] \
   && [ "$schema_canonical" = false ]; then
    config_ready=true
fi

status=blocked
reason=unsupported_platform
if [ "$kernel" = Darwin ] && [ "$architecture" = arm64 ] && [ "$hardware_arm64" = 1 ] && [ "$rosetta_translated" = 0 ]; then
    if [ -n "$missing" ]; then
        reason=missing_inputs
    elif [ "$config_ready" != true ]; then
        reason=unsafe_or_incomplete_configuration
    else
        status=ready
        reason=ok
    fi
elif [ "$kernel" = Darwin ] && { [ "$architecture" = x86_64 ] || [ "$rosetta_translated" = 1 ]; }; then
    reason=non_native_arm64
fi

printf '{'
printf '"schema_version":2,'
printf '"target":"darwin-arm64",'
printf '"artifact":"tauri-ui-only-app",'
printf '"signing":"ad-hoc-local",'
printf '"distribution":"local-only",'
printf '"desktop_ipc_readiness":"not_implemented",'
printf '"functional_pilot":false,'
printf '"public_distribution_eligible":false,'
printf '"ipc_schema":{"provenance":"%s","canonical":%s,"sha256":"%s"},' "$schema_provenance" "$schema_canonical" "$schema_sha"
printf '"source":{"commit":"%s","dirty":%s},' "$source_commit" "$source_dirty_json"
printf '"lockfiles":{"package_lock_sha256":"%s","cargo_lock_sha256":"%s"},' "$package_lock_sha" "$cargo_lock_sha"
printf '"toolchain":{"rust_channel":"%s","cargo_version":"%s","rustc_release":"%s","rustc_host":"%s","sdk_version":"%s"},' "$RUST_CHANNEL" "$cargo_version" "$rustc_release" "$rustc_host" "$sdk_version"
printf '"host":{"kernel":"%s","architecture":"%s","hardware_arm64":"%s","hardware_model":"%s","rosetta_translated":"%s","macos_version":"%s","macos_build":"%s","sdkroot":"%s"},' "$kernel" "$architecture" "$hardware_arm64" "$hardware_model" "$rosetta_translated" "$macos_version" "$macos_build" "$SDKROOT"
printf '"configuration_ready":%s,' "$config_ready"
printf '"missing":[%s],' "$missing"
printf '"status":"%s","reason":"%s",' "$status" "$reason"
printf '"limitations":["ui-shell-only","desktop-ipc-not-ready","go-exporter-pending","no-core","no-installer","no-public-signing","no-notarization","no-stapling"]'
printf '}\n'

if [ "$status" = ready ]; then
    exit 0
fi
exit 2

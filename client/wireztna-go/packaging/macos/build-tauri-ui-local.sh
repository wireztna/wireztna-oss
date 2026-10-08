#!/bin/bash -p
# Produces an arm64 Tauri .app for local UI inspection only. It never runs or installs it.

set -euo pipefail
LC_ALL=C
PATH="/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"
EXPECTED_SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX15.4.sdk
SDKROOT=${SDKROOT:-$EXPECTED_SDKROOT}
unset VITE_API_URL RUSTUP_TOOLCHAIN RUSTC RUSTDOC RUSTFLAGS CARGO_BUILD_TARGET CARGO_ENCODED_RUSTFLAGS
export LC_ALL PATH SDKROOT

if [ "$#" -ne 0 ]; then
    printf '%s\n' 'usage: build-tauri-ui-local.sh' >&2
    exit 64
fi

SCRIPT_DIR=$(cd "$(/usr/bin/dirname "$0")" && pwd)
REPOSITORY_ROOT=$(cd "$SCRIPT_DIR/../../../.." && pwd)
DESKTOP_DIR="$REPOSITORY_ROOT/client/wireztna-desktop"
RUST_TOOLCHAIN_FILE="$DESKTOP_DIR/rust-toolchain.toml"
RUST_CHANNEL=$(/usr/bin/sed -n 's/^[[:space:]]*channel[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$RUST_TOOLCHAIN_FILE" 2>/dev/null | /usr/bin/head -n 1 || :)
RUST_TOOLCHAIN_BIN="${HOME}/.rustup/toolchains/${RUST_CHANNEL}-aarch64-apple-darwin/bin"
PATH="$RUST_TOOLCHAIN_BIN:$PATH"
export PATH
PREFLIGHT="$SCRIPT_DIR/tauri-ui-local-preflight.sh"
TAURI="$DESKTOP_DIR/node_modules/.bin/tauri"
TARGET_DIR="$DESKTOP_DIR/src-tauri/target/aarch64-apple-darwin/release/bundle/macos"
APP="$TARGET_DIR/WireZTNA Desktop UI Preview.app"
EVIDENCE="$TARGET_DIR/wireztna-desktop-ui-local-evidence.json"
EXPECTED_BUNDLE_ID=com.wireztna.desktop.ui-preview
SCHEMA="$SCRIPT_DIR/ipc-v2-wire.snapshot.json"
PACKAGE_LOCK="$DESKTOP_DIR/package-lock.json"
CARGO_LOCK="$DESKTOP_DIR/src-tauri/Cargo.lock"

hash_file() {
    /usr/bin/shasum -a 256 "$1" | /usr/bin/cut -d ' ' -f 1
}

hash_bundle() {
    app_root=$1
    (
        cd "$app_root"
        /usr/bin/find . -print | LC_ALL=C /usr/bin/sort | while IFS= read -r entry; do
            mode=$(/usr/bin/stat -f '%Sp' "$entry")
            if [ -L "$entry" ]; then
                printf 'link\t%s\t%s\t%s\n' "$mode" "$entry" "$(/bin/readlink "$entry")"
            elif [ -f "$entry" ]; then
                printf 'file\t%s\t%s\t%s\n' "$mode" "$entry" "$(hash_file "$entry")"
            elif [ -d "$entry" ]; then
                printf 'directory\t%s\t%s\n' "$mode" "$entry"
            else
                printf 'other\t%s\t%s\n' "$mode" "$entry"
            fi
        done
    ) | /usr/bin/shasum -a 256 | /usr/bin/cut -d ' ' -f 1
}

[ ! -e "$EVIDENCE" ] || {
    printf 'build-tauri-ui-local: refusing to overwrite evidence: %s\n' "$EVIDENCE" >&2
    exit 2
}
"$PREFLIGHT"
SDK_VERSION=$(/usr/bin/plutil -extract Version raw -o - "$SDKROOT/SDKSettings.plist")
CARGO_VERSION=$("$RUST_TOOLCHAIN_BIN/cargo" --version)
RUSTC_VERBOSE=$("$RUST_TOOLCHAIN_BIN/rustc" -vV)
RUSTC_RELEASE=$(printf '%s\n' "$RUSTC_VERBOSE" | /usr/bin/sed -n 's/^release: //p')
RUSTC_HOST=$(printf '%s\n' "$RUSTC_VERBOSE" | /usr/bin/sed -n 's/^host: //p')

(
    cd "$DESKTOP_DIR"
    "$TAURI" build --target aarch64-apple-darwin --bundles app
)

[ -d "$APP" ] || { printf 'build-tauri-ui-local: expected app is absent: %s\n' "$APP" >&2; exit 2; }
INFO_PLIST="$APP/Contents/Info.plist"
EXECUTABLE_NAME=$(/usr/bin/plutil -extract CFBundleExecutable raw -o - "$INFO_PLIST")
BUNDLE_ID=$(/usr/bin/plutil -extract CFBundleIdentifier raw -o - "$INFO_PLIST")
EXECUTABLE="$APP/Contents/MacOS/$EXECUTABLE_NAME"
[ -f "$EXECUTABLE" ] || { printf '%s\n' 'build-tauri-ui-local: bundle executable is absent' >&2; exit 2; }
[ "$BUNDLE_ID" = "$EXPECTED_BUNDLE_ID" ] \
    || { printf '%s\n' 'build-tauri-ui-local: unexpected bundle identifier' >&2; exit 2; }

ARCHITECTURES=$(/usr/bin/lipo -archs "$EXECUTABLE")
[ "$ARCHITECTURES" = arm64 ] \
    || { printf 'build-tauri-ui-local: expected arm64 only, found: %s\n' "$ARCHITECTURES" >&2; exit 2; }

# Ad-hoc signing is local integrity metadata, not public trust or release signing.
/usr/bin/codesign --force --deep --sign - --timestamp=none "$APP"
/usr/bin/codesign --verify --deep --strict "$APP"
SIGNATURE_INFO=$(/usr/bin/codesign -dv --verbose=4 "$APP" 2>&1)
case "$SIGNATURE_INFO" in
    *"Signature=adhoc"*) ;;
    *) printf '%s\n' 'build-tauri-ui-local: signature is not ad-hoc' >&2; exit 2 ;;
esac

if [ -e "$APP/Contents/Library/LaunchDaemons" ] \
   || [ -e "$APP/Contents/Library/PrivilegedHelperTools" ] \
   || [ -e "$APP/Contents/MacOS/wireztna" ]; then
    printf '%s\n' 'build-tauri-ui-local: privileged/core payload detected' >&2
    exit 2
fi

BUNDLE_SHA=$(hash_bundle "$APP")
EXECUTABLE_SHA=$(hash_file "$EXECUTABLE")
PLIST_SHA=$(hash_file "$INFO_PLIST")
PACKAGE_LOCK_SHA=$(hash_file "$PACKAGE_LOCK")
CARGO_LOCK_SHA=$(hash_file "$CARGO_LOCK")
SCHEMA_SHA=$(hash_file "$SCHEMA")
SCHEMA_PROVENANCE=$(/usr/bin/plutil -extract provenance raw -o - "$SCHEMA")
SOURCE_COMMIT=null
SOURCE_DIRTY=null
if [ -x /usr/bin/git ] && /usr/bin/git -C "$REPOSITORY_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    SOURCE_COMMIT=\"$(/usr/bin/git -C "$REPOSITORY_ROOT" rev-parse HEAD)\"
    if [ -n "$(/usr/bin/git -C "$REPOSITORY_ROOT" status --porcelain --untracked-files=normal)" ]; then
        SOURCE_DIRTY=true
    else
        SOURCE_DIRTY=false
    fi
fi

EVIDENCE_TMP=$(/usr/bin/mktemp "$TARGET_DIR/.wireztna-ui-evidence.XXXXXX")
trap '/bin/rm -f "$EVIDENCE_TMP"' EXIT HUP INT TERM
/bin/cat > "$EVIDENCE_TMP" <<EOF
{"schema_version":3,"target":"darwin-arm64","artifact":"tauri-ui-only-app","bundle_identifier":"$EXPECTED_BUNDLE_ID","architectures":["arm64"],"sdkroot":"$SDKROOT","sdk_version":"$SDK_VERSION","rust_channel":"$RUST_CHANNEL","cargo_version":"$CARGO_VERSION","rustc_release":"$RUSTC_RELEASE","rustc_host":"$RUSTC_HOST","signing":"ad-hoc-local","distribution":"local-only","vite_api_url":"unset","desktop_ipc_readiness":"not_implemented","functional_pilot":false,"public_distribution_eligible":false,"contains_core":false,"contains_privileged_helper":false,"bundle_content_sha256":"$BUNDLE_SHA","executable_sha256":"$EXECUTABLE_SHA","info_plist_sha256":"$PLIST_SHA","package_lock_sha256":"$PACKAGE_LOCK_SHA","cargo_lock_sha256":"$CARGO_LOCK_SHA","ipc_schema_sha256":"$SCHEMA_SHA","ipc_schema_provenance":"$SCHEMA_PROVENANCE","source_commit":$SOURCE_COMMIT,"source_dirty":$SOURCE_DIRTY,"limitations":["not-run","not-installed","desktop-ipc-not-ready","go-exporter-pending","no-public-signing","no-notarization","no-stapling"]}
EOF
/bin/mv "$EVIDENCE_TMP" "$EVIDENCE"
trap - EXIT HUP INT TERM

printf 'Local-only UI bundle: %s\nEvidence: %s\n' "$APP" "$EVIDENCE"

#!/bin/bash -p
# Build a generic unsigned Apple Silicon installer. This script never installs it.

set -euo pipefail
LC_ALL=C
COPYFILE_DISABLE=1
EXPECTED_SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX15.4.sdk
SDKROOT=${SDKROOT:-$EXPECTED_SDKROOT}
MACOSX_DEPLOYMENT_TARGET=12.0
GOPROXY=off
GOSUMDB=off
GOTOOLCHAIN=local
CARGO_NET_OFFLINE=true
NPM_CONFIG_OFFLINE=true
unset VITE_API_URL RUSTUP_TOOLCHAIN RUSTC RUSTDOC RUSTFLAGS CARGO_BUILD_TARGET CARGO_ENCODED_RUSTFLAGS
export LC_ALL SDKROOT MACOSX_DEPLOYMENT_TARGET COPYFILE_DISABLE GOPROXY GOSUMDB GOTOOLCHAIN CARGO_NET_OFFLINE NPM_CONFIG_OFFLINE

usage() {
    printf '%s\n' 'usage: build-local-pkg.sh [--runtime-dir DIR] [--version VERSION] [--output FILE-unsigned.pkg]' >&2
    exit 64
}

VERSION=
OUTPUT=
RUNTIME_SOURCE=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --runtime-dir) [ "$#" -ge 2 ] || usage; RUNTIME_SOURCE=$2; shift 2 ;;
        --version) [ "$#" -ge 2 ] || usage; VERSION=$2; shift 2 ;;
        --output) [ "$#" -ge 2 ] || usage; OUTPUT=$2; shift 2 ;;
        *) usage ;;
    esac
done

SCRIPT_DIR=$(cd "$(/usr/bin/dirname "$0")" && /bin/pwd -P)
GO_DIR=$(cd "$SCRIPT_DIR/../.." && /bin/pwd -P)
REPOSITORY_ROOT=$(cd "$GO_DIR/../.." && /bin/pwd -P)
DESKTOP_DIR="$REPOSITORY_ROOT/client/wireztna-desktop"
PLIST_TEMPLATE="$SCRIPT_DIR/pkg-scripts/com.wireztna.desktop-service.plist.in"
RUNTIME_LOCK="$SCRIPT_DIR/runtime-lock.json"
[ -n "$RUNTIME_SOURCE" ] || RUNTIME_SOURCE="$GO_DIR/dist/macos-runtime-arm64"
case "$RUNTIME_SOURCE" in /*) ;; *) RUNTIME_SOURCE="$PWD/$RUNTIME_SOURCE" ;; esac
HOST_GO=$(command -v go || :)
RUST_CHANNEL=$(/usr/bin/sed -n 's/^[[:space:]]*channel[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$DESKTOP_DIR/rust-toolchain.toml" | /usr/bin/head -n 1)
RUST_BIN="${HOME}/.rustup/toolchains/${RUST_CHANNEL}-aarch64-apple-darwin/bin"
PATH="$RUST_BIN:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export PATH

fail() { printf 'build-local-pkg: %s\n' "$*" >&2; exit 2; }
sha256() { /usr/bin/shasum -a 256 "$1" | /usr/bin/awk '{print $1}'; }
minimum_macos() {
    /usr/bin/otool -l "$1" | /usr/bin/awk '/LC_BUILD_VERSION/{seen=1;next} seen && /minos/{print $2; exit} /LC_VERSION_MIN_MACOSX/{legacy=1;next} legacy && /version/{print $2; exit}'
}
validate_macos12_macho() {
    local path=$1 minos
    /usr/bin/lipo -archs "$path" | /usr/bin/grep -Ex arm64 >/dev/null || fail "Mach-O must be arm64-only: $path"
    minos=$(minimum_macos "$path")
    case "$minos" in 10.*|11.*|12.0) ;; *) fail "minimum macOS exceeds 12.0 or is unreadable: $path ($minos)" ;; esac
}
validate_ad_hoc() {
    local path=$1 signature
    /usr/bin/codesign --verify --strict "$path" || fail "invalid ad-hoc signature: $path"
    signature=$(/usr/bin/codesign -dv --verbose=4 "$path" 2>&1)
    case "$signature" in *'Signature=adhoc'*) ;; *) fail "Mach-O is not ad-hoc signed: $path" ;; esac
}

[ "$(/usr/bin/id -u)" -ne 0 ] || fail 'build must not run as root'
[ "$(/usr/bin/uname -s)" = Darwin ] && [ "$(/usr/bin/uname -m)" = arm64 ] || fail 'native darwin/arm64 is required'
[ "$(/usr/sbin/sysctl -in sysctl.proc_translated 2>/dev/null || printf 0)" = 0 ] || fail 'Rosetta execution is not allowed'
[ -n "$VERSION" ] || VERSION=$(/bin/cat "$GO_DIR/VERSION")
printf '%s\n' "$VERSION" | /usr/bin/grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || fail 'version must be canonical numeric major.minor.patch'
[ -n "$OUTPUT" ] || OUTPUT="$GO_DIR/dist/macos/wireztna-${VERSION}-macos-arm64-unsigned.pkg"
case "$OUTPUT" in /*-unsigned.pkg) ;; *-unsigned.pkg) OUTPUT="$PWD/$OUTPUT" ;; *) usage ;; esac
OUTPUT_DIR=$(/usr/bin/dirname "$OUTPUT")
/bin/mkdir -p "$OUTPUT_DIR"
[ -d "$OUTPUT_DIR" ] && [ ! -L "$OUTPUT_DIR" ] || fail 'output parent must be a real directory'
[ ! -e "$OUTPUT" ] && [ ! -L "$OUTPUT" ] || fail "refusing to overwrite $OUTPUT"

for tool in /usr/bin/codesign /usr/bin/ditto /usr/bin/lipo /usr/bin/otool /usr/bin/pkgbuild /usr/bin/plutil /usr/bin/shasum /usr/bin/xattr /usr/sbin/pkgutil /opt/homebrew/bin/node /opt/homebrew/bin/npm "$RUST_BIN/cargo" "$RUST_BIN/rustc" "$DESKTOP_DIR/node_modules/.bin/tauri"; do
    [ -x "$tool" ] || fail "missing pinned tool $tool"
done
[ "$SDKROOT" = "$EXPECTED_SDKROOT" ] && [ -f "$SDKROOT/SDKSettings.plist" ] \
    && [ "$(/usr/bin/plutil -extract Version raw -o - "$SDKROOT/SDKSettings.plist")" = 15.4 ] || fail 'exact macOS SDK 15.4 is required'
[ "$RUST_CHANNEL" = 1.98.1 ] \
    && [ "$("$RUST_BIN/rustc" -vV | /usr/bin/sed -n 's/^release: //p')" = 1.98.1 ] \
    && [ "$("$RUST_BIN/rustc" -vV | /usr/bin/sed -n 's/^host: //p')" = aarch64-apple-darwin ] || fail 'pinned Rust 1.98.1 arm64 toolchain is required'
GO=/opt/homebrew/bin/go
[ -x "$GO" ] || GO=/usr/local/go/bin/go
[ -x "$GO" ] || GO=$HOST_GO
[ -n "$GO" ] && [ "$("$GO" version)" = 'go version go1.23.2 darwin/arm64' ] || fail 'pinned Go 1.23.2 darwin/arm64 is required'
[ -f "$PLIST_TEMPLATE" ] && /usr/bin/plutil -lint "$PLIST_TEMPLATE" >/dev/null || fail 'launchd plist template is absent or invalid'
[ -f "$RUNTIME_LOCK" ] && [ "$(/usr/bin/plutil -extract schema_version raw -o - "$RUNTIME_LOCK" 2>/dev/null)" = 1 ] || fail 'runtime lock is absent or invalid'
[ -d "$RUNTIME_SOURCE" ] && [ ! -L "$RUNTIME_SOURCE" ] || fail 'runtime source must be a real directory'
[ -z "$(/usr/bin/find "$RUNTIME_SOURCE" -type l -print -quit)" ] || fail 'runtime source must not contain symlinks'
RUNTIME_MANIFEST="$RUNTIME_SOURCE/provenance/runtime-manifest.json"
[ -f "$RUNTIME_MANIFEST" ] && [ "$(/usr/bin/plutil -extract schema_version raw -o - "$RUNTIME_MANIFEST" 2>/dev/null)" = 1 ] || fail 'runtime manifest is absent or invalid'
[ "$(/usr/bin/plutil -extract architecture raw -o - "$RUNTIME_MANIFEST")" = arm64 ] \
    && [ "$(/usr/bin/plutil -extract minimum_macos raw -o - "$RUNTIME_MANIFEST")" = 12.0 ] || fail 'runtime manifest platform mismatch'
for component in bash wg wg-quick wireguard-go; do
    [ -f "$RUNTIME_SOURCE/$component" ] && [ ! -L "$RUNTIME_SOURCE/$component" ] && [ -x "$RUNTIME_SOURCE/$component" ] || fail "runtime component is absent or not executable: $component"
    expected=$(/usr/bin/plutil -extract "components.$component.sha256" raw -o - "$RUNTIME_MANIFEST")
    [ "$(sha256 "$RUNTIME_SOURCE/$component")" = "$expected" ] || fail "runtime manifest checksum mismatch: $component"
done
for provenance in bash.spdx.json wg.spdx.json wg-quick.spdx.json wireguard-go.spdx.json; do
    [ -f "$RUNTIME_SOURCE/provenance/$provenance" ] && [ "$(/usr/bin/plutil -extract spdxVersion raw -o - "$RUNTIME_SOURCE/provenance/$provenance" 2>/dev/null)" = SPDX-2.3 ] || fail "runtime provenance is absent or invalid: $provenance"
done
for license in bash-GPL-3.0.txt wireguard-tools-GPL-2.0.txt wireguard-go-MIT.txt; do
    [ -s "$RUNTIME_SOURCE/provenance/licenses/$license" ] || fail "runtime license is absent: $license"
done
for macho in bash wg wireguard-go; do
    validate_macos12_macho "$RUNTIME_SOURCE/$macho"
    validate_ad_hoc "$RUNTIME_SOURCE/$macho"
    dependencies=$(/usr/bin/otool -L "$RUNTIME_SOURCE/$macho" | /usr/bin/sed -n '2,$ { s/^[[:space:]]*//; s/[[:space:]](compatibility.*$//; p; }')
    while IFS= read -r dependency; do
        [ -z "$dependency" ] || case "$dependency" in /System/Library/*|/usr/lib/*) ;; *) fail "unsafe runtime dependency: $macho -> $dependency" ;; esac
    done <<EOF
$dependencies
EOF
done
[ "$(/usr/bin/head -n 1 "$RUNTIME_SOURCE/wg-quick")" = '#!/usr/bin/env bash' ] || fail 'wg-quick must use the reviewed env bash shebang'
[ "$(env -i PATH="$RUNTIME_SOURCE:/usr/bin:/bin:/usr/sbin:/sbin" "$RUNTIME_SOURCE/bash" --noprofile --norc -c 'printf "%s" "${BASH_VERSINFO[0]}"')" -ge 4 ] || fail 'runtime Bash must be version 4 or newer'
"$DESKTOP_DIR/scripts/generate-brand-icons.sh" --check || fail 'app icons do not match canonical WireZTNA web branding'

WORK=$(/usr/bin/mktemp -d "$OUTPUT_DIR/.wireztna-pkg.XXXXXX")
trap '/bin/rm -rf "$WORK"' EXIT HUP INT TERM
PAYLOAD="$WORK/payload"
APP_BUILD="$WORK/tauri-target/aarch64-apple-darwin/release/bundle/macos/WireZTNA.app"
HELPER_BUILD="$WORK/wireztna"
TMP_PKG="$WORK/package.pkg"
TAURI_VERSION_CONFIG="$WORK/tauri-version.json"
printf '{"version":"%s"}\n' "$VERSION" > "$TAURI_VERSION_CONFIG"
/bin/chmod 0600 "$TAURI_VERSION_CONFIG"
/bin/mkdir -p "$PAYLOAD/Applications" "$PAYLOAD/Library/Application Support/WireZTNA/bin" \
    "$PAYLOAD/Library/Application Support/WireZTNA/wireguard"
/bin/chmod 0700 "$PAYLOAD/Library/Application Support/WireZTNA/wireguard"

BUILD_COMMIT=$(/usr/bin/git -C "$REPOSITORY_ROOT" rev-parse --short HEAD 2>/dev/null || printf unknown)
BUILD_DATE=${SOURCE_DATE_EPOCH:+$(/bin/date -u -r "$SOURCE_DATE_EPOCH" '+%Y-%m-%dT%H:%M:%SZ')}
[ -n "$BUILD_DATE" ] || BUILD_DATE=$(/bin/date -u '+%Y-%m-%dT%H:%M:%SZ')
(
    cd "$GO_DIR"
    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 "$GO" build -mod=readonly -trimpath \
        -ldflags "-s -w -X github.com/wireztna/client/pkg/version.Version=$VERSION -X github.com/wireztna/client/pkg/version.Commit=$BUILD_COMMIT -X github.com/wireztna/client/pkg/version.BuildDate=$BUILD_DATE" \
        -o "$HELPER_BUILD" .
)
validate_macos12_macho "$HELPER_BUILD"
/usr/bin/codesign --force --sign - --timestamp=none "$HELPER_BUILD"
validate_ad_hoc "$HELPER_BUILD"

(
    cd "$DESKTOP_DIR/src-tauri"
    "$RUST_BIN/cargo" metadata --locked --offline --format-version 1 --no-deps >/dev/null
)
(
    cd "$DESKTOP_DIR"
    CARGO_TARGET_DIR="$WORK/tauri-target" WIREZTNA_APP_VERSION="$VERSION" MACOSX_DEPLOYMENT_TARGET=12.0 \
        "$DESKTOP_DIR/node_modules/.bin/tauri" build --config "$TAURI_VERSION_CONFIG" --ci --no-sign \
        --target aarch64-apple-darwin --bundles app -- --locked --offline
)
[ -d "$APP_BUILD" ] || fail 'Tauri app output is absent'
/usr/bin/grep -R -F 'wireztna-about-version:' "$DESKTOP_DIR/dist" >/dev/null \
    && /usr/bin/grep -R -F "$VERSION" "$DESKTOP_DIR/dist" >/dev/null || fail 'About UI version does not match package version'
APP_EXECUTABLE=$(/usr/bin/plutil -extract CFBundleExecutable raw -o - "$APP_BUILD/Contents/Info.plist")
[ "$(/usr/bin/plutil -extract CFBundleIdentifier raw -o - "$APP_BUILD/Contents/Info.plist")" = com.wireztna.desktop ] || fail 'app bundle identifier mismatch'
[ "$(/usr/bin/plutil -extract CFBundleShortVersionString raw -o - "$APP_BUILD/Contents/Info.plist")" = "$VERSION" ] || fail 'app short version does not match package version'
[ "$(/usr/bin/plutil -extract CFBundleVersion raw -o - "$APP_BUILD/Contents/Info.plist")" = "$VERSION" ] || fail 'app bundle version does not match package version'
validate_macos12_macho "$APP_BUILD/Contents/MacOS/$APP_EXECUTABLE"
/usr/bin/codesign --force --deep --sign - --timestamp=none "$APP_BUILD"
/usr/bin/codesign --verify --deep --strict "$APP_BUILD" || fail 'app ad-hoc signature is invalid'
APP_SIGNATURE=$(/usr/bin/codesign -dv --verbose=4 "$APP_BUILD" 2>&1)
case "$APP_SIGNATURE" in *'Signature=adhoc'*) ;; *) fail 'app is not ad-hoc signed' ;; esac

/usr/bin/ditto --norsrc --noextattr --noqtn "$APP_BUILD" "$PAYLOAD/Applications/WireZTNA.app"
/bin/cp -X "$HELPER_BUILD" "$PAYLOAD/Library/Application Support/WireZTNA/bin/wireztna"
/usr/bin/ditto --norsrc --noextattr --noqtn "$RUNTIME_SOURCE" "$PAYLOAD/Library/Application Support/WireZTNA/runtime"
/bin/chmod 0755 "$PAYLOAD/Library/Application Support/WireZTNA/bin/wireztna"
/bin/chmod -R go-w "$PAYLOAD/Library/Application Support/WireZTNA/runtime"
/usr/bin/xattr -cr "$PAYLOAD"
/bin/chmod -RN "$PAYLOAD"
[ -z "$(/usr/bin/find "$PAYLOAD" -name '._*' -print -quit)" ] || fail 'literal AppleDouble sidecar detected in staging payload'
/usr/bin/codesign --verify --strict "$PAYLOAD/Library/Application Support/WireZTNA/bin/wireztna"
/usr/bin/codesign --verify --deep --strict "$PAYLOAD/Applications/WireZTNA.app"
for macho in bash wg wireguard-go; do /usr/bin/codesign --verify --strict "$PAYLOAD/Library/Application Support/WireZTNA/runtime/$macho"; done

PKGBUILD_FILTER='(^|/)(\._[^/]*|\.DS_Store)$|(^|/)(CVS|\.svn)(/|$)'
/usr/bin/pkgbuild --root "$PAYLOAD" --scripts "$SCRIPT_DIR/pkg-scripts" --filter "$PKGBUILD_FILTER" \
    --ownership recommended --identifier com.wireztna.desktop.local-pilot --version "$VERSION" \
    --install-location / "$TMP_PKG"
PAYLOAD_FILES=$(/usr/sbin/pkgutil --payload-files "$TMP_PKG")
APPLEDOUBLE_INVALID=
while IFS= read -r payload_path; do
    case "$payload_path" in
        */._*)
            payload_dir=${payload_path%/*}; payload_name=${payload_path##*/}; logical_path="$payload_dir/${payload_name#._}"
            if ! /usr/bin/grep -Fqx "$logical_path" <<< "$PAYLOAD_FILES"; then
                printf 'build-local-pkg: orphaned AppleDouble metadata entry: %s\n' "$payload_path" >&2
                APPLEDOUBLE_INVALID=1
            fi
            ;;
    esac
done <<< "$PAYLOAD_FILES"
[ -z "$APPLEDOUBLE_INVALID" ] || exit 2
for required in \
    './Applications/WireZTNA.app' \
    './Library/Application Support/WireZTNA/bin/wireztna' \
    './Library/Application Support/WireZTNA/runtime/bash' \
    './Library/Application Support/WireZTNA/runtime/wg' \
    './Library/Application Support/WireZTNA/runtime/wg-quick' \
    './Library/Application Support/WireZTNA/runtime/wireguard-go' \
    './Library/Application Support/WireZTNA/runtime/provenance/runtime-manifest.json'; do
    /usr/bin/grep -Fqx "$required" <<< "$PAYLOAD_FILES" || fail "required package payload entry is absent: $required"
done
/bin/ln "$TMP_PKG" "$OUTPUT"

printf 'Generic unsigned package: %s\n' "$OUTPUT"
printf 'Runtime source: %s\n' "$RUNTIME_SOURCE"
printf '%s\n' 'Owner and configuration are resolved by Installer; no privileged command or network mutation was executed.'

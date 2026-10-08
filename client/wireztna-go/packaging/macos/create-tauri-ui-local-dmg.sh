#!/bin/bash -p
# Wraps an existing, evidenced Tauri UI-only .app in a local-only DMG.

set -euo pipefail
LC_ALL=C
PATH="/usr/bin:/bin:/usr/sbin:/sbin"
export LC_ALL PATH

if [ "$#" -ne 3 ]; then
    printf '%s\n' 'usage: create-tauri-ui-local-dmg.sh APP EVIDENCE_JSON OUTPUT-local-only.dmg' >&2
    exit 64
fi

APP=$1
EVIDENCE=$2
OUTPUT=$3
OUTPUT_EVIDENCE="$OUTPUT.evidence.json"
EXPECTED_BUNDLE_ID=com.wireztna.desktop.ui-preview
case "$OUTPUT" in
    *-local-only.dmg) ;;
    *) printf '%s\n' 'create-tauri-ui-local-dmg: output must end in -local-only.dmg' >&2; exit 64 ;;
esac

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

[ -d "$APP" ] || { printf '%s\n' 'create-tauri-ui-local-dmg: APP input is absent' >&2; exit 2; }
[ -f "$EVIDENCE" ] || { printf '%s\n' 'create-tauri-ui-local-dmg: evidence input is absent' >&2; exit 2; }
[ ! -e "$OUTPUT" ] || { printf '%s\n' 'create-tauri-ui-local-dmg: refusing to overwrite output' >&2; exit 2; }
[ ! -e "$OUTPUT_EVIDENCE" ] \
    || { printf '%s\n' 'create-tauri-ui-local-dmg: refusing to overwrite output evidence' >&2; exit 2; }

EVIDENCE_VERSION=$(/usr/bin/plutil -extract schema_version raw -o - "$EVIDENCE")
ARTIFACT=$(/usr/bin/plutil -extract artifact raw -o - "$EVIDENCE")
EVIDENCE_SDKROOT=$(/usr/bin/plutil -extract sdkroot raw -o - "$EVIDENCE")
EVIDENCE_SDK_VERSION=$(/usr/bin/plutil -extract sdk_version raw -o - "$EVIDENCE")
EVIDENCE_RUST_CHANNEL=$(/usr/bin/plutil -extract rust_channel raw -o - "$EVIDENCE")
EVIDENCE_RUSTC_HOST=$(/usr/bin/plutil -extract rustc_host raw -o - "$EVIDENCE")
EVIDENCE_TARGET=$(/usr/bin/plutil -extract target raw -o - "$EVIDENCE")
EVIDENCE_ARCH=$(/usr/bin/plutil -extract architectures.0 raw -o - "$EVIDENCE")
EVIDENCE_SIGNING=$(/usr/bin/plutil -extract signing raw -o - "$EVIDENCE")
EVIDENCE_DISTRIBUTION=$(/usr/bin/plutil -extract distribution raw -o - "$EVIDENCE")
EVIDENCE_BUNDLE_ID=$(/usr/bin/plutil -extract bundle_identifier raw -o - "$EVIDENCE")
EVIDENCE_BUNDLE_SHA=$(/usr/bin/plutil -extract bundle_content_sha256 raw -o - "$EVIDENCE")
EVIDENCE_EXECUTABLE_SHA=$(/usr/bin/plutil -extract executable_sha256 raw -o - "$EVIDENCE")
EVIDENCE_PLIST_SHA=$(/usr/bin/plutil -extract info_plist_sha256 raw -o - "$EVIDENCE")
EVIDENCE_PACKAGE_LOCK_SHA=$(/usr/bin/plutil -extract package_lock_sha256 raw -o - "$EVIDENCE")
EVIDENCE_CARGO_LOCK_SHA=$(/usr/bin/plutil -extract cargo_lock_sha256 raw -o - "$EVIDENCE")
EVIDENCE_SCHEMA_SHA=$(/usr/bin/plutil -extract ipc_schema_sha256 raw -o - "$EVIDENCE")
EVIDENCE_SCHEMA_PROVENANCE=$(/usr/bin/plutil -extract ipc_schema_provenance raw -o - "$EVIDENCE")
EVIDENCE_SOURCE_COMMIT=$(/usr/bin/plutil -extract source_commit raw -o - "$EVIDENCE")
EVIDENCE_SOURCE_DIRTY=$(/usr/bin/plutil -extract source_dirty raw -o - "$EVIDENCE")
EVIDENCE_READINESS=$(/usr/bin/plutil -extract desktop_ipc_readiness raw -o - "$EVIDENCE")
EVIDENCE_CORE=$(/usr/bin/plutil -extract contains_core raw -o - "$EVIDENCE")
EVIDENCE_HELPER=$(/usr/bin/plutil -extract contains_privileged_helper raw -o - "$EVIDENCE")
printf '%s\n' "$EVIDENCE_SOURCE_COMMIT" | /usr/bin/grep -Eq '^[0-9a-f]{40}$' \
    || { printf '%s\n' 'create-tauri-ui-local-dmg: source commit is absent or invalid' >&2; exit 2; }
case "$EVIDENCE_SOURCE_DIRTY" in
    true|false) ;;
    *) printf '%s\n' 'create-tauri-ui-local-dmg: source dirty flag is invalid' >&2; exit 2 ;;
esac
[ "$EVIDENCE_VERSION" = 3 ] \
    && [ "$ARTIFACT" = tauri-ui-only-app ] \
    && [ "$EVIDENCE_SDKROOT" = /Library/Developer/CommandLineTools/SDKs/MacOSX15.4.sdk ] \
    && [ "$EVIDENCE_SDK_VERSION" = 15.4 ] \
    && [ "$EVIDENCE_RUST_CHANNEL" = 1.98.1 ] \
    && [ "$EVIDENCE_RUSTC_HOST" = aarch64-apple-darwin ] \
    && [ "$EVIDENCE_TARGET" = darwin-arm64 ] \
    && [ "$EVIDENCE_ARCH" = arm64 ] \
    && [ "$EVIDENCE_SIGNING" = ad-hoc-local ] \
    && [ "$EVIDENCE_DISTRIBUTION" = local-only ] \
    && [ "$EVIDENCE_BUNDLE_ID" = "$EXPECTED_BUNDLE_ID" ] \
    && [ "$EVIDENCE_READINESS" = not_implemented ] \
    && [ "$EVIDENCE_SCHEMA_PROVENANCE" = ui_draft_pending_go_exporter ] \
    && [ "$EVIDENCE_CORE" = false ] \
    && [ "$EVIDENCE_HELPER" = false ] \
    || { printf '%s\n' 'create-tauri-ui-local-dmg: incompatible or unsafe evidence' >&2; exit 2; }

INFO_PLIST="$APP/Contents/Info.plist"
EXECUTABLE_NAME=$(/usr/bin/plutil -extract CFBundleExecutable raw -o - "$INFO_PLIST")
BUNDLE_ID=$(/usr/bin/plutil -extract CFBundleIdentifier raw -o - "$INFO_PLIST")
EXECUTABLE="$APP/Contents/MacOS/$EXECUTABLE_NAME"
[ "$BUNDLE_ID" = "$EXPECTED_BUNDLE_ID" ] \
    || { printf '%s\n' 'create-tauri-ui-local-dmg: unexpected bundle identifier' >&2; exit 2; }
[ "$(/usr/bin/lipo -archs "$EXECUTABLE")" = arm64 ] \
    || { printf '%s\n' 'create-tauri-ui-local-dmg: bundle is not arm64-only' >&2; exit 2; }
/usr/bin/codesign --verify --deep --strict "$APP"
SIGNATURE_INFO=$(/usr/bin/codesign -dv --verbose=4 "$APP" 2>&1)
case "$SIGNATURE_INFO" in
    *"Signature=adhoc"*) ;;
    *) printf '%s\n' 'create-tauri-ui-local-dmg: signature is not ad-hoc' >&2; exit 2 ;;
esac
if [ -e "$APP/Contents/Library/LaunchDaemons" ] \
   || [ -e "$APP/Contents/Library/PrivilegedHelperTools" ] \
   || [ -e "$APP/Contents/MacOS/wireztna" ]; then
    printf '%s\n' 'create-tauri-ui-local-dmg: privileged/core payload detected' >&2
    exit 2
fi

BUNDLE_SHA=$(hash_bundle "$APP")
EXECUTABLE_SHA=$(hash_file "$EXECUTABLE")
PLIST_SHA=$(hash_file "$INFO_PLIST")
[ "$BUNDLE_SHA" = "$EVIDENCE_BUNDLE_SHA" ] \
    && [ "$EXECUTABLE_SHA" = "$EVIDENCE_EXECUTABLE_SHA" ] \
    && [ "$PLIST_SHA" = "$EVIDENCE_PLIST_SHA" ] \
    || { printf '%s\n' 'create-tauri-ui-local-dmg: evidence does not belong to this complete app bundle' >&2; exit 2; }

STAGE=$(/usr/bin/mktemp -d "${TMPDIR:-/tmp}/wireztna-ui-dmg.XXXXXX")
trap '/bin/rm -rf "$STAGE"' EXIT HUP INT TERM
/bin/cp -R "$APP" "$STAGE/"
/bin/cp "$EVIDENCE" "$STAGE/LOCAL-ONLY-EVIDENCE.json"
/usr/bin/hdiutil create -quiet -format UDZO -volname 'WireZTNA UI Preview LOCAL ONLY' -srcfolder "$STAGE" "$OUTPUT"

DMG_SHA=$(hash_file "$OUTPUT")
OUTPUT_EVIDENCE_TMP=$(/usr/bin/mktemp "$OUTPUT_EVIDENCE.XXXXXX")
printf '{"schema_version":3,"artifact":"tauri-ui-only-dmg","source_sdkroot":"%s","source_sdk_version":"%s","source_rust_channel":"%s","source_rustc_host":"%s","source_bundle_content_sha256":"%s","source_executable_sha256":"%s","source_info_plist_sha256":"%s","source_package_lock_sha256":"%s","source_cargo_lock_sha256":"%s","source_ipc_schema_sha256":"%s","source_ipc_schema_provenance":"%s","source_commit":"%s","source_dirty":%s,"distribution":"local-only","desktop_ipc_readiness":"not_implemented","functional_pilot":false,"public_distribution_eligible":false,"dmg_sha256":"%s","limitations":["not-run","not-installed","desktop-ipc-not-ready","go-exporter-pending","no-public-signing","no-notarization","no-stapling"]}\n' "$EVIDENCE_SDKROOT" "$EVIDENCE_SDK_VERSION" "$EVIDENCE_RUST_CHANNEL" "$EVIDENCE_RUSTC_HOST" "$BUNDLE_SHA" "$EXECUTABLE_SHA" "$PLIST_SHA" "$EVIDENCE_PACKAGE_LOCK_SHA" "$EVIDENCE_CARGO_LOCK_SHA" "$EVIDENCE_SCHEMA_SHA" "$EVIDENCE_SCHEMA_PROVENANCE" "$EVIDENCE_SOURCE_COMMIT" "$EVIDENCE_SOURCE_DIRTY" "$DMG_SHA" > "$OUTPUT_EVIDENCE_TMP"
/bin/mv "$OUTPUT_EVIDENCE_TMP" "$OUTPUT_EVIDENCE"
printf 'Local-only DMG: %s\nEvidence: %s\n' "$OUTPUT" "$OUTPUT_EVIDENCE"

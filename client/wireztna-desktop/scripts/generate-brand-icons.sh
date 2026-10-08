#!/bin/bash -p
# Regenerate platform app icons from the public WireZTNA mark used by wireztna.com.

set -euo pipefail
LC_ALL=C
PATH=/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin
export LC_ALL PATH

SCRIPT_DIR=$(cd "$(/usr/bin/dirname "$0")" && /bin/pwd -P)
DESKTOP_DIR=$(cd "$SCRIPT_DIR/.." && /bin/pwd -P)
MANIFEST="$DESKTOP_DIR/branding/app-icon.json"
SOURCE="$DESKTOP_DIR/../../wireztna-web/apple-touch-icon.png"
TAURI="$DESKTOP_DIR/node_modules/.bin/tauri"
MODE=generate
case "${1-}" in
    '') ;;
    --check) MODE=check ;;
    *) printf '%s\n' 'usage: generate-brand-icons.sh [--check]' >&2; exit 64 ;;
esac

[ -f "$MANIFEST" ] && [ ! -L "$MANIFEST" ] || { printf '%s\n' 'generate-brand-icons: branding manifest is missing or indirect' >&2; exit 2; }
[ -f "$SOURCE" ] && [ ! -L "$SOURCE" ] || { printf '%s\n' 'generate-brand-icons: canonical website icon is missing or indirect' >&2; exit 2; }
[ -x "$TAURI" ] || { printf '%s\n' 'generate-brand-icons: pinned Tauri CLI is unavailable' >&2; exit 2; }
[ "$($TAURI icon --version)" = 'tauri-cli-icon 2.11.4' ] || { printf '%s\n' 'generate-brand-icons: Tauri icon generator version mismatch' >&2; exit 2; }

if [ "$MODE" = generate ]; then
    exec "$TAURI" icon --output "$DESKTOP_DIR/src-tauri/icons" "$MANIFEST"
fi

CHECK_DIR=$(/usr/bin/mktemp -d "${TMPDIR:-/private/tmp}/wireztna-brand-icons.XXXXXX")
trap '/bin/rm -rf "$CHECK_DIR"' EXIT HUP INT TERM
"$TAURI" icon --output "$CHECK_DIR" "$MANIFEST" >/dev/null
for icon in 32x32.png 64x64.png 128x128.png 128x128@2x.png icon.png icon.ico; do
    /usr/bin/cmp -s "$CHECK_DIR/$icon" "$DESKTOP_DIR/src-tauri/icons/$icon" \
        || { printf 'generate-brand-icons: stale generated icon: %s\n' "$icon" >&2; exit 2; }
done
if [ "$(/usr/bin/uname -s)" = Darwin ]; then
    /usr/bin/iconutil --convert iconset "$CHECK_DIR/icon.icns" --output "$CHECK_DIR/generated.iconset"
    /usr/bin/iconutil --convert iconset "$DESKTOP_DIR/src-tauri/icons/icon.icns" --output "$CHECK_DIR/versioned.iconset"
    for icon in icon_16x16.png icon_16x16@2x.png icon_32x32.png icon_32x32@2x.png \
        icon_128x128.png icon_128x128@2x.png icon_256x256.png icon_256x256@2x.png \
        icon_512x512.png icon_512x512@2x.png; do
        /usr/bin/cmp -s "$CHECK_DIR/generated.iconset/$icon" "$CHECK_DIR/versioned.iconset/$icon" \
            || { printf 'generate-brand-icons: stale ICNS representation: %s\n' "$icon" >&2; exit 2; }
    done
fi
printf '%s\n' 'WireZTNA brand icons match the canonical website source.'

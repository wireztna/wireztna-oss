#!/bin/bash -p
# Build the pinned, self-contained Apple Silicon WireGuard runtime. Never installs it.

set -euo pipefail
LC_ALL=C
COPYFILE_DISABLE=1
PATH=/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/go/bin
EXPECTED_SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX15.4.sdk
SDKROOT=${SDKROOT:-$EXPECTED_SDKROOT}
MACOSX_DEPLOYMENT_TARGET=12.0
export LC_ALL COPYFILE_DISABLE SDKROOT MACOSX_DEPLOYMENT_TARGET
unset VITE_API_URL RUSTFLAGS CARGO_ENCODED_RUSTFLAGS

usage() {
    printf '%s\n' 'usage: build-runtime.sh [--output DIR] [--source-cache DIR] [--go-module-cache DIR] [--offline]' >&2
    exit 64
}

SCRIPT_DIR=$(cd "$(/usr/bin/dirname "$0")" && /bin/pwd -P)
GO_DIR=$(cd "$SCRIPT_DIR/../.." && /bin/pwd -P)
LOCK="$SCRIPT_DIR/runtime-lock.json"
OUTPUT="$GO_DIR/dist/macos-runtime-arm64"
SOURCE_CACHE="$GO_DIR/dist/macos-runtime-sources"
GO_MODULE_CACHE=
OFFLINE=false
while [ "$#" -gt 0 ]; do
    case "$1" in
        --output) [ "$#" -ge 2 ] || usage; OUTPUT=$2; shift 2 ;;
        --source-cache) [ "$#" -ge 2 ] || usage; SOURCE_CACHE=$2; shift 2 ;;
        --go-module-cache) [ "$#" -ge 2 ] || usage; GO_MODULE_CACHE=$2; shift 2 ;;
        --offline) OFFLINE=true; shift ;;
        *) usage ;;
    esac
done
case "$OUTPUT" in /*) ;; *) OUTPUT="$PWD/$OUTPUT" ;; esac
case "$SOURCE_CACHE" in /*) ;; *) SOURCE_CACHE="$PWD/$SOURCE_CACHE" ;; esac
if [ -n "$GO_MODULE_CACHE" ]; then case "$GO_MODULE_CACHE" in /*) ;; *) GO_MODULE_CACHE="$PWD/$GO_MODULE_CACHE" ;; esac; fi

fail() { printf 'build-runtime: %s\n' "$*" >&2; exit 2; }
lock_value() { /usr/bin/plutil -extract "$1" raw -o - "$LOCK"; }
sha256() { /usr/bin/shasum -a 256 "$1" | /usr/bin/awk '{print $1}'; }

[ "$(/usr/bin/id -u)" -ne 0 ] || fail 'build must not run as root'
[ "$(/usr/bin/uname -s)" = Darwin ] && [ "$(/usr/bin/uname -m)" = arm64 ] || fail 'native darwin/arm64 is required'
[ "$(/usr/sbin/sysctl -in sysctl.proc_translated 2>/dev/null || printf 0)" = 0 ] || fail 'Rosetta execution is not allowed'
[ -f "$LOCK" ] && [ "$(/usr/bin/plutil -extract schema_version raw -o - "$LOCK" 2>/dev/null)" = 1 ] || fail 'runtime-lock.json is absent or invalid'
[ "$(lock_value architecture)" = arm64 ] && [ "$(lock_value minimum_macos)" = 12.0 ] || fail 'runtime lock platform mismatch'
for tool in /usr/bin/clang /usr/bin/codesign /usr/bin/git /usr/bin/lipo /usr/bin/make /usr/bin/otool /usr/bin/plutil /usr/bin/shasum /usr/bin/strip /usr/bin/tar; do
    [ -x "$tool" ] || fail "missing build tool $tool"
done
[ "$SDKROOT" = "$EXPECTED_SDKROOT" ] && [ -f "$SDKROOT/SDKSettings.plist" ] \
    && [ "$(/usr/bin/plutil -extract Version raw -o - "$SDKROOT/SDKSettings.plist")" = "$(lock_value toolchain.macos_sdk)" ] \
    || fail 'exact pinned macOS SDK is required'
GO=/opt/homebrew/bin/go
[ -x "$GO" ] || GO=/usr/local/go/bin/go
[ -x "$GO" ] || GO=$(command -v go || :)
[ -n "$GO" ] && [ "$("$GO" version)" = 'go version go1.23.2 darwin/arm64' ] || fail 'pinned Go 1.23.2 darwin/arm64 is required'
if [ "$OFFLINE" = false ]; then [ -x /usr/bin/curl ] || fail 'curl is required for online source retrieval'; fi

OUTPUT_PARENT=$(/usr/bin/dirname "$OUTPUT")
/bin/mkdir -p "$OUTPUT_PARENT" "$SOURCE_CACHE"
[ -d "$OUTPUT_PARENT" ] && [ ! -L "$OUTPUT_PARENT" ] || fail 'output parent must be a real directory'
[ ! -e "$OUTPUT" ] && [ ! -L "$OUTPUT" ] || fail "refusing to overwrite $OUTPUT"
WORK=$(/usr/bin/mktemp -d "$OUTPUT_PARENT/.wireztna-runtime.XXXXXX")
trap '/bin/rm -rf "$WORK"' EXIT HUP INT TERM
STAGE="$WORK/runtime"
SRC="$WORK/src"
/bin/mkdir -p "$STAGE/provenance/licenses" "$SRC"

BASH_VERSION=$(lock_value components.bash.version)
BASH_URL=$(lock_value components.bash.source_url)
BASH_SHA=$(lock_value components.bash.source_sha256)
BASH_ARCHIVE="$SOURCE_CACHE/bash-${BASH_VERSION%.*}.tar.gz"
if [ ! -f "$BASH_ARCHIVE" ]; then
    [ "$OFFLINE" = false ] || fail "offline source is absent: $BASH_ARCHIVE"
    /usr/bin/curl --fail --location --proto '=https' --tlsv1.2 --output "$BASH_ARCHIVE.part" "$BASH_URL"
    /bin/mv "$BASH_ARCHIVE.part" "$BASH_ARCHIVE"
fi
[ "$(sha256 "$BASH_ARCHIVE")" = "$BASH_SHA" ] || fail 'Bash source checksum mismatch'
/usr/bin/tar -xzf "$BASH_ARCHIVE" -C "$SRC"
BASH_SRC="$SRC/bash-${BASH_VERSION%.*}"
[ -x "$BASH_SRC/configure" ] && [ -f "$BASH_SRC/COPYING" ] || fail 'Bash source archive layout mismatch'
(
    cd "$BASH_SRC"
    CC=/usr/bin/clang CFLAGS='-Os -mmacosx-version-min=12.0' LDFLAGS='-mmacosx-version-min=12.0' \
        ./configure --without-installed-readline --disable-nls --without-bash-malloc --disable-rpath >/dev/null
    if ! /usr/bin/make -j"$(/usr/sbin/sysctl -n hw.logicalcpu)" bash >"$WORK/bash-build.log" 2>&1; then
        /usr/bin/tail -n 100 "$WORK/bash-build.log" >&2
        fail 'Bash compilation failed'
    fi
)
/bin/cp -X "$BASH_SRC/bash" "$STAGE/bash"
/usr/bin/strip -S "$STAGE/bash"

checkout_component() {
    local name=$1 repository=$2 commit=$3 destination=$4 cached
    cached="$SOURCE_CACHE/${name}-git"
    if [ -d "$cached/.git" ]; then
        [ -z "$(/usr/bin/git -C "$cached" status --porcelain)" ] || fail "$name cached repository is dirty"
        /usr/bin/git clone --quiet --no-hardlinks "$cached" "$destination"
    else
        [ "$OFFLINE" = false ] || fail "offline repository is absent: $cached"
        /usr/bin/git clone --quiet "$repository" "$destination"
    fi
    /usr/bin/git -C "$destination" checkout --quiet --detach "$commit"
    [ "$(/usr/bin/git -C "$destination" rev-parse HEAD)" = "$commit" ] || fail "$name commit mismatch"
}

TOOLS_REPOSITORY=$(lock_value components.wireguard_tools.repository)
TOOLS_COMMIT=$(lock_value components.wireguard_tools.commit)
TOOLS_VERSION=$(lock_value components.wireguard_tools.version)
TOOLS_SRC="$SRC/wireguard-tools"
checkout_component wireguard-tools "$TOOLS_REPOSITORY" "$TOOLS_COMMIT" "$TOOLS_SRC"
(
    cd "$TOOLS_SRC/src"
    /usr/bin/make clean >/dev/null
    if ! CFLAGS='-Os -mmacosx-version-min=12.0' LDFLAGS='-mmacosx-version-min=12.0' \
        /usr/bin/make -j"$(/usr/sbin/sysctl -n hw.logicalcpu)" PLATFORM=darwin CC=/usr/bin/clang wg >"$WORK/wg-build.log" 2>&1; then
        /usr/bin/tail -n 100 "$WORK/wg-build.log" >&2
        fail 'wireguard-tools compilation failed'
    fi
)
/bin/cp -X "$TOOLS_SRC/src/wg" "$STAGE/wg"
/bin/cp -X "$TOOLS_SRC/src/wg-quick/darwin.bash" "$STAGE/wg-quick"
/usr/bin/strip -S "$STAGE/wg"

GO_REPOSITORY=$(lock_value components.wireguard_go.repository)
GO_COMMIT=$(lock_value components.wireguard_go.commit)
GO_VERSION=$(lock_value components.wireguard_go.version)
GO_SRC="$SRC/wireguard-go"
checkout_component wireguard-go "$GO_REPOSITORY" "$GO_COMMIT" "$GO_SRC"
if [ "$OFFLINE" = true ]; then
    [ -n "$GO_MODULE_CACHE" ] && [ -d "$GO_MODULE_CACHE" ] || fail 'offline wireguard-go build requires --go-module-cache'
    GO_PROXY=off
    GO_SUMDB=off
else
    [ -n "$GO_MODULE_CACHE" ] || GO_MODULE_CACHE="$WORK/go-mod-cache"
    /bin/mkdir -p "$GO_MODULE_CACHE"
    GO_PROXY=https://proxy.golang.org
    GO_SUMDB=sum.golang.org
fi
(
    cd "$GO_SRC"
    printf 'package main\n\nconst Version = "%s"\n' "$GO_VERSION" > version.go
    env CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 GOTOOLCHAIN=local GOMODCACHE="$GO_MODULE_CACHE" \
        GOPROXY="$GO_PROXY" GOSUMDB="$GO_SUMDB" "$GO" build -mod=readonly -trimpath -ldflags='-s -w' -o "$STAGE/wireguard-go" .
)

/bin/chmod 0755 "$STAGE/bash" "$STAGE/wg" "$STAGE/wg-quick" "$STAGE/wireguard-go"
for macho in "$STAGE/bash" "$STAGE/wg" "$STAGE/wireguard-go"; do
    /usr/bin/lipo -archs "$macho" | /usr/bin/grep -Ex arm64 >/dev/null || fail "non-arm64 runtime binary: $macho"
    /usr/bin/codesign --force --sign - --timestamp=none "$macho"
    /usr/bin/codesign --verify --strict "$macho" || fail "invalid ad-hoc signature: $macho"
    minos=$(/usr/bin/otool -l "$macho" | /usr/bin/awk '/LC_BUILD_VERSION/{seen=1;next} seen && /minos/{print $2; exit} /LC_VERSION_MIN_MACOSX/{legacy=1;next} legacy && /version/{print $2; exit}')
    case "$minos" in 10.*|11.*|12.0) ;; *) fail "runtime minimum macOS exceeds 12.0 or is unreadable: $macho ($minos)" ;; esac
    dependencies=$(/usr/bin/otool -L "$macho" | /usr/bin/sed -n '2,$ { s/^[[:space:]]*//; s/[[:space:]](compatibility.*$//; p; }')
    while IFS= read -r dependency; do
        [ -z "$dependency" ] || case "$dependency" in /System/Library/*|/usr/lib/*) ;; *) fail "unsafe runtime dependency: $macho -> $dependency" ;; esac
    done <<EOF
$dependencies
EOF
done
[ "$(/usr/bin/head -n 1 "$STAGE/wg-quick")" = '#!/usr/bin/env bash' ] || fail 'unexpected wg-quick interpreter'
/usr/bin/grep -q wireguard-go "$STAGE/wg-quick" || fail 'wg-quick lacks the userspace implementation chain'
[ "$(env -i PATH="$STAGE:/usr/bin:/bin:/usr/sbin:/sbin" "$STAGE/bash" --noprofile --norc -c 'printf "%s.%s.%s" "${BASH_VERSINFO[0]}" "${BASH_VERSINFO[1]}" "${BASH_VERSINFO[2]}"')" = "$BASH_VERSION" ] || fail 'built Bash version mismatch'

/bin/cp -X "$BASH_SRC/COPYING" "$STAGE/provenance/licenses/bash-GPL-3.0.txt"
/bin/cp -X "$TOOLS_SRC/COPYING" "$STAGE/provenance/licenses/wireguard-tools-GPL-2.0.txt"
/bin/cp -X "$GO_SRC/LICENSE" "$STAGE/provenance/licenses/wireguard-go-MIT.txt"
for component in bash wg wg-quick wireguard-go; do
    component_sha=$(sha256 "$STAGE/$component")
    case "$component" in
        bash) package_name=GNU-Bash; package_version=$BASH_VERSION; license='GPL-3.0-or-later'; source="$BASH_URL"; source_ref="$BASH_SHA" ;;
        wg|wg-quick) package_name=wireguard-tools; package_version=$TOOLS_VERSION; license='GPL-2.0-only'; source="$TOOLS_REPOSITORY"; source_ref="$TOOLS_COMMIT" ;;
        wireguard-go) package_name=wireguard-go; package_version=$GO_VERSION; license=MIT; source="$GO_REPOSITORY"; source_ref="$GO_COMMIT" ;;
    esac
    /bin/cat > "$STAGE/provenance/$component.spdx.json" <<EOF
{"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","name":"WireZTNA-runtime-$component","documentNamespace":"https://wireztna.com/spdx/runtime/$component/$component_sha","creationInfo":{"created":"1970-01-01T00:00:00Z","creators":["Tool: WireZTNA-build-runtime"]},"packages":[{"name":"$package_name","SPDXID":"SPDXRef-Package","versionInfo":"$package_version","downloadLocation":"$source","licenseConcluded":"$license","checksums":[{"algorithm":"SHA256","checksumValue":"$component_sha"}],"externalRefs":[{"referenceCategory":"OTHER","referenceType":"wireztna-source-ref","referenceLocator":"$source_ref"}]}]}
EOF
done
BASH_OUT_SHA=$(sha256 "$STAGE/bash")
WG_OUT_SHA=$(sha256 "$STAGE/wg")
WG_QUICK_OUT_SHA=$(sha256 "$STAGE/wg-quick")
GO_OUT_SHA=$(sha256 "$STAGE/wireguard-go")
/bin/cat > "$STAGE/provenance/runtime-manifest.json" <<EOF
{"schema_version":1,"architecture":"arm64","minimum_macos":"12.0","components":{"bash":{"version":"$BASH_VERSION","sha256":"$BASH_OUT_SHA"},"wg":{"version":"$TOOLS_VERSION","sha256":"$WG_OUT_SHA"},"wg-quick":{"version":"$TOOLS_VERSION","sha256":"$WG_QUICK_OUT_SHA"},"wireguard-go":{"version":"$GO_VERSION","sha256":"$GO_OUT_SHA"}}}
EOF
[ "$(/usr/bin/plutil -extract schema_version raw -o - "$STAGE/provenance/runtime-manifest.json" 2>/dev/null)" = 1 ] || fail 'generated runtime manifest is invalid'
/bin/chmod -R go-w "$STAGE"
/bin/mv "$STAGE" "$OUTPUT"
trap - EXIT HUP INT TERM
/bin/rm -rf "$WORK"
printf 'Pinned WireZTNA runtime: %s\n' "$OUTPUT"
printf '%s\n' 'Not installed; no privileged command or network mutation was executed.'

#!/bin/sh
# Assemble the canonical WireZTNA MSI from an already-built Windows payload.
set -eu
umask 077

usage() {
    echo "usage: $0 VERSION BIN_DIR OUTPUT_MSI" >&2
    exit 64
}

[ "$#" -eq 3 ] || usage
version=$1
bin_dir=$2
output=$3

old_ifs=$IFS
IFS=.
set -- $version
IFS=$old_ifs
[ "$#" -eq 3 ] || { echo "VERSION must be canonical x.y.z" >&2; exit 64; }
major=$1
minor=$2
build=$3
for part in "$major" "$minor" "$build"; do
    case "$part" in
        ""|*[!0-9]*) echo "VERSION must be canonical x.y.z" >&2; exit 64 ;;
    esac
    case "$part" in
        0|[1-9]*) ;;
        *) echo "VERSION must be canonical x.y.z" >&2; exit 64 ;;
    esac
done
[ "$major" -le 255 ] && [ "$minor" -le 255 ] && [ "$build" -le 65535 ] || {
    echo "VERSION exceeds Windows Installer ProductVersion bounds" >&2
    exit 64
}

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
manifest="$script_dir/wireztna-wixl.wxs"
expected_name="wireztna-$version-windows-amd64.msi"
[ "$(basename -- "$output")" = "$expected_name" ] || {
    echo "OUTPUT_MSI basename must be $expected_name" >&2
    exit 64
}
output_dir=$(dirname -- "$output")
[ -d "$output_dir" ] && [ ! -L "$output_dir" ] || {
    echo "output directory must exist and must not be a symbolic link: $output_dir" >&2
    exit 66
}
[ ! -e "$output" ] && [ ! -L "$output" ] || {
    echo "refusing to leave or replace a pre-existing releaseable MSI: $output" >&2
    exit 65
}
command -v python3 >/dev/null 2>&1 || { echo "python3 is required" >&2; exit 69; }

# Fail closed before payload reads or wixl.  There is deliberately no flag,
# environment variable, or boolean override: an authenticated external IPC v2
# readiness receipt/trust contract has not been integrated yet.
blocked_report="$output.host-blocked.json"
python3 "$script_dir/windows_readiness.py" \
    --version "$version" \
    --report-out "$blocked_report"
echo "MSI assembly blocked by windows.ipc-v2.peer-auth; see $blocked_report" >&2
exit 78

# Future authenticated-readiness integration resumes here.  Keep the source
# contract fail-safe now so enabling readiness cannot reintroduce input TOCTOU.
[ -d "$bin_dir" ] || { echo "BIN_DIR is not a directory: $bin_dir" >&2; exit 66; }
[ ! -L "$bin_dir" ] || { echo "BIN_DIR must not be a symbolic link" >&2; exit 66; }
command -v wixl >/dev/null 2>&1 || { echo "wixl is required" >&2; exit 69; }

work=$(mktemp -d "$output_dir/.wireztna-msi.XXXXXXXX") || {
    echo "cannot create exclusive MSI work directory" >&2
    exit 73
}
cleanup() {
    rm -rf -- "$work"
}
trap cleanup EXIT HUP INT TERM
snapshot="$work/payload"
mkdir -m 700 -- "$snapshot"
python3 "$script_dir/snapshot-msi-payload.py" \
    "$bin_dir" "$snapshot" \
    --manifest-out "$work/payload-snapshot.json"
temporary_msi="$work/$expected_name"

# wixl sees only immutable private snapshots, never the live five payload paths.
wixl --arch x64 \
    -D "ProductVersion=$version" \
    -D "BinDir=$snapshot" \
    -o "$temporary_msi" \
    "$manifest"
[ -s "$temporary_msi" ] || { echo "wixl did not produce a non-empty MSI" >&2; exit 70; }

# Publish without clobbering: hard-link creation is atomic and fails if another
# writer created OUTPUT_MSI after the initial check.
ln "$temporary_msi" "$output" || {
    echo "could not publish MSI atomically without replacing an existing path" >&2
    exit 73
}
rm -f -- "$temporary_msi"
trap - EXIT HUP INT TERM
cleanup
printf '%s\n' "$output"

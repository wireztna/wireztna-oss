#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
command -v python3 >/dev/null 2>&1 || {
  printf 'build-deb.sh: required command not found: python3\n' >&2
  exit 1
}
exec python3 "$script_dir/build_deb.py" "$@"

#!/bin/bash -p
# Retired legacy entry point. It must never create or install a macOS artifact.

set -euo pipefail
printf '%s\n' 'create-dmg.sh is disabled: the legacy CLI/core installer is outside the UI-only macOS scope.' >&2
printf '%s\n' 'Use tauri-ui-local-preflight.sh and the explicitly local-only Tauri packaging scripts.' >&2
exit 64

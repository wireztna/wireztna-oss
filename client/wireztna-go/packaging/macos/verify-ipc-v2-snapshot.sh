#!/bin/bash -p
# Future compatibility gate. UI-authored drafts can never satisfy this gate.

set -euo pipefail
LC_ALL=C
PATH="/usr/bin:/bin:/usr/sbin:/sbin"
export LC_ALL PATH

SCRIPT_DIR=$(cd "$(/usr/bin/dirname "$0")" && pwd)
EXPECTED="$SCRIPT_DIR/ipc-v2-wire.snapshot.json"
PLUTIL=/usr/bin/plutil

if [ "$#" -ne 1 ]; then
    printf '%s\n' 'usage: verify-ipc-v2-snapshot.sh GO_EXPORTED_CANONICAL_SNAPSHOT.json' >&2
    exit 64
fi

CANDIDATE=$1
[ -f "$CANDIDATE" ] || { printf '%s\n' 'verify-ipc-v2-snapshot: candidate is absent' >&2; exit 2; }
[ -x "$PLUTIL" ] || { printf '%s\n' 'verify-ipc-v2-snapshot: plutil is unavailable' >&2; exit 2; }

candidate_provenance=$($PLUTIL -extract provenance raw -o - "$CANDIDATE" 2>/dev/null || :)
candidate_canonical=$($PLUTIL -extract canonical raw -o - "$CANDIDATE" 2>/dev/null || :)
expected_provenance=$($PLUTIL -extract provenance raw -o - "$EXPECTED" 2>/dev/null || :)
expected_canonical=$($PLUTIL -extract canonical raw -o - "$EXPECTED" 2>/dev/null || :)

if [ "$candidate_provenance" != go_exporter ] || [ "$candidate_canonical" != true ]; then
    printf '%s\n' 'verify-ipc-v2-snapshot: candidate must be canonical output from the future Go exporter' >&2
    exit 2
fi

if [ "$expected_provenance" != go_exporter ] || [ "$expected_canonical" != true ]; then
    printf '%s\n' 'verify-ipc-v2-snapshot: blocked; checked-in schema is ui_draft_pending_go_exporter, not canonical evidence' >&2
    exit 2
fi

if /usr/bin/cmp -s "$EXPECTED" "$CANDIDATE"; then
    printf '%s\n' 'IPC v2 Go-exported snapshot matches exactly; desktop readiness remains unchanged.'
    exit 0
fi

printf '%s\n' 'IPC v2 snapshot drift detected; keep TAURI_DESKTOP_IPC_READINESS=not_implemented.' >&2
exit 2

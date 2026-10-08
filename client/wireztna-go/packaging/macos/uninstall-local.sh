#!/bin/bash -p
# Remove the WireZTNA macOS payload and owned service state. User config is preserved.

set -euo pipefail
LC_ALL=C
PATH=/usr/bin:/bin:/usr/sbin:/sbin
export LC_ALL PATH

[ "$#" -eq 0 ] || { printf '%s\n' 'usage: uninstall-local.sh' >&2; exit 64; }
[ "$(/usr/bin/id -u)" -eq 0 ] || { printf '%s\n' 'uninstall-local: run from an approved root administration context' >&2; exit 2; }
[ "$(/usr/bin/uname -s)" = Darwin ] && [ "$(/usr/bin/uname -m)" = arm64 ] || { printf '%s\n' 'uninstall-local: darwin/arm64 is required' >&2; exit 2; }

PKG_ID=com.wireztna.desktop.local-pilot
LABEL=com.wireztna.desktop-service
APP=/Applications/WireZTNA.app
SUPPORT='/Library/Application Support/WireZTNA'
HELPER="$SUPPORT/bin/wireztna"
RUNTIME_DIR="$SUPPORT/runtime"
WG_DIR="$SUPPORT/wireguard"
PLIST=/Library/LaunchDaemons/com.wireztna.desktop-service.plist
SOCKET=/var/run/wireztna/desktop-v2.sock
LOCK="$SOCKET.lock"
APPLIED=
INTENT=
WG_CONFIG="$WG_DIR/wg-wireztna.conf"
ROUTE_MARKER="$WG_DIR/wg-wireztna.broker-route.json"
TXN_DIR=/private/var/db/wireztna-desktop-uninstall-rollback
RESTORE_ON_FAILURE=false
DAEMON_WAS_ACTIVE=false

fail() { printf 'uninstall-local: %s\n' "$*" >&2; exit 2; }

# launchctl_print_state returns 0 only when present, 1 only for the exact
# macOS service-not-found contract, and 2 for every ambiguous observation.
launchctl_print_state() {
    local launchctl_bin=$1 label=$2 stderr rc expected
    if stderr=$("$launchctl_bin" print "system/$label" 2>&1 >/dev/null); then return 0; else rc=$?; fi
    expected=$(printf 'Bad request.\nCould not find service "%s" in domain for system' "$label")
    if [ "$rc" -eq 113 ] && [ "$stderr" = "$expected" ]; then return 1; fi
    printf 'uninstall-local: ambiguous launchctl print result: status=%s stderr=%q\n' "$rc" "$stderr" >&2
    return 2
}
# End launchctl_print_state.

restore_staged_path() {
    local name=$1 destination=$2 staged="$TXN_DIR/$name"
    [ -e "$staged" ] || return 0
    [ ! -e "$destination" ] && [ ! -L "$destination" ] || return 1
    /bin/mv "$staged" "$destination"
}

restore_previous_daemon() {
    local status=$1 restored=true rc remaining
    trap - EXIT HUP INT TERM
    if [ "$status" -ne 0 ] && [ "$RESTORE_ON_FAILURE" = true ]; then
        restore_staged_path plist "$PLIST" || restored=false
        restore_staged_path app "$APP" || restored=false
        restore_staged_path helper "$HELPER" || restored=false
        restore_staged_path runtime "$RUNTIME_DIR" || restored=false
        restore_staged_path lock "$LOCK" || restored=false
        restore_staged_path applied "$APPLIED" || restored=false
        restore_staged_path intent "$INTENT" || restored=false
        restore_staged_path wg-config "$WG_CONFIG" || restored=false
        restore_staged_path route-marker "$ROUTE_MARKER" || restored=false
        if [ "$restored" = false ]; then
            printf '%s\n' 'uninstall-local: failed to restore staged payload; rollback evidence was preserved' >&2
        elif [ "$DAEMON_WAS_ACTIVE" = true ]; then
            if launchctl_print_state /bin/launchctl "$LABEL"; then :; else
                rc=$?
                if [ "$rc" -eq 1 ]; then /bin/launchctl bootstrap system "$PLIST" || restored=false; else restored=false; fi
            fi
            [ "$restored" = false ] || /bin/launchctl kickstart "system/$LABEL" || restored=false
            remaining=30
            while [ "$restored" = true ]; do
                if launchctl_print_state /bin/launchctl "$LABEL"; then break; else rc=$?; fi
                if [ "$rc" -ne 1 ] || [ "$remaining" -le 0 ]; then restored=false; break; fi
                /bin/sleep 1 || { restored=false; break; }
                remaining=$((remaining - 1))
            done
            [ "$restored" = true ] || printf '%s\n' 'uninstall-local: could not prove daemon restoration; rollback evidence was preserved' >&2
        fi
        if [ "$restored" = true ]; then /bin/rm -rf "$TXN_DIR" || printf '%s\n' 'uninstall-local: daemon restored but rollback directory could not be removed' >&2; fi
    fi
    exit "$status"
}
handle_exit() { local status=$?; restore_previous_daemon "$status"; }
handle_signal() { restore_previous_daemon "$1"; }
trap handle_exit EXIT
trap 'handle_signal 129' HUP
trap 'handle_signal 130' INT
trap 'handle_signal 143' TERM

[ -f "$PLIST" ] && [ ! -L "$PLIST" ] || fail 'trusted installed plist is required'
[ "$(/usr/bin/stat -f '%u:%g:%Lp' "$PLIST")" = 0:0:644 ] || fail 'plist metadata is unsafe'
/usr/bin/plutil -lint "$PLIST" >/dev/null || fail 'plist is invalid'
[ "$(/usr/bin/plutil -extract Label raw -o - "$PLIST")" = "$LABEL" ] || fail 'plist label mismatch'
[ "$(/usr/bin/plutil -extract ProgramArguments.0 raw -o - "$PLIST")" = "$HELPER" ] || fail 'plist helper mismatch'
[ "$(/usr/bin/plutil -extract ProgramArguments.6 raw -o - "$PLIST")" = --socket-path ] || fail 'plist socket flag mismatch'
[ "$(/usr/bin/plutil -extract ProgramArguments.7 raw -o - "$PLIST")" = "$SOCKET" ] || fail 'plist socket path mismatch'
OWNER_UID=$(/usr/bin/plutil -extract ProgramArguments.3 raw -o - "$PLIST")
CONFIG_DIR=$(/usr/bin/plutil -extract ProgramArguments.5 raw -o - "$PLIST")
case "$OWNER_UID" in ''|0|*[!0-9]*) fail 'invalid owner UID' ;; esac
[ -f "$HELPER" ] && [ ! -L "$HELPER" ] && [ "$(/usr/bin/stat -f '%u:%g:%Lp' "$HELPER")" = 0:0:755 ] || fail 'helper metadata mismatch'
[ -d "$SUPPORT/bin" ] && [ ! -L "$SUPPORT/bin" ] || fail 'helper parent directory is unsafe'
[ -d "$RUNTIME_DIR" ] && [ ! -L "$RUNTIME_DIR" ] && [ "$(/usr/bin/stat -f '%u:%g' "$RUNTIME_DIR")" = 0:0 ] || fail 'runtime directory is unsafe'
[ -z "$(/usr/bin/find "$RUNTIME_DIR" -type l -print -quit)" ] || fail 'runtime contains an unsafe symlink'
[ -d "$WG_DIR" ] && [ ! -L "$WG_DIR" ] || fail 'WireGuard state directory is unsafe'
[ ! -e "$TXN_DIR" ] && [ ! -L "$TXN_DIR" ] || fail 'previous uninstall rollback evidence exists; recover or remove it explicitly before retrying'

APPLIED="/var/db/wireztna/desktop/$OWNER_UID/desktop-service-applied.json"
INTENT="/var/db/wireztna/desktop/$OWNER_UID/desktop-service-intent.json"
validate_private_file() {
    local path=$1 expected_uid=$2
    if [ -e "$path" ] || [ -L "$path" ]; then
        [ -f "$path" ] && [ ! -L "$path" ] || fail "unsafe exact file $path"
        [ "$(/usr/bin/stat -f '%u:%Lp' "$path")" = "$expected_uid:600" ] || fail "metadata mismatch at $path"
    fi
}
validate_private_file "$LOCK" 0
validate_private_file "$APPLIED" 0
validate_private_file "$INTENT" 0
validate_private_file "$WG_CONFIG" 0
validate_private_file "$ROUTE_MARKER" 0
if [ -e "$SOCKET" ] || [ -L "$SOCKET" ]; then
    [ -S "$SOCKET" ] && [ ! -L "$SOCKET" ] || fail 'unsafe IPC path'
    [ "$(/usr/bin/stat -f '%u:%Lp' "$SOCKET")" = "$OWNER_UID:600" ] || fail 'IPC metadata mismatch'
fi
if [ -e "$APP" ] || [ -L "$APP" ]; then [ -d "$APP" ] && [ ! -L "$APP" ] && [ "$(/usr/bin/stat -f '%u:%g' "$APP")" = 0:0 ] || fail 'app metadata mismatch'; fi

/bin/mkdir -m 0700 "$TXN_DIR" || fail 'could not create uninstall rollback transaction'
RESTORE_ON_FAILURE=true
remaining=30
if launchctl_print_state /bin/launchctl "$LABEL"; then
    DAEMON_WAS_ACTIVE=true
    /bin/launchctl bootout "system/$LABEL" || fail 'could not stop the exact daemon'
else
    rc=$?; [ "$rc" -eq 1 ] || fail 'could not classify daemon state'
fi
while :; do
    if launchctl_print_state /bin/launchctl "$LABEL"; then
        [ "$remaining" -gt 0 ] || fail 'daemon remained loaded after 30s'
        /bin/sleep 1 || fail 'sleep failed while waiting for daemon removal'
        remaining=$((remaining - 1))
    else
        rc=$?; [ "$rc" -eq 1 ] || fail 'could not prove daemon absence'; break
    fi
done
"$HELPER" desktop-verify-disconnected --owner-uid "$OWNER_UID" --config-dir "$CONFIG_DIR" || fail 'owned network state remains; recovery evidence was preserved'

if [ -e "$SOCKET" ]; then /bin/rm "$SOCKET"; fi
stage_path() { local source=$1 name=$2; if [ -e "$source" ]; then /bin/mv "$source" "$TXN_DIR/$name"; fi; }
stage_path "$LOCK" lock
stage_path "$APPLIED" applied
stage_path "$INTENT" intent
stage_path "$WG_CONFIG" wg-config
stage_path "$ROUTE_MARKER" route-marker
stage_path "$HELPER" helper
stage_path "$RUNTIME_DIR" runtime
stage_path "$APP" app
stage_path "$PLIST" plist

# All owned artifacts are absent while exact originals remain reversible.
RESTORE_ON_FAILURE=false
trap - EXIT HUP INT TERM
/bin/rm -rf "$TXN_DIR" || printf '%s\n' 'uninstall-local: payload removed; stale committed rollback directory must be removed manually' >&2
if /usr/sbin/pkgutil --pkg-info "$PKG_ID" >/dev/null 2>&1; then
    /usr/sbin/pkgutil --forget "$PKG_ID" >/dev/null || printf 'uninstall-local: payload removed; stale receipt remains for %s\n' "$PKG_ID" >&2
fi
/bin/rmdir "$SUPPORT/bin" 2>/dev/null || :
/bin/rmdir "$WG_DIR" 2>/dev/null || :
/bin/rmdir "/var/db/wireztna/desktop/$OWNER_UID" 2>/dev/null || :
/bin/rmdir /var/db/wireztna/desktop 2>/dev/null || :
/bin/rmdir /var/db/wireztna 2>/dev/null || :
/bin/rmdir /var/run/wireztna 2>/dev/null || :
/bin/rmdir "$SUPPORT" 2>/dev/null || :
printf 'Removed WireZTNA macOS payload. Preserved owner config: %s\n' "$CONFIG_DIR"

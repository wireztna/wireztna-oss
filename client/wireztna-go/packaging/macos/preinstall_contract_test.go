package macos

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readScript(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestPreinstallPersistsCompleteRollbackBeforeStoppingPreviousDaemon(t *testing.T) {
	script := readScript(t, "pkg-scripts/preinstall")
	ordered := []string{
		`/bin/mkdir -m 0700 "$TXN_DIR"`,
		`/usr/bin/ditto --norsrc --noextattr --noqtn "$APP" "$PREVIOUS_APP"`,
		`/bin/cp -p "$HELPER" "$PREVIOUS_HELPER"`,
		`/usr/bin/ditto --norsrc --noextattr --noqtn "$RUNTIME_DIR" "$PREVIOUS_RUNTIME"`,
		`/bin/cp -p "$PLIST" "$PREVIOUS_PLIST"`,
		`/bin/launchctl bootout "system/$LABEL"`,
		`"$HELPER" desktop-verify-disconnected`,
	}
	previous := -1
	for _, contract := range ordered {
		position := strings.Index(script, contract)
		if position < 0 || position <= previous {
			t.Fatalf("preinstall transaction order is missing or invalid at %q", contract)
		}
		previous = position
	}
	for _, required := range []string{
		"existing app, helper, runtime, and plist must all be present",
		"root_owned_tree_safe \"$PREVIOUS_RUNTIME\"",
		"RESTORE_ON_FAILURE=true",
		"DAEMON_WAS_ACTIVE=true",
		`printf '1\n' > "$WAS_ACTIVE"`,
		`trap 'handle_signal 129' HUP`,
		`/bin/launchctl bootstrap system "$PLIST"`,
		`/bin/launchctl kickstart "system/$LABEL"`,
		"legacy IPC socket is present",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("preinstall is missing contract %q", required)
		}
	}
}

func TestLaunchctlPrintStateClassifiesOnlyExactAbsence(t *testing.T) {
	script := readScript(t, "pkg-scripts/preinstall")
	start := strings.Index(script, "launchctl_print_state() {")
	end := strings.Index(script[start:], "\n# End launchctl_print_state.")
	if start < 0 || end < 0 {
		t.Fatal("launchctl classifier markers are missing")
	}
	classifier := script[start : start+end]
	fakePath := filepath.Join(t.TempDir(), "launchctl")
	fake := `#!/bin/bash
printf '%s' "${FAKE_STDOUT-}"
printf '%s' "${FAKE_STDERR-}" >&2
exit "${FAKE_STATUS-0}"
`
	if err := os.WriteFile(fakePath, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	const label = "com.wireztna.desktop-service"
	exactAbsent := "Bad request.\nCould not find service \"" + label + "\" in domain for system"
	cases := []struct {
		name, status, stderr string
		want                 int
	}{
		{name: "present", status: "0", stderr: "ignored diagnostic", want: 0},
		{name: "exact absent", status: "113", stderr: exactAbsent, want: 1},
		{name: "wrong status", status: "5", stderr: exactAbsent, want: 2},
		{name: "empty error", status: "113", want: 2},
		{name: "wrong label", status: "113", stderr: strings.ReplaceAll(exactAbsent, label, "foreign.service"), want: 2},
		{name: "extra diagnostic", status: "113", stderr: exactAbsent + "\nextra", want: 2},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("/bin/bash", "-c", classifier+"\n"+`launchctl_print_state "$1" "$2"`, "classifier", fakePath, label)
			command.Env = append(os.Environ(), "FAKE_STATUS="+test.status, "FAKE_STDERR="+test.stderr)
			runErr := command.Run()
			got := 0
			if runErr != nil {
				var exitError *exec.ExitError
				if !errors.As(runErr, &exitError) {
					t.Fatalf("classifier execution error = %v", runErr)
				}
				got = exitError.ExitCode()
			}
			if got != test.want {
				t.Fatalf("classifier exit = %d, want %d", got, test.want)
			}
		})
	}
}

func TestInstallerSignalTrapExitsNonZero(t *testing.T) {
	for _, scriptPath := range []string{"pkg-scripts/preinstall", "pkg-scripts/postinstall", "uninstall-local.sh"} {
		t.Run(filepath.Base(scriptPath), func(t *testing.T) {
			script := readScript(t, scriptPath)
			if !strings.Contains(script, `trap 'handle_signal 129' HUP`) || !strings.Contains(script, `handle_signal() {`) {
				t.Fatal("script does not use an explicit non-zero HUP handler")
			}
			command := exec.Command("/bin/bash", "-c", `handle_signal() { exit "$1"; }; trap 'handle_signal 129' HUP; kill -HUP $$; /bin/sleep 1`)
			err := command.Run()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 129 {
				t.Fatalf("HUP exit = %v, want status 129", err)
			}
		})
	}
}

func TestPostinstallMaterializesPathStateAndRestoresRuntime(t *testing.T) {
	script := readScript(t, "pkg-scripts/postinstall")
	for _, required := range []string{
		`PLIST_TEMPLATE="$SCRIPT_DIR/com.wireztna.desktop-service.plist.in"`,
		`/usr/bin/plutil -replace ProgramArguments.3 -string "$OWNER_UID"`,
		`/usr/bin/plutil -replace ProgramArguments.5 -string "$CONFIG_DIR"`,
		`/usr/bin/plutil -replace KeepAlive -json "$keepalive_json"`,
		`keepalive_json="{\"PathState\":{\"$CONFIG_FILE\":true}}"`,
		`OWNER_UID=$(/usr/bin/stat -f '%u' /dev/console)`,
		`CONFIG_DIR="$HOME_DIR/.wireztna"`,
		`elif [ "$UPGRADE_ROLLBACK" = true ]`,
		`root_owned_tree_safe "$PREVIOUS_RUNTIME"`,
		`/usr/bin/ditto --norsrc --noextattr --noqtn "$PREVIOUS_RUNTIME" "$RUNTIME_DIR"`,
		`[ "$HAS_CONFIG" = true ]`,
		`/bin/launchctl bootstrap system "$PLIST"`,
		`/bin/launchctl kickstart "system/$LABEL"`,
		"installation committed; stale rollback evidence",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("postinstall is missing contract %q", required)
		}
	}
	materialize := strings.LastIndex(script, "materialize_plist")
	bootstrap := strings.LastIndex(script, `/bin/launchctl bootstrap system "$PLIST"`)
	disarm := strings.LastIndex(script, "UPGRADE_ROLLBACK=false")
	cleanup := strings.LastIndex(script, `/bin/rm -rf "$TXN_DIR"`)
	if materialize < 0 || bootstrap < 0 || disarm < 0 || cleanup < 0 || !(materialize < bootstrap && bootstrap < disarm && disarm < cleanup) {
		t.Fatal("postinstall does not preserve rollback through plist materialization and launchd verification")
	}
}

func TestUninstallStagesRuntimeAndForgetsReceiptAfterCommit(t *testing.T) {
	script := readScript(t, "uninstall-local.sh")
	bootout := strings.Index(script, `/bin/launchctl bootout "system/$LABEL"`)
	verify := strings.Index(script, `"$HELPER" desktop-verify-disconnected`)
	stageRuntime := strings.Index(script, `stage_path "$RUNTIME_DIR" runtime`)
	stagePlist := strings.Index(script, `stage_path "$PLIST" plist`)
	commit := strings.LastIndex(script, "RESTORE_ON_FAILURE=false")
	forget := strings.LastIndex(script, `/usr/sbin/pkgutil --forget "$PKG_ID"`)
	if bootout < 0 || verify < 0 || stageRuntime < 0 || stagePlist < 0 || commit < 0 || forget < 0 ||
		!(bootout < verify && verify < stageRuntime && stageRuntime < stagePlist && stagePlist < commit && commit < forget) {
		t.Fatal("uninstall order is not bootout -> verify -> reversible runtime/payload stage -> terminal commit -> receipt forget")
	}
	for _, required := range []string{
		`restore_staged_path runtime "$RUNTIME_DIR"`,
		`restore_staged_path app "$APP"`,
		`restore_staged_path plist "$PLIST"`,
		"Preserved owner config",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("uninstall is missing contract %q", required)
		}
	}
}

func TestGenericPackageUsesPinnedRuntimeAndOneCanonicalVersion(t *testing.T) {
	script := readScript(t, "build-local-pkg.sh")
	for _, forbidden := range []string{"--owner-uid", "--config-dir", "desktop-validate-config"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("generic builder still contains host-specific contract %q", forbidden)
		}
	}
	for _, required := range []string{
		`'^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'`,
		`printf '{"version":"%s"}\n' "$VERSION" > "$TAURI_VERSION_CONFIG"`,
		`MACOSX_DEPLOYMENT_TARGET=12.0`,
		`runtime-manifest.json`,
		`components.$component.sha256`,
		`WireZTNA/runtime/wireguard-go`,
		`--identifier com.wireztna.desktop.local-pilot`,
		`--version "$VERSION"`,
		`wireztna-about-version:`,
		`generate-brand-icons.sh" --check`,
		"CFBundleShortVersionString",
		"CFBundleVersion",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("generic package contract is missing %q", required)
		}
	}
}

func TestRuntimeBuilderPinsSourcesAndRejectsUnsafeDependencies(t *testing.T) {
	script := readScript(t, "build-runtime.sh")
	lock := readScript(t, "runtime-lock.json")
	for _, required := range []string{
		"0d5cd86965f869a26cf64f4b71be7b96f90a3ba8b3d74e27e8e9d9d5550f31ba",
		"49ce333da02056ae7b22ee2aeb6afe8aaed79b19",
		"f333402bd9cbe0f3eeb02507bd14e23d7d639280",
		`"minimum_macos": "12.0"`,
	} {
		if !strings.Contains(lock, required) {
			t.Fatalf("runtime lock is missing %q", required)
		}
	}
	for _, required := range []string{
		"--without-installed-readline",
		"-mmacosx-version-min=12.0",
		`GOMODCACHE="$GO_MODULE_CACHE"`,
		`GOPROXY="$GO_PROXY"`,
		`/System/Library/*|/usr/lib/*`,
		"runtime-manifest.json",
		".spdx.json",
		"codesign --force --sign - --timestamp=none",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("runtime builder is missing %q", required)
		}
	}
}

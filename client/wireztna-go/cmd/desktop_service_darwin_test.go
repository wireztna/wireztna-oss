//go:build darwin

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestDesktopServiceCommandIsSeparateAndRequiresExplicitOwnerPaths(t *testing.T) {
	command := newDesktopServiceCommand()
	if command.Name() != "desktop-service" {
		t.Fatalf("command name = %q", command.Name())
	}
	if command.Flags().Lookup("owner-uid") == nil || command.Flags().Lookup("config-dir") == nil || command.Flags().Lookup("journal-path") == nil || command.Flags().Lookup("socket-path") == nil {
		t.Fatal("desktop-service security flags are incomplete")
	}
	if strings.Contains(strings.ToLower(command.Long), "legacy ipc") && !strings.Contains(strings.ToLower(command.Long), "does not expose") {
		t.Fatal("command description suggests legacy IPC is exposed")
	}
	if err := command.PersistentPreRunE(command, nil); err == nil {
		t.Fatal("command accepted omitted owner/config flags")
	}
}

func TestDesktopServiceFlagsRejectUnsafeJournalDirectory(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("pilot command intentionally rejects non-arm64 Darwin")
	}
	uid := uint64(os.Getuid())
	if uid == 0 {
		t.Skip("owner safety test requires a non-root test user")
	}
	configDir := writeDesktopTestConfig(t)
	command := newDesktopServiceCommand()
	if err := command.Flags().Set("owner-uid", fmt.Sprint(uid)); err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("config-dir", configDir); err != nil {
		t.Fatal(err)
	}
	unsafeJournalDir := t.TempDir()
	if err := os.Chmod(unsafeJournalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("journal-path", filepath.Join(unsafeJournalDir, "journal.json")); err != nil {
		t.Fatal(err)
	}
	if err := command.PersistentPreRunE(command, nil); err == nil {
		t.Fatal("command accepted a journal directory writable outside the service identity")
	}
}

func TestBuildDesktopServiceCompositionDoesNotMutateHost(t *testing.T) {
	uid := uint64(os.Getuid())
	if uid == 0 {
		t.Skip("composition path test requires a non-root test user")
	}
	configDir := writeDesktopTestConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	runtimeService, err := buildDesktopService(ctx, desktopServiceOptions{
		ownerUID: uid, configDir: configDir, journalPath: filepath.Join(configDir, "journal.json"),
	})
	if err != nil {
		cancel()
		wireGuardDirectory := "/Library/Application Support/WireZTNA/wireguard"
		info, statErr := os.Lstat(wireGuardDirectory)
		rootOwned := false
		if statErr == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				rootOwned = stat.Uid == 0
			}
		}
		if errors.Is(err, os.ErrPermission) &&
			strings.Contains(err.Error(), "inspect Darwin broker route marker") &&
			rootOwned && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() == 0o700 {
			t.Skipf("installed root-owned WireGuard state is intentionally unreadable to this non-root composition test: %v", err)
		}
		t.Fatalf("buildDesktopService() error = %v", err)
	}
	if runtimeService.server == nil || runtimeService.controller == nil {
		cancel()
		t.Fatal("composition omitted server or controller")
	}
	snapshot, err := runtimeService.controller.Snapshot(context.Background())
	if err != nil || snapshot.State != "disconnected" {
		cancel()
		t.Fatalf("initial snapshot = %#v, %v", snapshot, err)
	}
	cancel()
	closeContext, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err := runtimeService.controller.Close(closeContext); err != nil {
		t.Fatalf("controller.Close() error = %v", err)
	}
}

func TestDesktopStreamIdentityChangesPerProcessComposition(t *testing.T) {
	first, err := newDesktopStreamIdentity()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newDesktopStreamIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if first.StreamID == second.StreamID || first.Epoch != 1 || second.Epoch != 1 {
		t.Fatalf("stream identities first=%#v second=%#v", first, second)
	}
}

func writeDesktopTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	dir = resolvedDir
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	privateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	brokerKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf(`api_url: https://control.example
private_key: %s
overlay_ip: 10.200.1.2/32
broker_public_key: %s
broker_endpoint: broker.example:51820
broker_overlay_ip: 10.200.0.1
allowed_ips:
  - 10.20.0.0/16
tunnel_dns: 10.200.0.1
interface: wg-wireztna
`, privateKey.String(), brokerKey.String())
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

//go:build darwin

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDesktopValidateConfigIsReadOnlyAndRequiresRealEnrollment(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("pilot validator intentionally rejects non-arm64 Darwin")
	}
	uid := uint64(os.Getuid())
	if uid == 0 {
		t.Skip("config ownership test requires a non-root test user")
	}
	configDir := writeDesktopTestConfig(t)
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.Chmod(configPath, 0o400); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	command := newDesktopValidateConfigCommand()
	if !command.Hidden {
		t.Fatal("desktop config validator must remain internal")
	}
	if err := command.Flags().Set("owner-uid", fmt.Sprint(uid)); err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("config-dir", configDir); err != nil {
		t.Fatal(err)
	}
	if err := command.PersistentPreRunE(command, nil); err != nil {
		t.Fatal(err)
	}
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || info.Mode().Perm() != 0o400 {
		t.Fatalf("validator mutated config: mode=%#o contentChanged=%v", info.Mode().Perm(), string(after) != string(before))
	}

	invalidDir := writeDesktopTestConfig(t)
	invalidPath := filepath.Join(invalidDir, "config.yaml")
	invalid, err := os.ReadFile(invalidPath)
	if err != nil {
		t.Fatal(err)
	}
	invalid = []byte(strings.ReplaceAll(string(invalid), "broker_overlay_ip: 10.200.0.1\n", ""))
	if err := os.WriteFile(invalidPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateDesktopConfig(desktopValidateConfigOptions{ownerUID: uid, configDir: invalidDir}); err == nil {
		t.Fatal("validator accepted config without broker_overlay_ip")
	}
}

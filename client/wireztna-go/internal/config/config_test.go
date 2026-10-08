//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncFileToDirCreatesPrivateFile(t *testing.T) {
	dir := t.TempDir()
	syncFileToDir(dir, "token", []byte("secret"))

	path := filepath.Join(dir, "token")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat synced token: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("token permissions = %04o, want 0600", got)
	}
}

func TestSyncFileToDirTightensExistingPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("same"), 0644); err != nil {
		t.Fatalf("create existing config: %v", err)
	}

	syncFileToDir(dir, "config.yaml", []byte("same"))

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat synced config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("config permissions = %04o, want 0600", got)
	}
}

func TestHardenLegacyPrivateFilesBeforeRead(t *testing.T) {
	dir := t.TempDir()
	for _, filename := range []string{"config.yaml", "token"} {
		if err := os.WriteFile(filepath.Join(dir, filename), []byte("secret"), 0644); err != nil {
			t.Fatalf("create legacy %s: %v", filename, err)
		}
	}

	if err := hardenLegacyPrivateFiles(dir); err != nil {
		t.Fatalf("harden legacy files: %v", err)
	}

	for _, filename := range []string{"config.yaml", "token"} {
		info, err := os.Stat(filepath.Join(dir, filename))
		if err != nil {
			t.Fatalf("stat hardened %s: %v", filename, err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Fatalf("%s permissions = %04o, want 0600", filename, got)
		}
	}
}

func TestHardenLegacyPrivateFilesRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("secret"), 0644); err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "token")); err != nil {
		t.Fatalf("create token symlink: %v", err)
	}

	if err := hardenLegacyPrivateFiles(dir); err == nil {
		t.Fatal("hardenLegacyPrivateFiles accepted a token symlink")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat symlink target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Fatalf("symlink target permissions = %04o, want unchanged 0644", got)
	}
}

func TestSyncFileToDirRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "token")); err != nil {
		t.Fatalf("create token symlink: %v", err)
	}

	syncFileToDir(dir, "token", []byte("replacement"))

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if got := string(data); got != "original" {
		t.Fatalf("symlink target contents = %q, want unchanged", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat symlink target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Fatalf("symlink target permissions = %04o, want unchanged 0644", got)
	}
}

func TestReadConfigFileRejectsSymlink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "real-config.yaml")
	if err := os.WriteFile(target, []byte("private_key: secret\n"), 0644); err != nil {
		t.Fatalf("create config target: %v", err)
	}
	link := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create config symlink: %v", err)
	}

	if err := ReadConfigFile(link); err == nil {
		t.Fatal("ReadConfigFile accepted a config symlink")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat config target: %v", err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Fatalf("config target permissions = %04o, want unchanged 0644", got)
	}
}

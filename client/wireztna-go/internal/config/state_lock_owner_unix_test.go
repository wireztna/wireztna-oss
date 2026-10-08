//go:build !windows

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOwnerRemoteMutationLockRejectsUnsafeObjects(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, dir, lockPath string)
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, dir, lockPath string) {
				t.Helper()
				target := filepath.Join(dir, "target")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, lockPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlink",
			setup: func(t *testing.T, dir, lockPath string) {
				t.Helper()
				target := filepath.Join(dir, "target")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(target, lockPath); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(dir, "remote-mutation.lock")
			test.setup(t, dir, lockPath)
			if release, err := AcquireRemoteMutationForOwner(context.Background(), dir, uint32(os.Getuid())); err == nil {
				_ = release()
				t.Fatalf("unsafe %s lock was accepted", test.name)
			}
		})
	}
}

func TestOwnerRemoteMutationLockRejectsUnsafeDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if release, err := AcquireRemoteMutationForOwner(context.Background(), dir, uint32(os.Getuid())); err == nil {
		_ = release()
		t.Fatal("group-readable owner directory was accepted")
	}
}

func TestDefaultAndExplicitOwnerRemoteMutationLocksSerialize(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUDO_USER", "")
	configDir := filepath.Join(home, ".wireztna")

	release, err := AcquireRemoteMutation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	secondRelease, err := AcquireRemoteMutationForOwner(ctx, configDir, uint32(os.Getuid()))
	if secondRelease != nil {
		_ = secondRelease()
	}
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contending explicit lock error = %v, want deadline exceeded", err)
	}
}

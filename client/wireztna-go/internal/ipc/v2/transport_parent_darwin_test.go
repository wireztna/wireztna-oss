package v2

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

const darwinUmaskHelperEnvironment = "WIREZTNA_DARWIN_UMASK_HELPER"

func TestEnsureDarwinParentNeutralizesRestrictiveUmask(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "wireztna")
	command := exec.Command(os.Args[0], "-test.run=^TestEnsureDarwinParentUmaskHelper$")
	command.Env = append(os.Environ(), darwinUmaskHelperEnvironment+"=1", "WIREZTNA_DARWIN_UMASK_PARENT="+parent)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("umask helper failed: %v\n%s", err, output)
	}
}

func TestEnsureDarwinParentUmaskHelper(t *testing.T) {
	if os.Getenv(darwinUmaskHelperEnvironment) != "1" {
		return
	}
	parent := os.Getenv("WIREZTNA_DARWIN_UMASK_PARENT")
	oldUmask := unix.Umask(0o077)
	defer unix.Umask(oldUmask)
	if err := ensureDarwinParent(parent); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != darwinParentMode {
		t.Fatalf("created parent mode = %v, want directory %04o", info.Mode(), darwinParentMode)
	}
}

func TestEnsureDarwinParentRejectsUnsafePreexistingDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "wireztna")
	if err := os.Mkdir(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := ensureDarwinParent(parent); err == nil {
		t.Fatal("unsafe pre-existing parent was repaired instead of rejected")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("unsafe parent was mutated to mode %04o", info.Mode().Perm())
	}
}

func TestServeDarwinRejectsNonProductionSocketPath(t *testing.T) {
	err := ServeDarwin(context.Background(), &Server{}, DarwinTransportConfig{
		SocketPath: filepath.Join(t.TempDir(), "desktop-v2.sock"),
		OwnerUID:   uint64(os.Geteuid()),
	})
	if err == nil {
		t.Fatal("ServeDarwin accepted a non-production socket path")
	}
}

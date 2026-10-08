//go:build darwin

package darwin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiscardVerifiedAtPreservesRacedReplacement(t *testing.T) {
	directory := t.TempDir()
	name := ".wireztna-quarantine-test"
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dirFD)
	expected, err := readResolverObjectAt(dirFD, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = discardVerifiedAt(dirFD, name, expected, func(resolverObject) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "changed quarantine") {
		t.Fatalf("discard error = %v, want changed quarantine", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "replacement" {
		t.Fatalf("raced replacement changed to %q", content)
	}
}

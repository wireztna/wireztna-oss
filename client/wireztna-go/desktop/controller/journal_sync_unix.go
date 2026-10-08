//go:build !windows

package controller

import (
	"os"
	"path/filepath"
)

func syncJournalDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

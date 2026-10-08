//go:build windows

// Package wintun embeds wintun.dll and extracts it at runtime if needed.
// This ensures the client works as a single .exe download without requiring
// the user to manually place wintun.dll alongside the binary.
//
// wintun.dll is MIT-licensed. See LICENSE.txt in this directory.
package wintun

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

//go:embed wintun.dll
var wintunDLL []byte

var extractOnce sync.Once
var extractErr error

// EnsureAvailable extracts wintun.dll next to the running executable if it
// doesn't already exist, then sets the DLL search directory so the TUN
// library can find it.
func EnsureAvailable() error {
	extractOnce.Do(func() {
		exePath, err := os.Executable()
		if err != nil {
			extractErr = fmt.Errorf("cannot find executable path: %w", err)
			return
		}
		dir := filepath.Dir(exePath)
		dllPath := filepath.Join(dir, "wintun.dll")

		// If already exists and same size, skip
		if info, err := os.Stat(dllPath); err == nil && info.Size() == int64(len(wintunDLL)) {
			windows.SetDllDirectory(dir)
			return
		}

		// Try to write next to the exe (requires write permission to that dir)
		if err := os.WriteFile(dllPath, wintunDLL, 0644); err == nil {
			windows.SetDllDirectory(dir)
			return
		}

		// Fallback: write to user's temp directory
		tmpDir := os.TempDir()
		dllPath = filepath.Join(tmpDir, "wintun.dll")
		if info, err := os.Stat(dllPath); err == nil && info.Size() == int64(len(wintunDLL)) {
			windows.SetDllDirectory(tmpDir)
			return
		}
		if err := os.WriteFile(dllPath, wintunDLL, 0644); err != nil {
			extractErr = fmt.Errorf("cannot write wintun.dll: %w", err)
			return
		}
		windows.SetDllDirectory(tmpDir)
	})
	return extractErr
}

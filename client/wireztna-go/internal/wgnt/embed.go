//go:build windows

// Package wgnt embeds wireguard.dll (WireGuard NT kernel driver) and provides
// Go bindings to its API via syscall (no CGO required).
//
// wireguard.dll is the official prebuilt binary from download.wireguard.com,
// licensed under the permissive prebuilt binaries license (see LICENSE-wireguard-nt.txt).
//
// API reference: https://git.zx2c4.com/wireguard-nt/about/
package wgnt

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

//go:embed wireguard.dll
var wireguardDLL []byte

var (
	initOnce sync.Once
	initErr  error
	dll      *windows.DLL

	// Function pointers resolved from the DLL
	procCreateAdapter         *windows.Proc
	procOpenAdapter           *windows.Proc
	procCloseAdapter          *windows.Proc
	procGetAdapterLUID        *windows.Proc
	procGetRunningDriverVer   *windows.Proc
	procSetLogger             *windows.Proc
	procSetAdapterLogging     *windows.Proc
	procSetAdapterState       *windows.Proc
	procGetAdapterState       *windows.Proc
	procSetConfiguration      *windows.Proc
	procGetConfiguration      *windows.Proc
	procDeleteDriver          *windows.Proc
)

// Initialize extracts wireguard.dll and resolves all function pointers.
// Safe to call multiple times — only executes once.
func Initialize() error {
	initOnce.Do(func() {
		initErr = doInit()
	})
	return initErr
}

func doInit() error {
	// Extract DLL next to the executable
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find executable path: %w", err)
	}
	dir := filepath.Dir(exePath)
	dllPath := filepath.Join(dir, "wireguard.dll")

	// Skip extraction if already exists and same size
	if info, err := os.Stat(dllPath); err != nil || info.Size() != int64(len(wireguardDLL)) {
		// Try writing next to exe first
		if err := os.WriteFile(dllPath, wireguardDLL, 0644); err != nil {
			// Fallback to temp
			dllPath = filepath.Join(os.TempDir(), "wireguard.dll")
			if info, err := os.Stat(dllPath); err != nil || info.Size() != int64(len(wireguardDLL)) {
				if err := os.WriteFile(dllPath, wireguardDLL, 0644); err != nil {
					return fmt.Errorf("cannot write wireguard.dll: %w", err)
				}
			}
		}
	}

	// Load the DLL
	dll, err = windows.LoadDLL(dllPath)
	if err != nil {
		return fmt.Errorf("cannot load wireguard.dll: %w", err)
	}

	// Resolve all function pointers
	procs := map[string]**windows.Proc{
		"WireGuardCreateAdapter":           &procCreateAdapter,
		"WireGuardOpenAdapter":             &procOpenAdapter,
		"WireGuardCloseAdapter":            &procCloseAdapter,
		"WireGuardGetAdapterLUID":          &procGetAdapterLUID,
		"WireGuardGetRunningDriverVersion": &procGetRunningDriverVer,
		"WireGuardSetLogger":               &procSetLogger,
		"WireGuardSetAdapterLogging":       &procSetAdapterLogging,
		"WireGuardSetAdapterState":         &procSetAdapterState,
		"WireGuardGetAdapterState":         &procGetAdapterState,
		"WireGuardSetConfiguration":        &procSetConfiguration,
		"WireGuardGetConfiguration":        &procGetConfiguration,
		"WireGuardDeleteDriver":            &procDeleteDriver,
	}

	for name, ptr := range procs {
		p, err := dll.FindProc(name)
		if err != nil {
			return fmt.Errorf("wireguard.dll missing %s: %w", name, err)
		}
		*ptr = p
	}

	return nil
}

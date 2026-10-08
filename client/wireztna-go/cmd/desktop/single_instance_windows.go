//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	createMutexW    = kernel32.NewProc("CreateMutexW")
	errAlreadyExists = syscall.Errno(183) // ERROR_ALREADY_EXISTS
)

// ensureSingleInstance creates a system-wide named mutex.
// If another instance of wireztna-desktop is already running, this exits immediately.
func ensureSingleInstance() {
	name, _ := syscall.UTF16PtrFromString("Global\\WireZTNA-Desktop-SingleInstance")

	handle, _, err := createMutexW.Call(
		0,
		0,
		uintptr(unsafe.Pointer(name)),
	)

	if handle == 0 {
		// Could not create mutex at all — proceed anyway (non-fatal)
		return
	}

	// If the mutex already existed, another instance owns it
	if err == errAlreadyExists {
		fmt.Println("[tray] Another instance is already running. Exiting.")
		os.Exit(0)
	}

	// We own the mutex — keep it open for the lifetime of the process.
	// Do NOT close the handle; it will be released when the process exits.
}

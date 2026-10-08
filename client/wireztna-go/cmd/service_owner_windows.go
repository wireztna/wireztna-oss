//go:build windows

package cmd

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const (
	tunnelOwnerMutexName  = "Global\\WireZTNA-TunnelOwner"
	legacyServicePipePath = `\\.\pipe\wireztna`
)

// existingServiceOwnerAvailable quickly detects a pre-v0.9.29 owner that does
// not create the global mutex. Keep this timeout short so a normal SCM start is
// not delayed when no legacy named-pipe server exists.
func existingServiceOwnerAvailable() bool {
	timeout := 100 * time.Millisecond
	conn, err := winio.DialPipe(legacyServicePipePath, &timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// acquireServiceOwnership ensures that the SCM service and elevated helper
// cannot both own the global WireGuard adapter and named-pipe state.
// The handle is intentionally kept open for the service lifetime; object
// existence, rather than mutex acquisition, is the ownership sentinel.
func acquireServiceOwnership() (func(), error) {
	name, err := windows.UTF16PtrFromString(tunnelOwnerMutexName)
	if err != nil {
		return nil, fmt.Errorf("encode tunnel owner mutex name: %w", err)
	}

	// Only LocalSystem and elevated administrators may open the ownership
	// sentinel. This prevents a normal user process from impersonating a tunnel
	// owner after the real service has created the object.
	securityDescriptor, err := windows.SecurityDescriptorFromString(
		"D:P(A;;GA;;;SY)(A;;GA;;;BA)",
	)
	if err != nil {
		return nil, fmt.Errorf("create tunnel owner security descriptor: %w", err)
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: securityDescriptor,
	}

	handle, createErr := windows.CreateMutex(attributes, false, name)
	if handle == 0 {
		return nil, fmt.Errorf("create tunnel owner mutex: %w", createErr)
	}
	if errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("another WireZTNA tunnel owner is already running")
	}
	if createErr != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("create tunnel owner mutex: %w", createErr)
	}

	return func() {
		_ = windows.CloseHandle(handle)
	}, nil
}

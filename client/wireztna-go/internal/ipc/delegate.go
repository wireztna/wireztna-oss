package ipc

import (
	"fmt"
	"runtime"
	"time"

	"github.com/wireztna/client/internal/elevation"
)

// ServiceAvailable checks if the WireZTNA service is reachable via IPC.
// Returns true if the service is running and accepting commands.
func ServiceAvailable() bool {
	if runtime.GOOS != "windows" {
		return false // On macOS/Linux we don't use the service model by default
	}
	c := NewClient()
	_, err := c.SendStatus()
	return err == nil
}

// ShouldDelegateToService returns true if the current command should delegate
// tunnel operations to the WireZTNA service instead of performing them directly.
// On Windows, this is always true when the service is reachable.
// On macOS/Linux, this always returns false (direct tunnel creation with sudo).
func ShouldDelegateToService() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	return ServiceAvailable()
}

// EnsureHelper ensures the elevated helper process is running on Windows.
// If already running (IPC reachable), returns immediately.
// Otherwise, launches the helper with UAC elevation and waits for IPC readiness.
// On non-Windows platforms, returns nil (no-op).
func EnsureHelper() error {
	if runtime.GOOS != "windows" {
		return nil
	}

	// Already running?
	if ServiceAvailable() {
		return nil
	}

	// Launch helper with UAC (shows elevation prompt)
	_, err := elevation.RunElevated([]string{"helper"}, true)
	if err != nil {
		return fmt.Errorf("UAC elevation failed: %w", err)
	}

	// Wait for helper IPC to become available (up to 10s)
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		if ServiceAvailable() {
			return nil
		}
	}

	return fmt.Errorf("helper did not start — UAC may have been denied")
}

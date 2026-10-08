//go:build !windows

package main

// ensureSingleInstance is a no-op on non-Windows platforms.
// On macOS/Linux, launchd/systemd typically prevent duplicate instances.
func ensureSingleInstance() {}

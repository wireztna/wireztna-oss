//go:build !windows

package main

// showNotification is a no-op on non-Windows platforms.
// On macOS/Linux the tray app is less critical since users use CLI/TUI.
func showNotification(title, message string) {
	// No-op: systray tooltip updates serve as visual feedback
	_ = title
	_ = message
}

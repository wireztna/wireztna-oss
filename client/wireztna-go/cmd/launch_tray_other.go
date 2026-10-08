//go:build !windows

package cmd

// launchTrayIfInstalled is a no-op on non-Windows platforms.
// On macOS/Linux the tray is launched by launchd/systemd/desktop autostart.
func launchTrayIfInstalled() {}

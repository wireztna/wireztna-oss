//go:build !windows

// Package winsvc provides stubs for non-Windows platforms.
package winsvc

// IsWindowsService always returns false on non-Windows.
func IsWindowsService() bool {
	return false
}

// RunAsService is a no-op on non-Windows.
func RunAsService(serviceMain func(stop <-chan struct{})) error {
	// On non-Windows, just run the service main directly
	stop := make(chan struct{})
	serviceMain(stop)
	return nil
}

// Install is a no-op on non-Windows (use systemd/launchd instead).
func Install() error {
	return nil
}

// Uninstall is a no-op on non-Windows.
func Uninstall() error {
	return nil
}

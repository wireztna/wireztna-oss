//go:build !windows

package cmd

// isWindowsService always returns false on non-Windows platforms.
func isWindowsService() bool {
	return false
}

// legacyServiceRuntimeAllowed prevents the unauthenticated legacy IPC owner
// from being started as a launchd/systemd service or elevated helper.
func legacyServiceRuntimeAllowed() error {
	return errLegacyServiceActivationBlocked
}

// runAsWindowsService cannot be used on non-Windows platforms.
func runAsWindowsService() error {
	return errLegacyServiceActivationBlocked
}

// installService is disabled until authenticated IPC v2 is wired.
func installService() error {
	return errLegacyServiceActivationBlocked
}

// uninstallService is not applicable because this release installs no Unix service.
func uninstallService() error {
	return errLegacyServiceActivationBlocked
}

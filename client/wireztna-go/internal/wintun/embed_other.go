//go:build !windows

package wintun

// EnsureAvailable is a no-op on non-Windows platforms.
func EnsureAvailable() error {
	return nil
}

//go:build !windows

package elevation

// IsElevated on non-Windows checks if running as root.
func IsElevated() bool {
	return false // On macOS/Linux, use sudo directly
}

// RunElevated is not applicable on non-Windows.
func RunElevated(args []string, hide bool) (uint32, error) {
	return 0, nil
}

//go:build !windows

package wgnt

import "fmt"

// Initialize is a no-op on non-Windows platforms.
func Initialize() error {
	return fmt.Errorf("wireguard-nt is only available on Windows")
}

//go:build linux

package firewall

import "os"

func openFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
}

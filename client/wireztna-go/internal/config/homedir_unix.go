//go:build !windows

package config

import (
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// realUserHome resolves the home directory of the actual invoking user,
// even when running under sudo. On Linux, sudo resets $HOME to /root,
// which means the CLI can't find the config/token written by the
// non-root user. This function detects SUDO_USER and resolves their
// real home directory.
//
// On macOS, sudo preserves $HOME by default so this is usually a no-op,
// but it handles the edge case where macOS is configured with env_reset.
func realUserHome() string {
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		return "" // Not running under sudo — use default os.UserHomeDir()
	}

	// Try os/user lookup first (works on most systems)
	u, err := user.Lookup(sudoUser)
	if err == nil && u.HomeDir != "" {
		return u.HomeDir
	}

	// Fallback: ask the system via getent (works on Linux even without cgo)
	out, err := exec.Command("getent", "passwd", sudoUser).Output()
	if err == nil {
		// getent output: username:x:uid:gid:gecos:homedir:shell
		fields := strings.Split(strings.TrimSpace(string(out)), ":")
		if len(fields) >= 6 && fields[5] != "" {
			return fields[5]
		}
	}

	// Last resort: try ~SUDO_USER expansion
	// (this won't work in all shells but covers the common case)
	home := "/home/" + sudoUser
	if _, err := os.Stat(home); err == nil {
		return home
	}

	return ""
}

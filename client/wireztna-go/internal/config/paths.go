package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// SharedConfigDir returns the system-wide config directory used by the service.
// On Windows: C:\ProgramData\WireZTNA
// On macOS/Linux: /etc/wireztna (or falls back to user dir)
//
// This allows the service (running as LocalSystem/root) and the user CLI
// (running as the logged-in user) to share the same config and token.
func SharedConfigDir() (string, error) {
	var dir string

	switch runtime.GOOS {
	case "windows":
		// ProgramData is accessible to both LocalSystem and regular users
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		dir = filepath.Join(programData, "WireZTNA")
	default:
		// On Unix, try /etc/wireztna first (if writable = root/service context)
		// Otherwise return an error — callers (configDir) handle the fallback
		dir = "/etc/wireztna/client"
		if err := os.MkdirAll(dir, 0755); err != nil {
			// Not root — return error so the caller can fall back to user dir
			return "", fmt.Errorf("cannot create shared config dir: %w", err)
		}
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// ResolveConfigDir determines the best config directory to use.
// Priority:
// 1. If shared config dir exists AND has a config.yaml → use it (service mode)
// 2. Otherwise → use user-specific ~/.wireztna (standalone mode)
func ResolveConfigDir() (string, error) {
	// Check shared dir first
	shared, err := SharedConfigDir()
	if err == nil {
		configFile := filepath.Join(shared, "config.yaml")
		if _, err := os.Stat(configFile); err == nil {
			if err := hardenLegacyPrivateFiles(shared); err != nil {
				return "", err
			}
			return shared, nil
		}
	}

	// Fall back to user dir. configDir repairs legacy private files before
	// returning the path to Viper or token readers.
	return configDir()
}

// IsServiceInstalled checks if the shared config directory exists,
// indicating the service/MSI is installed and config should go there.
func IsServiceInstalled() bool {
	shared, err := SharedConfigDir()
	if err != nil {
		return false
	}
	// Check if the install dir marker exists (set by MSI or service install)
	marker := filepath.Join(shared, ".installed")
	_, err = os.Stat(marker)
	return err == nil
}

// MarkServiceInstalled creates the install marker in the shared config dir.
// Called by `wireztna service install` or the MSI post-install action.
func MarkServiceInstalled() error {
	shared, err := SharedConfigDir()
	if err != nil {
		return err
	}
	marker := filepath.Join(shared, ".installed")
	return os.WriteFile(marker, []byte("1"), 0644)
}

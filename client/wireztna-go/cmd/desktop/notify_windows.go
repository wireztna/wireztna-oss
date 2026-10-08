//go:build windows

package main

import (
	"os"
	"path/filepath"

	"gopkg.in/toast.v1"
)

const appID = "WireZTNA"

// showNotification displays a Windows toast notification via the Action Center.
func showNotification(title, message string) {
	notification := toast.Notification{
		AppID:   appID,
		Title:   title,
		Message: message,
	}

	// Use the app icon if available alongside the binary
	if iconPath := findIconFile(); iconPath != "" {
		notification.Icon = iconPath
	}

	_ = notification.Push()
}

// findIconFile returns the path to the icon file if it exists next to the binary.
func findIconFile() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)

	candidates := []string{
		filepath.Join(dir, "wireztna.png"),
		filepath.Join(dir, "wireztna.ico"),
		filepath.Join(dir, "icon.png"),
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

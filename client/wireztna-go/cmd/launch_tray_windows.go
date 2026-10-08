//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// launchTrayIfInstalled launches wireztna-desktop.exe as a detached non-elevated process.
// Uses explorer.exe as a broker to de-elevate (if current process is running as admin,
// a child process would inherit elevation — explorer launches it as the normal user).
func launchTrayIfInstalled() {
	trayPath := findTrayExe()
	if trayPath == "" {
		return
	}

	// Use explorer.exe to launch the tray de-elevated.
	// explorer.exe running as the desktop shell will launch processes at the user's
	// normal integrity level, even if we're currently elevated.
	cmd := exec.Command("explorer.exe", trayPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008, // DETACHED_PROCESS
	}
	if err := cmd.Start(); err != nil {
		// Fallback: launch directly (may be elevated but at least it'll show)
		directLaunch(trayPath)
		return
	}

	fmt.Println("WireZTNA tray launched — check your system tray (bottom-right)")
}

func directLaunch(trayPath string) {
	cmd := exec.Command(trayPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008 | 0x00000010, // DETACHED_PROCESS | CREATE_NEW_CONSOLE
	}
	if err := cmd.Start(); err != nil {
		return
	}
	fmt.Println("WireZTNA tray launched — check your system tray (bottom-right)")
}

func findTrayExe() string {
	// Check next to our own binary first
	exePath, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exePath), "wireztna-desktop.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Check standard install location
	candidate := `C:\Program Files\WireZTNA\wireztna-desktop.exe`
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}

	return ""
}

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/wireztna/client/internal/elevation"
)

// ensureServiceRunning checks if the service is reachable.
// If not (on Windows), it attempts to install and start it.
// This handles the case where the MSI installer didn't register the service
// properly, or where the user installed via standalone binary.
func ensureServiceRunning() {
	// Quick check — can we reach the service?
	_, err := ipcClient.SendStatus()
	if err == nil {
		return // Service is running and reachable
	}

	if runtime.GOOS != "windows" {
		return // On other platforms, service is managed by systemd/launchd
	}

	// On Windows, try to install + start the service.
	// This uses sc.exe directly (no admin prompt — works if we're elevated,
	// silently fails if not).
	wireztnaExe := findWireztnaExe()
	if wireztnaExe == "" {
		return
	}

	// Try to create the service (ignore errors — might already exist or lack perms)
	_ = exec.Command("sc", "create", "WireZTNA",
		"binPath=", wireztnaExe+" service",
		"DisplayName=", "WireZTNA Network Access Service",
		"start=", "auto",
	).Run()

	// Try to start it
	_ = exec.Command("sc", "start", "WireZTNA").Run()

	// Wait a moment for it to start
	time.Sleep(2 * time.Second)
}

// findWireztnaExe locates the wireztna.exe binary.
// Looks in the same directory as wireztna-desktop.exe first,
// then in common install locations.
func findWireztnaExe() string {
	// Same directory as the currently running tray binary (most reliable)
	myExe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(myExe), "wireztna.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Standard install location
	standard := `C:\Program Files\WireZTNA\wireztna.exe`
	if _, err := os.Stat(standard); err == nil {
		return standard
	}

	// Try PATH
	if absPath, err := exec.LookPath("wireztna.exe"); err == nil {
		return absPath
	}

	return ""
}

// exeDir returns the directory of the currently running executable.
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// startServiceFallback attempts to start wireztna service as a background process
// if the Windows service registration failed (e.g., no admin rights).
// This is a fallback — not as robust as a proper service, but it works.
func startServiceFallback() {
	wireztnaExe := findWireztnaExe()
	if wireztnaExe == "" {
		return
	}

	// Check one more time if the IPC is reachable
	_, err := ipcClient.SendStatus()
	if err == nil {
		return // Already running
	}

	// Start as a background process (not a proper service, but functional)
	cmd := exec.Command(wireztnaExe, "service")
	cmd.Dir = filepath.Dir(wireztnaExe)
	_ = cmd.Start()

	// Detach — don't wait for it
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}

	// Give it a moment to start listening
	time.Sleep(2 * time.Second)
}

// startServiceElevated launches the helper/service process with UAC elevation.
// This is the key fix: the tray app itself doesn't need admin, but the helper
// that manages the wintun adapter does. We use ShellExecuteEx("runas") to
// trigger a single UAC prompt, then the helper runs elevated in the user's
// session (not Session 0) where wireguard-go UDP sockets work correctly.
func startServiceElevated() {
	wireztnaExe := findWireztnaExe()
	if wireztnaExe == "" {
		return
	}

	// Check one more time if the IPC is reachable
	_, err := ipcClient.SendStatus()
	if err == nil {
		return // Already running
	}

	if runtime.GOOS != "windows" {
		return
	}

	// Launch "wireztna.exe service" with UAC elevation.
	// hide=false: the service needs a visible console window to stay alive.
	fmt.Println("[tray] Requesting elevated permissions for tunnel service...")
	_, uacErr := elevation.RunElevated([]string{"service"}, false) // visible window
	if uacErr != nil {
		// UAC was denied or failed — user must manually run wireztna connect as admin
		fmt.Fprintf(os.Stderr, "[tray] UAC elevation failed: %v\n", uacErr)
		return
	}

	// Wait for the service to start listening on IPC.
	// The service needs time to: cleanup zombie adapter (1s sleep), create pipe.
	// Total wait: up to 10 seconds.
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		_, err := ipcClient.SendStatus()
		if err == nil {
			fmt.Println("[tray] Elevated service started successfully")
			return
		}
	}
	fmt.Println("[tray] warning: service started but IPC not reachable after 10s")
}

// ensureServiceAvailable tries all methods to get the service running.
// Order of attempts:
//  1. Check if already running (IPC reachable)
//  2. Try starting the Windows service (sc start — works if service registered and we have perms)
//  3. Try starting as background process (works if we happen to be elevated already)
//
// NOTE: UAC elevation is NOT attempted at startup because the tray is a
// -H windowsgui binary and the UAC prompt may not display correctly without
// user interaction. Instead, elevation happens on-demand when the user clicks Connect.
func ensureServiceAvailable() {
	// First check if it's already running
	_, err := ipcClient.SendStatus()
	if err == nil {
		return
	}

	// Try the proper Windows service approach (silent, no UAC)
	ensureServiceRunning()

	// Check again
	_, err = ipcClient.SendStatus()
	if err == nil {
		return
	}

	// Try unelevated background process (for cases where tray is already elevated)
	if runtime.GOOS == "windows" && elevation.IsElevated() {
		startServiceFallback()
	}
}

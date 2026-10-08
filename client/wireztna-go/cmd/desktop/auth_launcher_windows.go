//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// openAuthFlow launches the native WPF authentication helper. The legacy
// WebView2 wizard remains as a one-release fallback for incomplete upgrades.
func openAuthFlow(step string, onDone func()) {
	executable, err := os.Executable()
	if err != nil {
		openWizard(step, onDone)
		return
	}

	helperPath := filepath.Join(filepath.Dir(executable), "wireztna-auth.exe")
	if _, err := os.Stat(helperPath); err != nil {
		openWizard(step, onDone)
		return
	}

	command := exec.Command(
		helperPath,
		"--step", step,
		"--parent-pid", strconv.Itoa(os.Getpid()),
	)
	command.Dir = filepath.Dir(helperPath)

	startedAt := time.Now()
	if err := command.Start(); err != nil {
		openWizard(step, onDone)
		return
	}

	go func() {
		err := command.Wait()
		exitCode := 0
		if err != nil {
			exitCode = -1
			if exitError, ok := err.(*exec.ExitError); ok {
				exitCode = exitError.ExitCode()
			}
		}

		// A secondary helper uses explicit exit codes so the tray can distinguish
		// an accepted handoff from a failed activation without guessing by timing.
		switch exitCode {
		case 20:
			waitForAuthCompletion(onDone)
			return
		case 21:
			openWizard(step, onDone)
			return
		}

		refreshState()

		app.mu.RLock()
		connected := app.status == "connected"
		app.mu.RUnlock()

		if connected && onDone != nil {
			onDone()
			return
		}

		// A helper that cannot initialize should not strand users during an
		// upgrade. Do not fall back after an interactive session or normal close.
		if err != nil && time.Since(startedAt) < 3*time.Second {
			openWizard(step, onDone)
		}
	}()
}

// waitForAuthCompletion observes the service, which is the source of truth for
// an authentication flow forwarded to an already-running helper instance.
func waitForAuthCompletion(onDone func()) {
	if onDone == nil {
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(10 * time.Minute)
	defer timeout.Stop()

	for {
		refreshState()

		app.mu.RLock()
		connected := app.status == "connected"
		app.mu.RUnlock()
		if connected {
			onDone()
			return
		}

		select {
		case <-ticker.C:
		case <-timeout.C:
			return
		}
	}
}

//go:build windows

package cmd

import (
	"fmt"
	"os/exec"

	"github.com/wireztna/client/internal/winsvc"
)

// isWindowsService detects if we're running under the Windows SCM.
func isWindowsService() bool {
	return winsvc.IsWindowsService()
}

// runAsWindowsService wraps runServiceMain in the Windows SCM framework.
func runAsWindowsService() error {
	return winsvc.RunAsService(func(stop <-chan struct{}) {
		_ = runServiceMain(stop)
	})
}

// legacyServiceRuntimeAllowed preserves the runtime only for existing Windows
// installations. All new installation entry points remain fail-closed.
func legacyServiceRuntimeAllowed() error {
	return nil
}

// installService is disabled until authenticated IPC v2 is wired.
func installService() error {
	return errLegacyServiceActivationBlocked
}

// uninstallService stops and removes the WireZTNA Windows service.
func uninstallService() error {
	fmt.Println("[*] Stopping WireZTNA service...")
	exec.Command("sc.exe", "stop", "WireZTNA").Run()

	fmt.Println("[*] Removing service...")
	if err := winsvc.Uninstall(); err != nil {
		return fmt.Errorf("uninstall failed: %w", err)
	}
	fmt.Println("[+] Service removed")
	return nil
}

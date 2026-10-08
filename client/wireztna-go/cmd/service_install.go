package cmd

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/wireztna/client/internal/winsvc"
)

var errLegacyServiceActivationBlocked = errors.New("managed service activation is disabled until IPC v2 has OS-authenticated peer authorization; legacy IPC must not be installed or started")

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the WireZTNA service (currently disabled)",
	Long: `Managed service installation is deliberately disabled on every platform.

The existing service runtime uses legacy IPC without the required IPC v2
OS-authenticated peer authorization. This command fails before writing an
installation marker or changing the host service manager.`,
	RunE: runServiceInstall,
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall an existing WireZTNA Windows service",
	Long:  `Removes an existing legacy WireZTNA service on Windows. This does not enable or install a replacement service.`,
	RunE:  runServiceUninstall,
}

func init() {
	serviceCmd.AddCommand(serviceInstallCmd)
	serviceCmd.AddCommand(serviceUninstallCmd)
}

func runServiceInstall(cmd *cobra.Command, args []string) error {
	return errLegacyServiceActivationBlocked
}

func runServiceUninstall(cmd *cobra.Command, args []string) error {
	switch runtime.GOOS {
	case "windows":
		if err := winsvc.Uninstall(); err != nil {
			return fmt.Errorf("failed to uninstall service: %w", err)
		}
		fmt.Println("WireZTNA service uninstalled.")
		return nil
	case "darwin", "linux":
		return fmt.Errorf("no managed WireZTNA service is installed by this release on %s", runtime.GOOS)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

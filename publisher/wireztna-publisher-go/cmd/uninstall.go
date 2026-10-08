package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/wireztna/publisher/internal/config"
	"github.com/wireztna/publisher/internal/firewall"
	"github.com/wireztna/publisher/internal/service"
	"github.com/wireztna/publisher/internal/tunnel"
)

var flagPurge bool

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove publisher: stop service, remove tunnel, clean config",
	Long: `Completely removes the publisher from this system:
  1. Stops and removes the systemd service
  2. Removes the WireGuard tunnel interface
  3. Removes firewall rules
  4. Deletes configuration and enrollment state

Use --purge to also delete the binary itself.`,
	RunE: runUninstall,
}

func init() {
	uninstallCmd.Flags().BoolVar(&flagPurge, "purge", false, "Also remove the binary itself")
}

func runUninstall(cmd *cobra.Command, args []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (use sudo)")
	}

	fmt.Println("[*] Uninstalling WireZTNA Publisher...")
	fmt.Println()

	// 1. Stop and remove systemd service
	if service.HasSystemd() {
		fmt.Print("[*] Stopping service...          ")
		if err := service.Uninstall(); err != nil {
			fmt.Printf("⚠ %v\n", err)
		} else {
			fmt.Println("✓")
		}
	}

	// 2. Remove WireGuard tunnel
	fmt.Print("[*] Removing WireGuard tunnel... ")
	if err := tunnel.Destroy(); err != nil {
		fmt.Printf("⚠ %v\n", err)
	} else {
		fmt.Println("✓")
	}

	// 3. Remove firewall rules
	fmt.Print("[*] Removing firewall rules...   ")
	if err := firewall.Teardown(); err != nil {
		fmt.Printf("⚠ %v\n", err)
	} else {
		fmt.Println("✓")
	}

	// 4. Remove config directory
	fmt.Print("[*] Removing config directory... ")
	if err := config.RemoveAll(); err != nil {
		fmt.Printf("⚠ %v\n", err)
	} else {
		fmt.Println("✓")
	}

	// 5. Optionally remove the binary itself
	if flagPurge {
		exe, err := os.Executable()
		if err == nil {
			fmt.Printf("[*] Removing binary (%s)... ", exe)
			if err := os.Remove(exe); err != nil {
				fmt.Printf("⚠ %v\n", err)
			} else {
				fmt.Println("✓")
			}
		}
	}

	fmt.Println()
	fmt.Println("[✓] Publisher uninstalled cleanly.")
	if !flagPurge {
		fmt.Println()
		fmt.Println("    To remove the binary itself:")
		exe, _ := os.Executable()
		fmt.Printf("    rm %s\n", exe)
	}

	return nil
}

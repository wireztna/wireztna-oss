package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/wireztna/publisher/pkg/version"
)

var rootCmd = &cobra.Command{
	Use:   "wireztna-publisher",
	Short: "WireZTNA Publisher Agent",
	Long:  `Self-contained WireZTNA publisher agent. Connects to a broker via WireGuard and exposes local network resources to authorized clients.`,
	Version: fmt.Sprintf("%s (commit: %s, built: %s)", version.Version, version.Commit, version.BuildDate),
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(uninstallCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(diagnoseCmd)
}

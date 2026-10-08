package cmd

import "github.com/spf13/cobra"

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch interactive terminal UI",
	Long: `Opens an interactive terminal interface showing real-time
connection status, session info, and available projects.

The TUI refreshes every 2 seconds and provides keyboard
shortcuts for connecting, disconnecting, and switching projects.

Works on macOS, Linux, and Windows (cmd, PowerShell, Windows Terminal).`,
	RunE: runTUI,
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}

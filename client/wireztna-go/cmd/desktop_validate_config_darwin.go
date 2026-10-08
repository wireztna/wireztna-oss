//go:build darwin

package cmd

import (
	"errors"
	"runtime"

	"github.com/spf13/cobra"
	platformdarwin "github.com/wireztna/client/desktop/platform/darwin"
)

type desktopValidateConfigOptions struct {
	ownerUID  uint64
	configDir string
}

func init() {
	rootCmd.AddCommand(newDesktopValidateConfigCommand())
}

func newDesktopValidateConfigCommand() *cobra.Command {
	options := desktopValidateConfigOptions{}
	command := &cobra.Command{
		Use:    "desktop-validate-config",
		Hidden: true,
		Args:   cobra.NoArgs,
		// Override root's legacy config pre-run. Validation must read only the
		// explicit owner/config pair and must not harden or rewrite the file.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOARCH != "arm64" {
				return errors.New("desktop config validation supports macOS arm64 only")
			}
			if !cmd.Flags().Changed("owner-uid") || options.ownerUID == 0 {
				return errors.New("--owner-uid must explicitly identify a non-root desktop user")
			}
			if !cmd.Flags().Changed("config-dir") || options.configDir == "" {
				return errors.New("--config-dir must be explicitly provided")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return validateDesktopConfig(options)
		},
	}
	command.Flags().Uint64Var(&options.ownerUID, "owner-uid", 0, "UID owning the explicit desktop configuration")
	command.Flags().StringVar(&options.configDir, "config-dir", "", "absolute owner-controlled directory containing config.yaml")
	return command
}

func validateDesktopConfig(options desktopValidateConfigOptions) error {
	paths, err := platformdarwin.ResolveClientPaths(options.ownerUID, options.configDir)
	if err != nil {
		return err
	}
	clientConfig, err := platformdarwin.LoadClientConfig(paths)
	if err != nil {
		return err
	}
	return platformdarwin.ValidateClientConfig(clientConfig)
}

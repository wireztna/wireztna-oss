//go:build darwin

package cmd

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/wireztna/client/desktop/controller"
	platformdarwin "github.com/wireztna/client/desktop/platform/darwin"
)

type desktopVerifyDisconnectedOptions struct {
	ownerUID  uint64
	configDir string
}

func init() {
	rootCmd.AddCommand(newDesktopVerifyDisconnectedCommand())
}

func newDesktopVerifyDisconnectedCommand() *cobra.Command {
	options := desktopVerifyDisconnectedOptions{}
	command := &cobra.Command{
		Use:    "desktop-verify-disconnected",
		Hidden: true,
		Args:   cobra.NoArgs,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOARCH != "arm64" {
				return errors.New("desktop absence verification supports macOS arm64 only")
			}
			if !cmd.Flags().Changed("owner-uid") || options.ownerUID == 0 {
				return errors.New("--owner-uid must explicitly identify a non-root desktop user")
			}
			if !cmd.Flags().Changed("config-dir") || options.configDir == "" {
				return errors.New("--config-dir must be explicitly provided")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return verifyDesktopDisconnected(cmd.Context(), options)
		},
	}
	command.Flags().Uint64Var(&options.ownerUID, "owner-uid", 0, "UID owning the explicit desktop configuration")
	command.Flags().StringVar(&options.configDir, "config-dir", "", "absolute owner-controlled directory containing config.yaml")
	return command
}

func verifyDesktopDisconnected(ctx context.Context, options desktopVerifyDisconnectedOptions) error {
	paths, err := platformdarwin.ResolveClientPaths(options.ownerUID, options.configDir)
	if err != nil {
		return err
	}
	clientConfig, err := platformdarwin.LoadClientConfig(paths)
	if err != nil {
		return err
	}
	owner := controller.OwnerID(fmt.Sprintf("uid:%d", options.ownerUID))
	return platformdarwin.VerifyOwnedResourcesAbsent(ctx, owner, clientConfig)
}

// Package cmd implements the CLI interface using cobra.
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/pkg/version"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "wireztna",
	Short: "WireZTNA — Zero Trust Network Access client",
	Long: `wireztna is a WireGuard-based ZTNA client that connects to a
WireZTNA broker, manages ephemeral PSK sessions, and provides
split DNS routing for private network access.

It replaces the native WireGuard GUI with a purpose-built client
that handles authentication, session renewal, and tunnel lifecycle.`,
	Version: version.Full(),
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		return initConfig()
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ~/.wireztna/config.yaml)")
	rootCmd.PersistentFlags().String("api-url", "", "control plane API URL")
	rootCmd.PersistentFlags().String("interface", "wg-wireztna", "WireGuard interface name")
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "verbose output")

	// Bind flags to viper
	viper.BindPFlag("api_url", rootCmd.PersistentFlags().Lookup("api-url"))
	viper.BindPFlag("interface", rootCmd.PersistentFlags().Lookup("interface"))
	viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))

	// Environment variable bindings
	viper.BindEnv("api_url", "WIREZTNA_API_URL")
	viper.BindEnv("username", "WIREZTNA_USERNAME")
	viper.BindEnv("password", "WIREZTNA_PASSWORD")
	viper.BindEnv("group", "WIREZTNA_GROUP")
}

func initConfig() error {
	viper.AutomaticEnv()
	if cfgFile != "" {
		if err := config.ReadConfigFile(cfgFile); err != nil {
			return fmt.Errorf("load WireZTNA config %s: %w", cfgFile, err)
		}
		return nil
	}

	// ResolveConfigDir validates and hardens legacy private files, while
	// ReadResolvedConfig keeps the same no-follow handle discipline for parsing.
	if err := config.ReadResolvedConfig(); err != nil {
		return fmt.Errorf("refusing unsafe WireZTNA configuration: %w", err)
	}
	return nil
}

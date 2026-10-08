//go:build darwin

package cmd

import (
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/tui"
)

var newDarwinTUIBackend = tui.NewDarwinManagedBackend

func runTUI(cmd *cobra.Command, args []string) error {
	apiURL := viper.GetString("api_url")
	if apiURL == "" {
		return fmt.Errorf("API URL required: set --api-url, WIREZTNA_API_URL, or api_url in config")
	}

	// The API client is retained only for the inline OTP flow. Snapshot,
	// catalog, and every connection mutation are owned exclusively by IPC v2.
	client := api.NewClient(apiURL)
	if token, err := config.LoadToken(); err == nil && token != "" && !config.IsTokenExpired(token) {
		client.SetToken(token)
	}
	cfg := config.Load()
	backend := newDarwinTUIBackend()
	model := tui.NewManagedModel(client, cfg, backend)

	program := tea.NewProgram(model, tea.WithAltScreen())
	_, runErr := program.Run()
	closeErr := backend.Close()
	if runErr != nil {
		runErr = fmt.Errorf("TUI error: %w", runErr)
	}
	return errors.Join(runErr, closeErr)
}

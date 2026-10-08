//go:build !darwin

package cmd

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/tui"
)

func runTUI(cmd *cobra.Command, args []string) error {
	apiURL := viper.GetString("api_url")
	if apiURL == "" {
		return fmt.Errorf("API URL required: set --api-url, WIREZTNA_API_URL, or api_url in config")
	}

	client := api.NewClient(apiURL)
	needsAuth := false
	token, err := config.LoadToken()
	if err != nil || token == "" {
		needsAuth = true
	} else if config.IsTokenExpired(token) {
		needsAuth = true
	} else {
		client.SetToken(token)
	}

	cfg := config.Load()
	model := tui.NewModel(client, cfg)
	if needsAuth {
		model.SetInitialState(tui.StateAuthRequired)
	}

	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	return nil
}

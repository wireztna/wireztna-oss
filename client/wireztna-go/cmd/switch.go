package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/ipc"
)

type groupSwitchLifecycle interface {
	SendSwitch(groupID string) error
}

var resolveGroupSwitchLifecycle = func() (groupSwitchLifecycle, error) {
	if !ipc.ServiceAvailable() {
		return nil, fmt.Errorf("safe project switching requires the WireZTNA service; use 'wireztna disconnect' then 'wireztna connect -g <group>'")
	}
	return ipc.NewClient(), nil
}

var switchCmd = &cobra.Command{
	Use:   "switch [group-name]",
	Short: "Switch active project/group",
	Long: `Switch to a different project/group when CIDR overlap exists.

This is equivalent to disconnecting and reconnecting with a different
group selection. The tunnel will be briefly interrupted (~10s) while
the broker applies the new PSK with the updated routing rules.

If no group name is provided, an interactive selector is shown.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSwitch,
}

func init() {
	rootCmd.AddCommand(switchCmd)
}

func runSwitch(cmd *cobra.Command, args []string) error {
	apiURL := viper.GetString("api_url")
	if apiURL == "" {
		return fmt.Errorf("API URL required")
	}

	// A group switch changes both the server-side PSK/policy and the local
	// dataplane. Only the service owns a serialized lifecycle that performs the
	// complete transition before acknowledging success. Direct CLI mode cannot
	// make that transition atomic, so reject it before any session mutation.
	lifecycle, err := resolveGroupSwitchLifecycle()
	if err != nil {
		return err
	}

	token, err := config.LoadToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated — run 'wireztna login' first")
	}

	client := api.NewClient(apiURL)
	client.SetToken(token)

	groups, err := client.GetAvailableGroups()
	if err != nil {
		return fmt.Errorf("failed to fetch groups: %w", err)
	}

	var groupID string

	if len(args) > 0 {
		// Find by name
		target := args[0]
		for _, g := range groups.Groups {
			if g.Name == target || g.ID == target {
				if g.OnlinePublishers == 0 {
					return fmt.Errorf("cannot switch to '%s': no publishers online", g.Name)
				}
				groupID = g.ID
				fmt.Printf("[+] Switching to: %s\n", g.Name)
				break
			}
		}
		if groupID == "" {
			return fmt.Errorf("group '%s' not found", target)
		}
	} else {
		// Interactive selection
		selectedID, err := promptGroupSelection(groups)
		if err != nil {
			return err
		}
		groupID = selectedID
	}

	fmt.Println("[*] Switching project through WireZTNA service...")
	if err := switchGroup(lifecycle, groupID); err != nil {
		return err
	}

	fmt.Println("[+] Switched — service applied the new session and dataplane")
	return nil
}

func switchGroup(lifecycle groupSwitchLifecycle, groupID string) error {
	if err := lifecycle.SendSwitch(groupID); err != nil {
		return fmt.Errorf("project switch failed: %w", err)
	}
	return nil
}

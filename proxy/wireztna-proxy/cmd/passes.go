package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/wireztna/proxy/internal/api"
)

var (
	passesBrokerFlag string
	passesTokenFlag  string
)

var passesCmd = &cobra.Command{
	Use:   "passes",
	Short: "Manage access passes",
	Long: `List and revoke your access passes.

Subcommands:
  list      List all your access passes (active, expired, revoked)
  revoke    Revoke an active pass immediately

Examples:
  wzctl passes list --broker https://broker.example.com
  wzctl passes revoke dap_7f3a9c --broker https://broker.example.com`,
}

var passesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your access passes",
	Long: `List all access passes you've created.
Shows pass IDs, status, scope, creation time, and traffic stats.

Example:
  wzctl passes list --broker https://broker.example.com`,
	RunE: runPassesList,
}

var passesRevokeCmd = &cobra.Command{
	Use:   "revoke <pass_id>",
	Short: "Revoke an active access pass",
	Long: `Immediately revoke an active access pass.
Any open tunnel connections through this pass will be terminated.

Example:
  wzctl passes revoke dap_7f3a9c --broker https://broker.example.com`,
	Args: cobra.ExactArgs(1),
	RunE: runPassesRevoke,
}

func init() {
	passesCmd.PersistentFlags().StringVar(&passesBrokerFlag, "broker", "", "Broker URL (e.g., https://broker.example.com)")
	passesCmd.PersistentFlags().StringVar(&passesTokenFlag, "token", "", "API key (or set WIREZTNA_TOKEN env)")
	passesCmd.MarkPersistentFlagRequired("broker")

	passesCmd.AddCommand(passesListCmd)
	passesCmd.AddCommand(passesRevokeCmd)
	rootCmd.AddCommand(passesCmd)
}

func resolvePassesToken() (string, error) {
	token := passesTokenFlag
	if token == "" {
		token = os.Getenv("WIREZTNA_TOKEN")
	}
	if token == "" {
		token = loadToken()
	}
	if token == "" {
		return "", fmt.Errorf("no API key provided. Use --token, WIREZTNA_TOKEN env, or run 'wzctl register --save'")
	}
	return token, nil
}

func runPassesList(cmd *cobra.Command, args []string) error {
	token, err := resolvePassesToken()
	if err != nil {
		return err
	}

	client := api.New(passesBrokerFlag, token)
	passes, err := client.ListPasses()
	if err != nil {
		return fmt.Errorf("failed to list passes: %w", err)
	}

	if len(passes) == 0 {
		fmt.Fprintf(os.Stderr, "No access passes found.\n")
		return nil
	}

	// Print table
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "PASS ID\tSTATUS\tSCOPE\tCREATED\tEXPIRES\tTRAFFIC\n")
	for _, p := range passes {
		created := formatTime(p.CreatedAt)
		expires := formatTime(p.ExpiresAt)
		traffic := formatBytes(p.BytesUploaded + p.BytesDownloaded)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			p.PassID, p.Status, p.ScopeSummary, created, expires, traffic)
	}
	w.Flush()

	// Summary
	active := 0
	for _, p := range passes {
		if p.Status == "active" {
			active++
		}
	}
	fmt.Fprintf(os.Stderr, "\n%d passes total, %d active\n", len(passes), active)

	return nil
}

func runPassesRevoke(cmd *cobra.Command, args []string) error {
	passID := args[0]

	token, err := resolvePassesToken()
	if err != nil {
		return err
	}

	client := api.New(passesBrokerFlag, token)
	err = client.RevokePass(passID)
	if err != nil {
		return fmt.Errorf("failed to revoke pass: %w", err)
	}

	fmt.Fprintf(os.Stderr, "✓ Pass %s revoked. Active connections terminated.\n", passID)
	return nil
}

func formatTime(isoTime string) string {
	t, err := time.Parse(time.RFC3339Nano, isoTime)
	if err != nil {
		// Try alternate format
		t, err = time.Parse("2006-01-02T15:04:05", isoTime)
		if err != nil {
			return isoTime[:min(16, len(isoTime))]
		}
	}
	return t.Local().Format("Jan 02 15:04")
}

func formatBytes(bytes int64) string {
	if bytes == 0 {
		return "—"
	}
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

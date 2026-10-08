package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/wireztna/proxy/internal/api"
)

var (
	pubsBrokerFlag string
	pubsTokenFlag  string
)

var publishersCmd = &cobra.Command{
	Use:   "publishers",
	Short: "List your accessible publishers",
	Long: `List all publishers accessible to your account.
Shows publisher IDs, names, status, and exposed CIDRs.

Use the publisher ID with 'wzctl connect --publisher <id>'.

Example:
  wzctl publishers --broker https://broker.example.com`,
	RunE: runPublishers,
}

func init() {
	publishersCmd.Flags().StringVar(&pubsBrokerFlag, "broker", "", "Broker URL (e.g., https://broker.example.com)")
	publishersCmd.Flags().StringVar(&pubsTokenFlag, "token", "", "API key (or set WIREZTNA_TOKEN env)")

	publishersCmd.MarkFlagRequired("broker")

	rootCmd.AddCommand(publishersCmd)
}

func runPublishers(cmd *cobra.Command, args []string) error {
	// Resolve token: flag > env > file
	token := pubsTokenFlag
	if token == "" {
		token = os.Getenv("WIREZTNA_TOKEN")
	}
	if token == "" {
		token = loadToken()
	}
	if token == "" {
		return fmt.Errorf("no API key provided. Use --token, WIREZTNA_TOKEN env, or run 'wzctl register --save'")
	}

	client := api.New(pubsBrokerFlag, token)
	publishers, err := client.ListPublishers()
	if err != nil {
		return fmt.Errorf("failed to list publishers: %w", err)
	}

	if len(publishers) == 0 {
		fmt.Fprintf(os.Stderr, "No publishers found.\n\n")
		fmt.Fprintf(os.Stderr, "  To enroll a publisher, run:\n")
		fmt.Fprintf(os.Stderr, "    wzctl token --broker %s --cidrs \"10.0.0.0/24\" --name my-server\n\n", pubsBrokerFlag)
		return nil
	}

	// Print table
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintf(w, "ID\tNAME\tSTATUS\tCIDRS\n")
	for _, p := range publishers {
		cidrs := strings.Join(p.ExposedCIDRs, ", ")
		if cidrs == "" {
			cidrs = "—"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Status, cidrs)
	}
	w.Flush()

	return nil
}

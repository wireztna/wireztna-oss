package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wireztna/proxy/internal/api"
)

var (
	tokenBrokerFlag       string
	tokenTokenFlag        string
	tokenNameFlag         string
	tokenCidrsFlag        string
	tokenDownloadBaseFlag string
)

const defaultDownloadBase = "https://downloads.example.com"

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Generate a publisher enrollment token",
	Long: `Generate an enrollment token for your publisher.

The output includes the full commands to download and install
the publisher on the machine that has access to your private network.

Example:
  wzctl token --broker https://broker.example.com
  wzctl token --broker https://broker.example.com --name my-server --cidrs "10.0.1.0/24,192.168.1.0/24"`,
	RunE: runToken,
}

func init() {
	tokenCmd.Flags().StringVar(&tokenBrokerFlag, "broker", "", "Broker URL (e.g., https://broker.example.com)")
	tokenCmd.Flags().StringVar(&tokenTokenFlag, "token", "", "API key (or set WIREZTNA_TOKEN env)")
	tokenCmd.Flags().StringVar(&tokenNameFlag, "name", "", "Publisher name (default: hostname at enrollment)")
	tokenCmd.Flags().StringVar(&tokenCidrsFlag, "cidrs", "", "Exposed CIDRs, comma-separated (e.g., \"10.0.1.0/24,192.168.1.0/24\")")
	tokenCmd.Flags().StringVar(&tokenDownloadBaseFlag, "download-base", "", "Base URL for publisher binary downloads (or set WZCTL_DOWNLOAD_BASE env)")

	tokenCmd.MarkFlagRequired("broker")

	rootCmd.AddCommand(tokenCmd)
}

func runToken(cmd *cobra.Command, args []string) error {
	// Resolve token: flag > env > file
	token := tokenTokenFlag
	if token == "" {
		token = os.Getenv("WIREZTNA_TOKEN")
	}
	if token == "" {
		token = loadToken()
	}
	if token == "" {
		return fmt.Errorf("no API key provided. Use --token, WIREZTNA_TOKEN env, or run 'wzctl register --save'")
	}

	// Create enrollment token
	fmt.Fprintf(os.Stderr, "[wzctl] Generating enrollment token...\n")
	client := api.New(tokenBrokerFlag, token)

	// Parse CIDRs if provided
	var cidrs []string
	if tokenCidrsFlag != "" {
		for _, c := range strings.Split(tokenCidrsFlag, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				cidrs = append(cidrs, c)
			}
		}
	}

	result, err := client.CreatePublisherToken(tokenNameFlag, cidrs)
	if err != nil {
		return fmt.Errorf("failed to create token: %w", err)
	}

	// Print instructions
	enrollURL := result.TokenURL
	if enrollURL == "" {
		enrollURL = result.Token
	}

	// Resolve download base: flag > env > default
	downloadBase := tokenDownloadBaseFlag
	if downloadBase == "" {
		downloadBase = os.Getenv("WZCTL_DOWNLOAD_BASE")
	}
	if downloadBase == "" {
		downloadBase = defaultDownloadBase
	}
	downloadBase = strings.TrimRight(downloadBase, "/")

	fmt.Fprintf(os.Stderr, "\n✓ Enrollment token created (expires in 24h)\n\n")
	fmt.Fprintf(os.Stderr, "  Run these commands on the machine with access to your private network:\n\n")
	fmt.Fprintf(os.Stderr, "  # 1. Download the publisher\n")
	fmt.Fprintf(os.Stderr, "  curl -fsSL %s/wireztna-publisher-linux-amd64 -o wireztna-publisher\n", downloadBase)
	fmt.Fprintf(os.Stderr, "  chmod +x wireztna-publisher\n\n")
	fmt.Fprintf(os.Stderr, "  # 2. Install and start (the publisher auto-associates to your account)\n")
	fmt.Fprintf(os.Stderr, "  sudo ./wireztna-publisher install --token \"%s\"\n", enrollURL)
	fmt.Fprintf(os.Stderr, "  sudo systemctl start wireztna-publisher\n\n")
	fmt.Fprintf(os.Stderr, "  After the publisher is online, use 'wzctl connect' to tunnel.\n")

	return nil
}

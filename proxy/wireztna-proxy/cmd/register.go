package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wireztna/proxy/internal/api"
)

var (
	regBrokerFlag string
	regEmailFlag  string
	regSaveFlag   bool
)

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Register a new free-tier account",
	Long: `Register a new WireZTNA free-tier account via email verification.

A verification code will be sent to your email. After verification,
an API key is generated for CLI authentication.

Example:
  wzctl register --broker https://tenant.wireztna.com --email you@example.com`,
	RunE: runRegister,
}

func init() {
	registerCmd.Flags().StringVar(&regBrokerFlag, "broker", "", "Broker URL (e.g., https://tenant.wireztna.com)")
	registerCmd.Flags().StringVar(&regEmailFlag, "email", "", "Email address for registration")
	registerCmd.Flags().BoolVar(&regSaveFlag, "save", false, "Save API key to ~/.wireztna/token")

	registerCmd.MarkFlagRequired("broker")
	registerCmd.MarkFlagRequired("email")

	rootCmd.AddCommand(registerCmd)
}

func runRegister(cmd *cobra.Command, args []string) error {
	client := api.New(regBrokerFlag, "")

	// Step 1: Request OTP
	fmt.Fprintf(os.Stderr, "[wzctl] Sending verification code to %s...\n", regEmailFlag)
	if err := client.RegisterRequest(regEmailFlag); err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[wzctl] Code sent. Check your email.\n")

	// Step 2: Prompt for OTP code
	fmt.Fprint(os.Stderr, "Enter code: ")
	reader := bufio.NewReader(os.Stdin)
	code, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}
	code = strings.TrimSpace(code)

	if code == "" {
		return fmt.Errorf("code cannot be empty")
	}

	// Step 3: Verify and create account
	fmt.Fprintf(os.Stderr, "[wzctl] Verifying...\n")
	result, err := client.RegisterVerify(regEmailFlag, code)
	if err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}

	// Step 4: Display result
	fmt.Fprintf(os.Stderr, "\n✓ Registered successfully!\n\n")
	fmt.Fprintf(os.Stderr, "  API Key: %s\n", result.APIKey)
	fmt.Fprintf(os.Stderr, "  Plan:    %s\n\n", result.Plan)
	fmt.Fprintf(os.Stderr, "  Store this key securely — it won't be shown again.\n")
	fmt.Fprintf(os.Stderr, "  Export it: export WIREZTNA_TOKEN=%s\n\n", result.APIKey)

	// Step 5: Optionally save to file
	if regSaveFlag {
		if err := saveToken(result.APIKey); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not save token: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  Token saved to ~/.wireztna/token\n")
		}
	}

	return nil
}

// saveToken writes the API key to ~/.wireztna/token with restricted permissions.
func saveToken(token string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	dir := filepath.Join(home, ".wireztna")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	tokenPath := filepath.Join(dir, "token")
	return os.WriteFile(tokenPath, []byte(token+"\n"), 0600)
}

// loadToken reads the API key from ~/.wireztna/token.
func loadToken() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	tokenPath := filepath.Join(home, ".wireztna", "token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}

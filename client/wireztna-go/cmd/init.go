package cmd

import (
	"encoding/base64"
	"crypto/rand"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/wireztna/client/internal/config"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize client configuration",
	Long: `Creates the default configuration file at ~/.wireztna/config.yaml
and optionally generates a WireGuard keypair for this client.

Run this once on a new machine before using 'wireztna connect'.`,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().Bool("generate-keys", false, "generate a WireGuard keypair")
}

func runInit(cmd *cobra.Command, args []string) error {
	if err := config.WriteDefaultConfig(); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	fmt.Println("[+] Config written to ~/.wireztna/config.yaml")

	genKeys, _ := cmd.Flags().GetBool("generate-keys")
	if genKeys {
		privKey, err := wgtypes.GeneratePrivateKey()
		if err != nil {
			return fmt.Errorf("key generation failed: %w", err)
		}
		pubKey := privKey.PublicKey()

		fmt.Println()
		fmt.Println("[+] WireGuard keypair generated:")
		fmt.Printf("    Private key: %s\n", privKey.String())
		fmt.Printf("    Public key:  %s\n", pubKey.String())
		fmt.Println()
		fmt.Println("    Add 'private_key' to your config.yaml")
		fmt.Println("    Give the public key to your WireZTNA administrator")
	}

	return nil
}

// generateRandomID creates a short random identifier (not used in WG, just for state).
func generateRandomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

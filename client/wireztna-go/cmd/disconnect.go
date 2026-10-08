package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/dns"
	"github.com/wireztna/client/internal/ipc"
	"github.com/wireztna/client/internal/tunnel"
)

var disconnectCmd = &cobra.Command{
	Use:   "disconnect",
	Short: "Disconnect from the WireZTNA network",
	Long: `Tears down the WireGuard tunnel, removes split DNS configuration,
and cleans up all tunnel-related state.`,
	RunE: runDisconnect,
}

func init() {
	rootCmd.AddCommand(disconnectCmd)
}

type disconnectIPCClient interface{ SendDisconnect() error }
type disconnectDNSManager interface{ Cleanup() error }

var (
	persistDisconnectIntent    = func() error { return config.SetUserDisconnected(true) }
	disconnectServiceAvailable = ipc.ServiceAvailable
	newDisconnectIPCClient     = func() disconnectIPCClient { return ipc.NewClient() }
	downDirectTunnel           = tunnel.DownWithEndpoint
	newDisconnectDNSManager    = func() disconnectDNSManager { return dns.NewManager() }
)

func runDisconnect(cmd *cobra.Command, args []string) error {
	// Persist intent before teardown so any background daemon sees the manual
	// disconnect before it observes the interface disappearing. Persistence is
	// defensive: a failure must never prevent the requested teardown itself.
	stateErr := persistDisconnectIntent()
	if stateErr != nil {
		fmt.Printf("warning: persist disconnect intent: %v\n", stateErr)
	}

	// If service is running, delegate via IPC.
	if disconnectServiceAvailable() {
		svc := newDisconnectIPCClient()
		disconnectErr := svc.SendDisconnect()
		if disconnectErr == nil {
			fmt.Println("[+] Disconnected")
		}
		return errors.Join(
			wrapDisconnectError("persist disconnect intent", stateErr),
			wrapDisconnectError("disconnect failed", disconnectErr),
		)
	}

	// Direct teardown (fallback)
	clientConfig := config.Load()
	ifaceName := clientConfig.Interface
	if ifaceName == "" {
		ifaceName = "wg-wireztna"
	}

	fmt.Printf("[*] Tearing down tunnel %s...\n", ifaceName)
	tunnelErr := downDirectTunnel(ifaceName, clientConfig.BrokerEndpoint)
	if tunnelErr != nil {
		fmt.Printf("warning: tunnel teardown: %v\n", tunnelErr)
	} else {
		fmt.Println("[+] Tunnel down")
	}

	// Clean up DNS
	dnsErr := newDisconnectDNSManager().Cleanup()
	if dnsErr != nil {
		fmt.Printf("warning: DNS cleanup: %v\n", dnsErr)
	} else {
		fmt.Println("[+] Split DNS cleaned up")
	}

	err := errors.Join(
		wrapDisconnectError("persist disconnect intent", stateErr),
		wrapDisconnectError("tunnel teardown", tunnelErr),
		wrapDisconnectError("DNS cleanup", dnsErr),
	)
	if err == nil {
		fmt.Println("[+] Disconnected")
	}
	return err
}

func wrapDisconnectError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

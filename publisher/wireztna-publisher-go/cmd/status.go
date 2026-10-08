package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/wireztna/publisher/internal/config"
	"github.com/wireztna/publisher/internal/service"
	"github.com/wireztna/publisher/internal/tunnel"
	"github.com/wireztna/publisher/pkg/version"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show publisher status",
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	fmt.Printf("WireZTNA Publisher v%s\n", version.Version)
	fmt.Println()

	// Enrollment state
	if !config.IsEnrolled() {
		fmt.Println("  Status:    NOT ENROLLED")
		fmt.Println("  Run 'wireztna-publisher install --token <url>' to set up")
		return nil
	}

	state, err := config.LoadState()
	if err != nil {
		return fmt.Errorf("failed to read state: %w", err)
	}

	fmt.Printf("  Publisher:  %s\n", state.PublisherName)
	fmt.Printf("  ID:         %s\n", state.PublisherID)
	fmt.Printf("  Enrolled:   %s\n", state.EnrolledAt.Format(time.RFC3339))
	fmt.Printf("  Broker:     %s\n", state.BrokerEndpoint)
	fmt.Printf("  Tunnel IP:  %s\n", state.TunnelIP)
	fmt.Printf("  Interface:  %s\n", config.DefaultWGInterface)
	fmt.Println()

	// Service status
	if service.HasSystemd() {
		if service.IsActive() {
			fmt.Println("  Service:   active (running)")
		} else if service.IsEnabled() {
			fmt.Println("  Service:   enabled (not running)")
		} else {
			fmt.Println("  Service:   not installed")
		}
	} else {
		fmt.Println("  Service:   N/A (no systemd)")
	}

	// Tunnel status
	if tunnel.IsUp() {
		fmt.Printf("  Tunnel:    UP (%s)\n", config.DefaultWGInterface)

		peerStatus, err := tunnel.GetPeerStatus()
		if err == nil {
			if peerStatus.HasHandshake {
				age := peerStatus.HandshakeAge.Round(time.Second)
				status := "healthy"
				if age > 150*time.Second {
					status = "STALE"
				}
				fmt.Printf("  Handshake: %s ago (%s)\n", age, status)
			} else {
				fmt.Println("  Handshake: never (tunnel may be broken)")
			}

			fmt.Printf("  Transfer:  RX %s / TX %s\n",
				humanBytes(peerStatus.RxBytes), humanBytes(peerStatus.TxBytes))

			if peerStatus.Endpoint != "" {
				fmt.Printf("  Endpoint:  %s\n", peerStatus.Endpoint)
			}
		}
	} else {
		fmt.Println("  Tunnel:    DOWN")
	}

	return nil
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

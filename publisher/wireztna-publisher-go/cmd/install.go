package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/wireztna/publisher/internal/config"
	"github.com/wireztna/publisher/internal/enrollment"
	"github.com/wireztna/publisher/internal/firewall"
	"github.com/wireztna/publisher/internal/service"
	"github.com/wireztna/publisher/internal/tunnel"
)

var (
	flagToken         string
	flagName          string
	flagPort          int
	flagInterval      int
	flagLocalDNS      string
	flagNoService     bool
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Enroll with broker and install as a system service",
	Long: `Performs first-time setup of the publisher:
  1. Generates a WireGuard keypair
  2. Enrolls with the control plane using the provided token
  3. Creates the WireGuard tunnel to the broker
  4. Configures firewall rules (masquerade + forwarding)
  5. Installs and starts the systemd service

Example:
  sudo wireztna-publisher install --token "http://broker.example.com/api/v1/publishers/enroll?token=abc123"`,
	RunE: runInstall,
}

func init() {
	installCmd.Flags().StringVar(&flagToken, "token", "", "Enrollment URL with token (required)")
	installCmd.Flags().StringVar(&flagName, "name", "", "Publisher name (default: hostname)")
	installCmd.Flags().IntVar(&flagPort, "port", config.DefaultWGPort, "WireGuard listen port")
	installCmd.Flags().IntVar(&flagInterval, "heartbeat-interval", config.DefaultHeartbeatInterval, "Heartbeat interval in seconds")
	installCmd.Flags().StringVar(&flagLocalDNS, "dns", "", "Local DNS server IP (auto-detected if empty)")
	installCmd.Flags().BoolVar(&flagNoService, "no-service", false, "Don't install systemd service (manual start)")
	installCmd.MarkFlagRequired("token")
}

func runInstall(cmd *cobra.Command, args []string) error {
	// Check root
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (use sudo)")
	}

	// Check if already enrolled
	if config.IsEnrolled() {
		return fmt.Errorf("publisher already enrolled. Use 'wireztna-publisher uninstall' first to re-enroll")
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  WireZTNA Publisher — Install")
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println()

	// Parse the enrollment URL
	controlPlaneURL, token, err := enrollment.ParseEnrollmentURL(flagToken)
	if err != nil {
		return fmt.Errorf("invalid token URL: %w", err)
	}

	// Build install config
	cfg := config.DefaultInstallConfig()
	cfg.ControlPlaneURL = controlPlaneURL
	cfg.EnrollmentToken = token
	cfg.WGPort = flagPort
	cfg.HeartbeatInterval = flagInterval
	cfg.LocalDNS = flagLocalDNS
	if flagName != "" {
		cfg.PublisherName = flagName
	}

	// 1. Enable IP forwarding
	fmt.Println("[*] Enabling IP forwarding...")
	if err := firewall.EnableIPForwarding(); err != nil {
		return err
	}
	fmt.Println("[✓] IP forwarding enabled")

	// 2. Enroll
	state, privateKey, err := enrollment.Enroll(cfg)
	if err != nil {
		return err
	}

	// 3. Create WireGuard tunnel
	fmt.Println("[*] Creating WireGuard tunnel...")
	if err := tunnel.Create(state, privateKey, cfg.WGPort); err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}
	fmt.Println("[✓] Tunnel active")

	// 4. Configure firewall
	if err := firewall.Setup(); err != nil {
		return fmt.Errorf("failed to configure firewall: %w", err)
	}

	// 5. Install systemd service (unless --no-service)
	if !flagNoService {
		if !service.HasSystemd() {
			fmt.Println("[WARN] systemd not found — skipping service installation")
			fmt.Println("       Run 'wireztna-publisher start' manually to start the daemon")
		} else {
			fmt.Println("[*] Installing systemd service...")
			if err := service.Install(); err != nil {
				return fmt.Errorf("failed to install service: %w", err)
			}
			fmt.Println("[✓] Service installed and started")
		}
	} else {
		fmt.Println("[*] Skipping service installation (--no-service)")
		fmt.Println("    Run 'wireztna-publisher start' to start the daemon manually")
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  Publisher installed successfully!")
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("  Publisher ID:  %s\n", state.PublisherID)
	fmt.Printf("  Tunnel IP:     %s\n", state.TunnelIP)
	fmt.Printf("  Broker:        %s\n", state.BrokerEndpoint)
	fmt.Printf("  Interface:     %s\n", config.DefaultWGInterface)
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Println("    1. Go to the admin UI → Publishers")
	fmt.Println("    2. Set exposed_cidrs for this publisher")
	fmt.Println("    3. Assign the publisher to a group")
	fmt.Println()

	return nil
}

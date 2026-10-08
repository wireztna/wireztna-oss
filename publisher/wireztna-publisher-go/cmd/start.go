package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wireztna/publisher/internal/config"
	"github.com/wireztna/publisher/internal/firewall"
	"github.com/wireztna/publisher/internal/heartbeat"
	"github.com/wireztna/publisher/internal/tunnel"
	"github.com/wireztna/publisher/internal/watchdog"
	"github.com/wireztna/publisher/pkg/version"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the publisher daemon (used by systemd or manually)",
	Long: `Starts the publisher daemon in foreground mode:
  1. Ensures WireGuard tunnel is up
  2. Configures firewall rules
  3. Starts heartbeat reporter
  4. Starts watchdog for auto-recovery
  5. Runs until interrupted (SIGTERM/SIGINT)

This command is called by the systemd service. You can also run it directly
for debugging or when --no-service was used during install.`,
	RunE: runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (use sudo)")
	}

	// Load enrollment state
	state, err := config.LoadState()
	if err != nil {
		return fmt.Errorf("publisher not enrolled. Run 'wireztna-publisher install' first")
	}

	// Auto-upgrade: if enrolled before auth was added, request an API key
	if state.PublisherApiKey == "" {
		fmt.Println("[*] No API key found — attempting auto-upgrade...")
		if key, err := requestUpgradeKey(state); err != nil {
			fmt.Printf("[WARN] Auto-upgrade failed: %v (will retry on next start)\n", err)
		} else {
			state.PublisherApiKey = key
			if err := config.SaveState(state); err != nil {
				fmt.Printf("[WARN] Failed to persist API key: %v\n", err)
			} else {
				fmt.Printf("[✓] API key obtained and saved (wpk_...%s)\n", key[len(key)-6:])
			}
		}
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  WireZTNA Publisher Agent v%s\n", version.Version)
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Publisher: %s (%s)\n", state.PublisherName, state.PublisherID)
	fmt.Printf("  Broker:    %s\n", state.BrokerEndpoint)
	fmt.Println()

	// 1. Enable IP forwarding
	if err := firewall.EnableIPForwarding(); err != nil {
		fmt.Printf("[WARN] Failed to enable IP forwarding: %v\n", err)
	}

	// 2. Ensure tunnel is up
	if tunnel.IsUp() {
		fmt.Printf("[✓] Tunnel already active (%s)\n", config.DefaultWGInterface)
	} else {
		fmt.Println("[*] Starting WireGuard tunnel...")
		privateKey, err := config.LoadPrivateKey()
		if err != nil {
			return fmt.Errorf("failed to load private key: %w", err)
		}
		if err := tunnel.Create(state, privateKey, config.DefaultWGPort); err != nil {
			return fmt.Errorf("failed to create tunnel: %w", err)
		}
		fmt.Println("[✓] Tunnel started")
	}

	// 3. Ensure firewall rules
	if err := firewall.Setup(); err != nil {
		fmt.Printf("[WARN] Failed to configure firewall: %v\n", err)
	}

	// 4. Start heartbeat reporter
	reporter := heartbeat.NewReporter(state, config.DefaultHeartbeatInterval)
	go reporter.Start()

	// 5. Start watchdog
	wd := watchdog.New(state, config.DefaultWatchdogThreshold, config.DefaultHeartbeatInterval)
	go wd.Start()

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  Publisher running. Waiting for traffic...")
	fmt.Println("═══════════════════════════════════════════")

	// 6. Wait for shutdown signal
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	<-ctx.Done()

	fmt.Println()
	fmt.Println("[*] Shutting down publisher...")
	reporter.Stop()
	wd.Stop()
	// Don't destroy the tunnel on stop — leave it active for quick restart
	fmt.Println("[✓] Publisher stopped (tunnel left active for quick restart)")

	return nil
}

// requestUpgradeKey calls POST /api/v1/publishers/{id}/upgrade-key to obtain
// an API key for a legacy publisher that was enrolled before auth was added.
// The endpoint validates that the caller's IP matches the publisher's registered
// endpoint, so this only works from the actual publisher host.
func requestUpgradeKey(state *config.PublisherState) (string, error) {
	url := fmt.Sprintf("%s/api/v1/publishers/%s/upgrade-key",
		state.ControlPlaneURL, state.PublisherID)

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 409 {
		return "", fmt.Errorf("publisher already has a key (use admin rotate-key to replace)")
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		PublisherApiKey string `json:"publisher_api_key"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	if result.PublisherApiKey == "" {
		return "", fmt.Errorf("empty API key in response")
	}

	return result.PublisherApiKey, nil
}

package cmd

import (
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/wireztna/proxy/internal/display"
	"github.com/wireztna/proxy/internal/relay"
	"github.com/wireztna/proxy/internal/tunnel"
)

var (
	passFlag      string
	brokerFlag    string
	localPortFlag int
	urlFlag       string
	ttlFlag       int
)

var rootCmd = &cobra.Command{
	Use:   "wzctl",
	Short: "WireZTNA CLI — delegated access for AI agents and collaborators",
	Long: `wzctl exposes a remote resource as a localhost TCP listener
using a delegated access pass. It connects to the broker via WebSocket
and relays traffic bidirectionally.

Each local TCP connection opens a new WebSocket session to the broker,
which in turn opens a TCP connection to the target resource.

Usage:
  wzctl --pass <pass_id> --broker <broker_url> --local-port <port>
  wzctl --url <connection_url> --local-port <port>
  wzctl register --broker <url> --email <email>
  wzctl connect --broker <url> --publisher <name> --target <ip> --port <port> --local-port <port>`,
	RunE: runProxy,
}

func init() {
	rootCmd.Flags().StringVar(&passFlag, "pass", "", "Access pass ID (e.g., dap_7f3a9c)")
	rootCmd.Flags().StringVar(&brokerFlag, "broker", "", "Broker WebSocket URL (e.g., wss://tenant.wireztna.com)")
	rootCmd.Flags().IntVar(&localPortFlag, "local-port", 0, "Local TCP port to listen on (required)")
	rootCmd.Flags().StringVar(&urlFlag, "url", "", "Full connection URL (alternative to --pass + --broker)")
	rootCmd.Flags().IntVar(&ttlFlag, "ttl", 0, "Pass TTL in seconds (for countdown display; auto-detected if omitted)")

	rootCmd.MarkFlagRequired("local-port")
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runProxy(cmd *cobra.Command, args []string) error {
	// Validate inputs
	if urlFlag == "" && passFlag == "" {
		return fmt.Errorf("either --url or --pass is required")
	}
	if urlFlag == "" && brokerFlag == "" {
		return fmt.Errorf("--broker is required when using --pass")
	}
	if err := relay.ValidateLocalPort(localPortFlag); err != nil {
		return err
	}

	// Resolve connection parameters
	var wsURL string
	var passID string

	if urlFlag != "" {
		wsURL = urlFlag
		// Extract pass_id from URL path
		u, err := url.Parse(urlFlag)
		if err != nil {
			return fmt.Errorf("invalid connection URL: %w", err)
		}
		parts := strings.Split(strings.TrimSuffix(u.Path, "/"), "/")
		if len(parts) > 0 {
			passID = parts[len(parts)-1]
		}
	} else {
		passID = passFlag
		// Build WebSocket URL
		u, err := url.Parse(brokerFlag)
		if err != nil {
			return fmt.Errorf("invalid broker URL: %w", err)
		}
		u.Path = fmt.Sprintf("/api/v1/tunnel/%s", passID)
		if u.Scheme == "https" {
			u.Scheme = "wss"
		} else if u.Scheme == "http" {
			u.Scheme = "ws"
		}
		wsURL = u.String()
	}

	// Validate the pass exists (quick test connection)
	fmt.Fprintf(os.Stderr, "[wzctl] Connecting to broker...\n")

	testTunnel, err := tunnel.Connect(tunnel.Config{
		BrokerURL: wsURL,
		PassID:    passID,
	})
	if err != nil {
		if te, ok := err.(*tunnel.TunnelError); ok {
			fmt.Fprintf(os.Stderr, "[wzctl] Error: %s\n", te.Message)
			os.Exit(te.Code.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "[wzctl] Error: %s\n", err)
		os.Exit(1)
	}
	// Close test connection immediately — real connections open per-TCP
	testTunnel.Close()
	fmt.Fprintf(os.Stderr, "[wzctl] Pass validated.\n")

	// Start local TCP listener (each connection opens its own WS)
	r := relay.New(
		relay.Config{LocalPort: localPortFlag},
		relay.TunnelConfig{BrokerURL: wsURL, PassID: passID},
	)
	if err := r.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "[wzctl] Error: %s\n", err)
		os.Exit(1)
	}
	defer r.Stop()

	// Display — use TTL flag or query the API for time_remaining
	var expiresAt time.Time
	if ttlFlag > 0 {
		expiresAt = time.Now().Add(time.Duration(ttlFlag) * time.Second)
	} else {
		// Query pass info from API to get real TTL
		ttl := tunnel.QueryPassTTL(wsURL, passID)
		if ttl > 0 {
			expiresAt = time.Now().Add(time.Duration(ttl) * time.Second)
		} else {
			expiresAt = time.Now().Add(30 * time.Minute) // Fallback
		}
	}
	d := display.New(expiresAt)
	d.PrintStartup(r.Addr(), passID, passID)
	d.StartCountdown()
	defer d.Stop()

	// Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	fmt.Fprintf(os.Stderr, "\n[wzctl] Received %s, shutting down...\n", sig)
	d.PrintDisconnect("user interrupted", r.BytesIn.Load(), r.BytesOut.Load())
	return nil
}

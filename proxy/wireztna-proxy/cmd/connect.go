package cmd

import (
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/wireztna/proxy/internal/api"
	"github.com/wireztna/proxy/internal/display"
	"github.com/wireztna/proxy/internal/relay"
	"github.com/wireztna/proxy/internal/tunnel"
)

var (
	connBrokerFlag    string
	connTokenFlag     string
	connPublisherFlag string
	connTargetFlag    string
	connPortFlag      int
	connTTLFlag       string
	connLocalPortFlag int
	connDaemonFlag    bool
	connPassFlag      string
)

var connectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Create a pass and open a tunnel in one step",
	Long: `Open a tunnel to a private resource. Creates a new access pass automatically,
or reuses an existing one with --pass.

If --pass is provided, skips pass creation and connects directly using that pass.
This is useful to retry a connection within the same TTL window without consuming
a new pass from your quota.

Examples:
  # Create a new pass and connect
  wzctl connect --broker https://broker.example.com \
    --publisher pub_abc123 --target 10.0.2.30 --port 5432 \
    --ttl 30m --local-port 5432

  # Reuse an existing pass (no new pass created)
  wzctl connect --broker https://broker.example.com \
    --pass dap_8qcn3m --local-port 5432`,
	RunE: runConnect,
}

func init() {
	connectCmd.Flags().StringVar(&connBrokerFlag, "broker", "", "Broker URL (e.g., https://tenant.wireztna.com)")
	connectCmd.Flags().StringVar(&connTokenFlag, "token", "", "API key (or set WIREZTNA_TOKEN env)")
	connectCmd.Flags().StringVar(&connPublisherFlag, "publisher", "", "Publisher name or ID")
	connectCmd.Flags().StringVar(&connTargetFlag, "target", "", "Target IP or CIDR (e.g., 10.0.2.30)")
	connectCmd.Flags().IntVar(&connPortFlag, "port", 0, "Target port (e.g., 5432)")
	connectCmd.Flags().StringVar(&connTTLFlag, "ttl", "30m", "Pass TTL (e.g., 30m, 1h)")
	connectCmd.Flags().IntVar(&connLocalPortFlag, "local-port", 0, "Local TCP port to listen on")
	connectCmd.Flags().BoolVar(&connDaemonFlag, "daemon", false, "Run in background (for CI/CD, Docker, AI agents)")
	connectCmd.Flags().StringVar(&connPassFlag, "pass", "", "Reuse an existing pass ID (skips pass creation)")

	connectCmd.MarkFlagRequired("broker")
	connectCmd.MarkFlagRequired("local-port")

	rootCmd.AddCommand(connectCmd)
}

func runConnect(cmd *cobra.Command, args []string) error {
	// Resolve token: flag > env > file
	token := connTokenFlag
	if token == "" {
		token = os.Getenv("WIREZTNA_TOKEN")
	}
	if token == "" {
		token = loadToken()
	}
	if token == "" {
		return fmt.Errorf("no API key provided. Use --token, WIREZTNA_TOKEN env, or run 'wzctl register --save'")
	}

	// Validate local port
	if err := relay.ValidateLocalPort(connLocalPortFlag); err != nil {
		return err
	}

	var passID string
	var ttlRemaining int

	if connPassFlag != "" {
		// ─── Reuse existing pass ───
		passID = connPassFlag
		fmt.Fprintf(os.Stderr, "[wzctl] Using existing pass: %s\n", passID)

		// Query TTL for the existing pass
		ttlRemaining = 1800 // Default fallback
		// Try to get actual TTL from the API
		client := api.New(connBrokerFlag, token)
		passes, err := client.ListPasses()
		if err == nil {
			for _, p := range passes {
				if p.PassID == passID {
					if p.Status != "active" {
						return fmt.Errorf("pass %s is not active (status: %s). Create a new one with: wzctl connect --publisher ... --target ... --port ...", passID, p.Status)
					}
					// Calculate remaining from expires_at
					expAt, err := time.Parse(time.RFC3339Nano, p.ExpiresAt)
					if err == nil {
						ttlRemaining = int(time.Until(expAt).Seconds())
						if ttlRemaining <= 0 {
							return fmt.Errorf("pass %s has expired. Create a new one.", passID)
						}
					}
					break
				}
			}
		}

		fmt.Fprintf(os.Stderr, "[wzctl] Pass TTL remaining: %s\n", formatDuration(ttlRemaining))
	} else {
		// ─── Create new pass ───
		// Validate required flags for pass creation
		if connPublisherFlag == "" {
			return fmt.Errorf("--publisher is required (or use --pass to reuse an existing pass)")
		}
		if connTargetFlag == "" {
			return fmt.Errorf("--target is required (or use --pass to reuse an existing pass)")
		}
		if connPortFlag == 0 {
			return fmt.Errorf("--port is required (or use --pass to reuse an existing pass)")
		}

		// Parse TTL
		ttlSeconds, err := parseTTL(connTTLFlag)
		if err != nil {
			return fmt.Errorf("invalid --ttl: %w", err)
		}

		// Normalize target (add /32 if bare IP)
		target := connTargetFlag
		if !strings.Contains(target, "/") {
			target = target + "/32"
		}

		// Create access pass via API
		fmt.Fprintf(os.Stderr, "[wzctl] Creating access pass...\n")
		client := api.New(connBrokerFlag, token)

		passResp, err := client.CreatePass(api.CreatePassRequest{
			Label: fmt.Sprintf("cli-%d", time.Now().Unix()),
			Scope: api.PassScope{
				Publishers: []string{connPublisherFlag},
				CIDRs:      []string{target},
				Ports:      []int{connPortFlag},
			},
			TTLSeconds: ttlSeconds,
		})
		if err != nil {
			return fmt.Errorf("failed to create pass: %w", err)
		}

		passID = passResp.PassID
		ttlRemaining = passResp.TimeRemainingSeconds
		fmt.Fprintf(os.Stderr, "[wzctl] Pass created (%s) — expires in %s\n",
			passID, formatDuration(ttlRemaining))
	}

	// Build WebSocket URL
	wsURL := ""
	u, err := url.Parse(connBrokerFlag)
	if err != nil {
		return fmt.Errorf("invalid broker URL: %w", err)
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = fmt.Sprintf("/api/v1/tunnel/%s", passID)
	wsURL = u.String()

	// Step 3: Validate tunnel (same as root command)
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
		return fmt.Errorf("tunnel connection failed: %w", err)
	}
	testTunnel.Close()

	// Step 4: Start relay
	r := relay.New(
		relay.Config{LocalPort: connLocalPortFlag},
		relay.TunnelConfig{BrokerURL: wsURL, PassID: passID},
	)
	if err := r.Start(); err != nil {
		return fmt.Errorf("failed to start listener: %w", err)
	}

	// Daemon mode: print connection info and exit (relay runs in background goroutine)
	if connDaemonFlag {
		fmt.Fprintf(os.Stderr, "[wzctl] Tunnel open: localhost:%d → %s:%d\n", connLocalPortFlag, connTargetFlag, connPortFlag)
		fmt.Fprintf(os.Stderr, "[wzctl] Pass: %s (expires in %s)\n", passID, formatDuration(ttlRemaining))
		fmt.Fprintf(os.Stderr, "[wzctl] Running in background. Stop with: kill $(cat /tmp/wzctl-%d.pid)\n", connLocalPortFlag)

		// Write PID file
		pidFile := fmt.Sprintf("/tmp/wzctl-%d.pid", connLocalPortFlag)
		os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0644)

		// Block until TTL expires or signal (but don't print countdown)
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

		timer := time.NewTimer(time.Duration(ttlRemaining) * time.Second)
		select {
		case <-sigCh:
			fmt.Fprintf(os.Stderr, "[wzctl] Shutting down...\n")
		case <-timer.C:
			fmt.Fprintf(os.Stderr, "[wzctl] Pass expired, disconnecting.\n")
		}

		os.Remove(pidFile)
		r.Stop()
		return nil
	}

	// Foreground mode: display countdown
	defer r.Stop()

	expiresAt := time.Now().Add(time.Duration(ttlRemaining) * time.Second)
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

// parseTTL converts human-readable duration (30m, 1h, 90s) to seconds.
func parseTTL(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 1800, nil // Default 30m
	}

	// Try pure number (assume seconds)
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}

	// Parse duration suffix
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}

	suffix := s[len(s)-1]
	numStr := s[:len(s)-1]
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}

	switch suffix {
	case 's':
		return num, nil
	case 'm':
		return num * 60, nil
	case 'h':
		return num * 3600, nil
	default:
		return 0, fmt.Errorf("unknown duration suffix: %c (use s, m, or h)", suffix)
	}
}

// formatDuration formats seconds as human-readable (e.g., "30m", "1h30m").
func formatDuration(seconds int) string {
	if seconds <= 0 {
		return "0s"
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60

	if h > 0 && m > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%dh", h)
	}
	if m > 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%ds", s)
}

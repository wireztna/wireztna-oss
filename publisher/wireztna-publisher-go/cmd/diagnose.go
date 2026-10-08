package cmd

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/wireztna/publisher/internal/config"
	"github.com/wireztna/publisher/internal/service"
	"github.com/wireztna/publisher/internal/tunnel"
)

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose",
	Short: "Run self-diagnostic checks",
	Long: `Validates the publisher's health:
  1. Enrollment state
  2. WireGuard tunnel (handshake, traffic)
  3. Control plane connectivity
  4. IP forwarding
  5. Service status

Exit codes:
  0 = All checks passed
  1 = Critical failures
  2 = Warnings only`,
	RunE: runDiagnose,
}

type diagResult struct {
	passCount int
	warnCount int
	failCount int
}

func (d *diagResult) pass(name, detail string) {
	d.passCount++
	fmt.Printf("  \033[32m[PASS]\033[0m %s — %s\n", name, detail)
}

func (d *diagResult) warn(name, detail string) {
	d.warnCount++
	fmt.Printf("  \033[33m[WARN]\033[0m %s — %s\n", name, detail)
}

func (d *diagResult) fail(name, detail string) {
	d.failCount++
	fmt.Printf("  \033[31m[FAIL]\033[0m %s — %s\n", name, detail)
}

func runDiagnose(cmd *cobra.Command, args []string) error {
	r := &diagResult{}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  WireZTNA Publisher Diagnostic — %s\n", time.Now().Format(time.RFC3339))
	fmt.Println("═══════════════════════════════════════════")

	// ─── 1. Enrollment ───
	fmt.Println()
	fmt.Println("\033[34m━━━ 1. Enrollment State ━━━\033[0m")

	var state *config.PublisherState
	if !config.IsEnrolled() {
		r.fail("Enrollment", "Not enrolled — run 'wireztna-publisher install' first")
	} else {
		var err error
		state, err = config.LoadState()
		if err != nil {
			r.fail("State file", fmt.Sprintf("Cannot read state: %v", err))
		} else {
			r.pass("Enrollment", fmt.Sprintf("Enrolled at %s", state.EnrolledAt.Format(time.RFC3339)))
			r.pass("Publisher ID", state.PublisherID)
		}
	}

	// Private key
	if _, err := os.Stat(config.PrivateKeyPath()); err == nil {
		info, _ := os.Stat(config.PrivateKeyPath())
		if info != nil && info.Mode().Perm() == 0600 {
			r.pass("Private key", "Exists with correct permissions (0600)")
		} else {
			r.warn("Private key", "Exists but permissions may be too open")
		}
	} else {
		r.fail("Private key", "Not found")
	}

	// ─── 2. WireGuard Tunnel ───
	fmt.Println()
	fmt.Println("\033[34m━━━ 2. WireGuard Tunnel ━━━\033[0m")

	if tunnel.IsUp() {
		r.pass("Interface", fmt.Sprintf("%s is UP", config.DefaultWGInterface))

		peerStatus, err := tunnel.GetPeerStatus()
		if err == nil {
			r.pass("Broker peer", fmt.Sprintf("Configured (pubkey: %s...)", truncateStr(peerStatus.PublicKey, 12)))

			if peerStatus.HasHandshake {
				age := peerStatus.HandshakeAge.Round(time.Second)
				if age < 150*time.Second {
					r.pass("Handshake", fmt.Sprintf("Last %s ago — tunnel ALIVE", age))
				} else if age < 300*time.Second {
					r.warn("Handshake", fmt.Sprintf("Last %s ago — getting stale", age))
				} else {
					r.fail("Handshake", fmt.Sprintf("Last %s ago — tunnel DEAD", age))
				}
			} else {
				r.fail("Handshake", "No handshake ever completed — broker unreachable or key mismatch")
			}

			if peerStatus.RxBytes == 0 && peerStatus.TxBytes == 0 {
				r.warn("Traffic", "Zero bytes transferred — tunnel active but no data")
			} else {
				r.pass("Traffic", fmt.Sprintf("RX: %s / TX: %s",
					humanBytes(peerStatus.RxBytes), humanBytes(peerStatus.TxBytes)))
			}

			if peerStatus.Endpoint != "" {
				r.pass("Endpoint", peerStatus.Endpoint)
			}
		} else {
			r.fail("Peer status", fmt.Sprintf("Cannot read: %v", err))
		}
	} else {
		r.fail("Interface", fmt.Sprintf("%s is DOWN or missing", config.DefaultWGInterface))
	}

	// ─── 3. Control Plane ───
	fmt.Println()
	fmt.Println("\033[34m━━━ 3. Control Plane Connectivity ━━━\033[0m")

	if state != nil {
		client := &http.Client{Timeout: 5 * time.Second}
		healthURL := fmt.Sprintf("%s/health", state.ControlPlaneURL)
		resp, err := client.Get(healthURL)
		if err != nil {
			r.fail("API reachable", fmt.Sprintf("Cannot reach %s: %v", state.ControlPlaneURL, err))
		} else {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				r.pass("API reachable", fmt.Sprintf("HTTP 200 from %s", healthURL))
			} else {
				r.warn("API reachable", fmt.Sprintf("HTTP %d from %s", resp.StatusCode, healthURL))
			}
		}
	} else {
		r.fail("API reachable", "Cannot test — not enrolled")
	}

	// ─── 4. IP Forwarding ───
	fmt.Println()
	fmt.Println("\033[34m━━━ 4. IP Forwarding ━━━\033[0m")

	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err == nil && len(data) > 0 && data[0] == '1' {
		r.pass("IP forwarding", "Enabled")
	} else {
		r.fail("IP forwarding", "DISABLED — publisher cannot relay traffic")
	}

	// ─── 5. Service ───
	fmt.Println()
	fmt.Println("\033[34m━━━ 5. Service Status ━━━\033[0m")

	if service.HasSystemd() {
		if service.IsActive() {
			r.pass("Service", "Running")
		} else if service.IsEnabled() {
			r.warn("Service", "Enabled but not running")
		} else {
			r.warn("Service", "Not installed (use 'wireztna-publisher install')")
		}
	} else {
		r.warn("Service", "systemd not available — cannot check service status")
	}

	// ─── Summary ───
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  RESULTS: \033[32m%d passed\033[0m, \033[33m%d warnings\033[0m, \033[31m%d failures\033[0m\n",
		r.passCount, r.warnCount, r.failCount)
	fmt.Println("═══════════════════════════════════════════")

	if r.failCount > 0 {
		fmt.Println()
		fmt.Println("  Troubleshooting tips:")
		fmt.Println("  • Not enrolled       → Run 'wireztna-publisher install --token <url>'")
		fmt.Println("  • No handshake       → Check broker is running, UDP port open, keys correct")
		fmt.Println("  • Tunnel dead        → Run 'systemctl restart wireztna-publisher'")
		fmt.Println("  • IP forwarding off  → Run 'sysctl -w net.ipv4.ip_forward=1'")
		fmt.Println("  • API unreachable    → Check network between publisher and broker")
		os.Exit(1)
	}

	if r.warnCount > 0 {
		os.Exit(2)
	}

	return nil
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

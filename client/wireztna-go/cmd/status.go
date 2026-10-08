package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/ipc"
	"github.com/wireztna/client/internal/tunnel"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show connection status",
	Long: `Displays the current tunnel state, session information,
and connectivity details.

Shows:
  - Tunnel interface state (up/down)
  - WireGuard handshake age
  - Session expiry time and TTL remaining
  - Selected group/project
  - Transfer statistics (rx/tx bytes)`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	// If service is running, get status from it via IPC
	if ipc.ServiceAvailable() {
		svc := ipc.NewClient()
		state, err := svc.SendStatus()
		if err != nil {
			return fmt.Errorf("cannot reach service: %w", err)
		}
		if state.Status == ipc.StatusConnected {
			fmt.Println("Status: connected")
			fmt.Printf("  Overlay IP: %s\n", state.OverlayIP)
			fmt.Printf("  Endpoint:   %s\n", state.Endpoint)
			if !state.LastHandshake.IsZero() && time.Since(state.LastHandshake) < 24*time.Hour {
				fmt.Printf("  Handshake:  %s ago\n", time.Since(state.LastHandshake).Round(time.Second))
			}
			fmt.Printf("  Transfer:   ↓ %s / ↑ %s\n", formatBytes(state.RxBytes), formatBytes(state.TxBytes))
			if state.IsExitNode {
				fmt.Printf("  Mode:       VPN (%s)\n", state.ExitNodeName)
			} else {
				fmt.Printf("  Mode:       Split Tunnel\n")
			}
			if state.GroupName != "" {
				fmt.Printf("  Project:    %s\n", state.GroupName)
			}
			if !state.ExpiresAt.IsZero() {
				remaining := time.Until(state.ExpiresAt)
				fmt.Printf("  Session:    expires in %s\n", remaining.Round(time.Minute))
			}
		} else {
			fmt.Printf("Status: %s\n", state.Status)
			if state.NeedsEnroll {
				fmt.Println("  Device not enrolled. Run: wireztna enroll \"<url>\"")
			} else if state.NeedsLogin {
				fmt.Println("  Not authenticated. Run: wireztna login")
			} else if state.ErrorMessage != "" {
				fmt.Printf("  Error: %s\n", state.ErrorMessage)
			}
		}
		return nil
	}

	// Fallback: direct interface check
	ifaceName := viper.GetString("interface")
	if ifaceName == "" {
		ifaceName = "wg-wireztna"
	}

	// Check tunnel state
	info, err := tunnel.GetStatus(ifaceName)
	if err != nil {
		fmt.Println("Status: disconnected")
		fmt.Printf("  (no tunnel interface '%s' found)\n", ifaceName)
		return nil
	}

	fmt.Println("Status: connected")
	fmt.Printf("  Interface:  %s\n", ifaceName)
	fmt.Printf("  Overlay IP: %s\n", info.OverlayIP)
	fmt.Printf("  Endpoint:   %s\n", info.Endpoint)

	if info.LastHandshake.IsZero() {
		fmt.Println("  Handshake:  never")
	} else {
		age := time.Since(info.LastHandshake)
		status := "healthy"
		if age > 180*time.Second {
			status = "STALE"
		}
		fmt.Printf("  Handshake:  %s ago (%s)\n", age.Round(time.Second), status)
	}

	fmt.Printf("  Transfer:   ↓ %s / ↑ %s\n",
		formatBytes(info.RxBytes), formatBytes(info.TxBytes))

	// Check session from control plane
	apiURL := viper.GetString("api_url")
	if apiURL != "" {
		token, _ := config.LoadToken()
		if token != "" && !config.IsTokenExpired(token) {
			client := api.NewClient(apiURL)
			client.SetToken(token)
			sess, err := client.GetSessionInfo()
			if err == nil {
				fmt.Println()
				fmt.Println("Session:")
				fmt.Printf("  ID:        %s\n", sess.SessionID[:8]+"...")
				fmt.Printf("  Expires:   %s\n", sess.ExpiresAt.Time.Local().Format("2006-01-02 15:04:05"))
				fmt.Printf("  Remaining: %s\n", (time.Duration(sess.TTLRemaining) * time.Second).String())
				if sess.SelectedGroupName != "" {
					fmt.Printf("  Project:   %s\n", sess.SelectedGroupName)
				}
			}
		}
	}

	return nil
}

func formatBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

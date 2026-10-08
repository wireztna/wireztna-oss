package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/daemon"
	"github.com/wireztna/client/internal/dns"
	"github.com/wireztna/client/internal/elevation"
	"github.com/wireztna/client/internal/ipc"
	"github.com/wireztna/client/internal/tunnel"
)

var (
	saveDirectState = config.SaveStateForLifecycle
)

var connectCmd = &cobra.Command{
	Use:   "connect",
	Short: "Connect to the WireZTNA network",
	Long: `Establishes a WireGuard tunnel to the WireZTNA broker.

The full connection flow:
  1. Authenticate (or use cached token)
  2. Check for CIDR overlap and select group if needed
  3. Renew session (obtain fresh PSK)
  4. Create WireGuard tunnel interface
  5. Configure split DNS
  6. Verify connectivity to broker
  7. Start background renewal daemon

The tunnel stays active until 'wireztna disconnect' is called or
the process receives SIGINT/SIGTERM.`,
	RunE: runConnect,
}

func init() {
	rootCmd.AddCommand(connectCmd)
	connectCmd.Flags().StringP("group", "g", "", "select group/project (for CIDR overlap)")
	connectCmd.Flags().String("exit-node", "", "select publisher as VPN exit node (full tunnel)")
	connectCmd.Flags().Bool("no-daemon", false, "don't run background renewal (single-shot)")
	connectCmd.Flags().Duration("renew-before", 30*time.Minute, "renew session this long before expiry")

	viper.BindPFlag("group", connectCmd.Flags().Lookup("group"))
	viper.BindPFlag("exit-node", connectCmd.Flags().Lookup("exit-node"))
}

func runConnect(cmd *cobra.Command, args []string) error {
	apiURL := viper.GetString("api_url")
	if apiURL == "" {
		return fmt.Errorf("API URL required: set --api-url, WIREZTNA_API_URL, or api_url in config")
	}

	// On Windows: always prefer the helper process for tunnel management.
	// The helper runs elevated in the user's session (not Session 0) and manages
	// the tunnel. CLI/TUI/tray communicate via IPC without needing admin.
	if runtime.GOOS == "windows" {
		// If helper is already running, delegate
		if ipc.ServiceAvailable() {
			return runConnectViaIPC(cmd)
		}
		// If not elevated, launch helper (UAC)
		if !elevation.IsElevated() {
			return elevateAndConnect(cmd)
		}
		// If elevated but no helper → we ARE being run directly as admin
		// (e.g., user opened admin PowerShell and typed wireztna connect)
		// Fall through to direct tunnel creation below
	}

	verbose, _ := cmd.Flags().GetBool("verbose")
	noDaemon, _ := cmd.Flags().GetBool("no-daemon")
	renewBefore, _ := cmd.Flags().GetDuration("renew-before")
	ifaceName := viper.GetString("interface")
	if ifaceName == "" {
		ifaceName = "wg-wireztna"
	}

	// Step 1: Get or refresh token
	token, err := config.LoadToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated — run 'wireztna login' first")
	}

	// Validate token is not expired
	if config.IsTokenExpired(token) {
		return fmt.Errorf("token expired — run 'wireztna login' to re-authenticate")
	}

	// Create cancellation before the first blocking lifecycle request.
	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	// This command is an explicit request to connect and owns a durable
	// generation used to reject stale/cancelled commits.
	generation, err := config.BeginLifecycle(false)
	if err != nil {
		return fmt.Errorf("persist connect intent: %w", err)
	}

	client := api.NewClient(apiURL)
	client.SetToken(token)

	if verbose {
		fmt.Println("[*] Checking available groups...")
	}

	// Step 2: Group/exit-node selection
	groupID := viper.GetString("group")
	exitNodeID := viper.GetString("exit-node")
	if groupID == "" && exitNodeID == "" {
		groups, err := client.GetAvailableGroupsContext(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not check groups: %v\n", err)
		} else if groups.VPNMode && len(groups.ExitNodes) > 0 {
			// VPN mode user with exit nodes: show exit node selector
			selectedExitID, err := promptExitNodeSelection(groups)
			if err != nil {
				return err
			}
			exitNodeID = selectedExitID
		} else if groups.SelectionRecommended {
			selectedID, err := promptGroupSelection(groups)
			if err != nil {
				return err
			}
			groupID = selectedID
		}
	}
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle)
	}

	// Step 3: Renew session (get PSK)
	if verbose {
		fmt.Println("[*] Requesting session from control plane...")
	}
	session, err := client.RenewSessionContext(api.WithLifecycleGeneration(ctx, generation), groupID, exitNodeID)
	if err != nil {
		return fmt.Errorf("session renewal failed: %w", err)
	}
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle)
	}
	fmt.Printf("[+] Session active (TTL: %ds, expires: %s)\n",
		session.TTLSeconds, session.ExpiresAt.Time.Local().Format("15:04:05"))

	// Step 4: Get DNS zones for split DNS. Failing to resolve the intended
	// policy is a failed connection, not a successful tunnel with wrong DNS.
	zones, err := client.GetDNSZonesContext(ctx)
	if err != nil {
		return fmt.Errorf("fetch DNS zones: %w", err)
	}
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle)
	}

	// Step 5: Create WireGuard tunnel
	if verbose {
		fmt.Println("[*] Creating WireGuard tunnel...")
	}

	cfg := config.Load()

	// Use dynamic AllowedIPs from session renew (reflects current group/publisher CIDRs)
	// Falls back to static config if server didn't return any
	allowedIPs := cfg.AllowedIPs
	if len(session.AllowedIPs) > 0 {
		allowedIPs = session.AllowedIPs
	}

	tun, err := tunnel.New(tunnel.Config{
		InterfaceName:  ifaceName,
		PrivateKey:     cfg.PrivateKey,
		OverlayIP:      cfg.OverlayIP,
		BrokerPubKey:   cfg.BrokerPublicKey,
		BrokerEndpoint: cfg.BrokerEndpoint,
		PresharedKey:   session.PresharedKey,
		AllowedIPs:     allowedIPs,
		DNS:            cfg.TunnelDNS,
		IsExitNode:     session.IsExitNode,
		LogFunc: func(level, msg string) {
			if verbose {
				fmt.Printf("[%s] %s\n", level, msg)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}

	if err := tun.Up(); err != nil {
		return fmt.Errorf("failed to bring tunnel up: %w", err)
	}
	rollback := func(cause error) error {
		tun.Down()
		return errors.Join(cause, wrapDisconnectError("DNS cleanup", dns.NewManager().Cleanup()))
	}
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return rollback(fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle))
	}
	if session.IsExitNode {
		fmt.Printf("[+] Full tunnel active via exit node (all traffic routed through VPN)\n")
	}
	fmt.Printf("[+] Tunnel %s is up (overlay: %s)\n", ifaceName, cfg.OverlayIP)

	// Step 6: Configure split DNS
	if len(zones) > 0 {
		dnsManager := dns.NewManager()
		if err := dnsManager.Configure(cfg.TunnelDNS, zones); err != nil {
			return rollback(fmt.Errorf("configure split DNS: %w", err))
		}
		if verbose {
			fmt.Printf("[+] Split DNS configured for zones: %v\n", zones)
		}
	}

	// Step 7: Verify connectivity
	if verbose {
		fmt.Println("[*] Verifying tunnel connectivity...")
	}
	if err := waitForTunnelContext(ctx, cfg.BrokerOverlayIP, 50*time.Second); err != nil {
		return rollback(fmt.Errorf("verify tunnel health: %w", err))
	}
	fmt.Println("[+] Tunnel verified — broker reachable")
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return rollback(fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle))
	}

	// Persist session state so TUI can restore mode on relaunch
	state := &config.RuntimeState{
		SessionID:     session.SessionID,
		ExpiresAt:     session.ExpiresAt.Time,
		PresharedKey:  session.PresharedKey,
		GroupID:       groupID,
		ExitNodeID:    exitNodeID,
		IsExitNode:    session.IsExitNode,
		ConnectedAt:   time.Now(),
		InterfaceName: ifaceName,
	}
	if err := commitDirectConnection(ctx, generation, state); err != nil {
		return rollback(err)
	}

	// Step 8: Run daemon or exit
	if noDaemon {
		fmt.Println("[*] Single-shot mode — tunnel is up, no background renewal")
		return nil
	}

	fmt.Println("[*] Running in foreground — press Ctrl+C to disconnect")
	var mutationMu sync.Mutex
	var activeMu sync.Mutex
	activeTunnel := tun
	d := daemon.New(daemon.Config{
		Client: client,
		Tunnel: tun,
		TunnelConfig: &tunnel.Config{
			InterfaceName: ifaceName, PrivateKey: cfg.PrivateKey, OverlayIP: cfg.OverlayIP,
			BrokerPubKey: cfg.BrokerPublicKey, BrokerEndpoint: cfg.BrokerEndpoint,
			PresharedKey: session.PresharedKey, AllowedIPs: allowedIPs,
			DNS: cfg.TunnelDNS, IsExitNode: session.IsExitNode,
		},
		GroupID: groupID, ExitNodeID: exitNodeID, RenewBefore: renewBefore,
		Verbose: verbose, OperationMu: &mutationMu, LifecycleGeneration: generation,
		OnTunnelReplaced: func(replacement *tunnel.Tunnel) {
			activeMu.Lock()
			activeTunnel = replacement
			activeMu.Unlock()
		},
	})
	daemonDone := make(chan struct{})
	go func() {
		defer close(daemonDone)
		d.Run(ctx)
	}()

	<-ctx.Done()
	fmt.Println("\n[*] Shutting down...")
	_, stateErr := config.BeginLifecycle(true)
	<-daemonDone
	mutationMu.Lock()
	activeMu.Lock()
	activeTunnel.Down()
	activeMu.Unlock()
	dnsErr := dns.NewManager().Cleanup()
	mutationMu.Unlock()

	if err := errors.Join(
		wrapDisconnectError("persist disconnect intent", stateErr),
		wrapDisconnectError("DNS cleanup", dnsErr),
	); err != nil {
		return err
	}
	fmt.Println("[+] Disconnected")
	return nil
}

func commitDirectConnection(ctx context.Context, generation uint64, state *config.RuntimeState) error {
	if err := saveDirectState(generation, state); err != nil {
		return fmt.Errorf("persist runtime state: %w", err)
	}
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle)
	}
	return nil
}

func promptGroupSelection(groups *api.AvailableGroupsResponse) (string, error) {
	if groups.HasOverlap {
		fmt.Println("\nMultiple projects detected with overlapping networks:")
	} else {
		fmt.Println("\nMultiple projects available:")
	}
	fmt.Println()
	allLabel := "All groups (access all publishers)"
	if groups.HasOverlap {
		allLabel = "All groups (routing may be ambiguous for overlapping CIDRs)"
	}
	fmt.Printf("  [0] %s\n", allLabel)
	for i, g := range groups.Groups {
		cidrs := ""
		if len(g.CIDRs) > 0 {
			cidrs = fmt.Sprintf(" (%s)", g.CIDRs[0])
			if len(g.CIDRs) > 1 {
				cidrs = fmt.Sprintf(" (%s + %d more)", g.CIDRs[0], len(g.CIDRs)-1)
			}
		}
		// Show groups with no online publishers as unavailable
		if g.OnlinePublishers == 0 {
			fmt.Printf("  [%d] %s%s  (no publishers online — unavailable)\n", i+1, g.Name, cidrs)
		} else {
			fmt.Printf("  [%d] %s%s\n", i+1, g.Name, cidrs)
		}
	}
	fmt.Println()

	fmt.Print("Select project [0=all]: ")
	var choice int
	fmt.Scanln(&choice)

	// 0 means "all groups" — no group filter
	if choice == 0 {
		return "", nil
	}

	if choice < 1 || choice > len(groups.Groups) {
		return "", fmt.Errorf("invalid selection: %d", choice)
	}

	selected := groups.Groups[choice-1]
	if selected.OnlinePublishers == 0 {
		return "", fmt.Errorf("cannot connect to '%s': no publishers online", selected.Name)
	}

	fmt.Printf("[+] Selected: %s\n", selected.Name)
	return selected.ID, nil
}

func promptExitNodeSelection(groups *api.AvailableGroupsResponse) (string, error) {
	fmt.Println("\nVPN Mode — Select exit location:")
	fmt.Println()
	fmt.Println("  [0] Split tunnel (only internal resources, no exit node)")
	for i, node := range groups.ExitNodes {
		status := "online"
		if node.Status != "online" {
			status = node.Status
		}
		location := node.Location
		if location == "" {
			location = "unknown location"
		}
		fmt.Printf("  [%d] %s (%s) [%s]\n", i+1, node.Name, location, status)
	}
	fmt.Println()

	fmt.Print("Select [0=split tunnel]: ")
	var choice int
	fmt.Scanln(&choice)

	if choice == 0 {
		return "", nil // split tunnel, no exit node
	}

	if choice < 1 || choice > len(groups.ExitNodes) {
		return "", fmt.Errorf("invalid selection: %d", choice)
	}

	selected := groups.ExitNodes[choice-1]
	if selected.Status != "online" {
		return "", fmt.Errorf("exit node '%s' is %s (not online)", selected.Name, selected.Status)
	}
	fmt.Printf("[+] Full tunnel via %s (%s)\n", selected.Name, selected.Location)
	return selected.ID, nil
}

var pingTunnelHost = tunnel.PingHost

func waitForTunnel(brokerIP string, timeout time.Duration) error {
	return waitForTunnelContext(context.Background(), brokerIP, timeout)
}

func waitForTunnelContext(ctx context.Context, brokerIP string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	initial := time.NewTimer(10 * time.Second)
	defer initial.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-deadline.C:
		return fmt.Errorf("broker %s unreachable after %v", brokerIP, timeout)
	case <-initial.C:
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if pingTunnelHost(brokerIP) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("broker %s unreachable after %v", brokerIP, timeout)
		case <-ticker.C:
		}
	}
}

// needsElevation returns true on Windows when the process is not running

// needsElevation returns true on Windows when not running as admin.
func needsElevation() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	return !elevation.IsElevated()
}

// elevateAndConnect launches the helper process (hidden, elevated) then delegates via IPC.
func elevateAndConnect(cmd *cobra.Command) error {
	// Check if helper already running
	if ipc.ServiceAvailable() {
		return runConnectViaIPC(cmd)
	}

	// Launch helper with UAC (hidden window)
	fmt.Println("[*] Starting WireZTNA (admin access required)...")
	_, err := elevation.RunElevated([]string{"helper"}, true)
	if err != nil {
		return fmt.Errorf("cannot start helper: %w\nRun as Administrator and try: wireztna connect", err)
	}

	// Wait for helper IPC (max 10s)
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		if ipc.ServiceAvailable() {
			break
		}
	}
	if !ipc.ServiceAvailable() {
		return fmt.Errorf("helper did not start — UAC may have been denied")
	}

	return runConnectViaIPC(cmd)
}

type connectIPCClient interface {
	SendStatus() (*ipc.State, error)
	SendConnect(string) error
	SendExitNode(string) error
	SendDisconnect() error
}

var (
	newConnectIPCClient   = func() connectIPCClient { return ipc.NewClient() }
	connectIPCStatusDelay = 3 * time.Second
)

// runConnectViaIPC delegates to the helper via IPC (works without admin).
func runConnectViaIPC(cmd *cobra.Command) error {
	groupID := viper.GetString("group")
	exitNodeID, _ := cmd.Flags().GetString("exit-node")
	noDaemon, _ := cmd.Flags().GetBool("no-daemon")
	svc := newConnectIPCClient()

	state, err := svc.SendStatus()
	if err != nil {
		return fmt.Errorf("read service status: %w", err)
	}
	if state != nil && state.Status == ipc.StatusConnected {
		fmt.Printf("[+] Connected (overlay: %s)\n", state.OverlayIP)
		return nil
	}

	fmt.Println("[*] Connecting...")
	if exitNodeID != "" {
		err = svc.SendExitNode(exitNodeID)
	} else {
		err = svc.SendConnect(groupID)
	}
	if err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}

	time.Sleep(connectIPCStatusDelay)
	state, err = svc.SendStatus()
	if err != nil {
		return fmt.Errorf("verify service status: %w", err)
	}
	if state == nil {
		return fmt.Errorf("verify service status: empty response")
	}
	if state.Status != ipc.StatusConnected {
		if state.ErrorMessage != "" {
			return fmt.Errorf("%s", state.ErrorMessage)
		}
		return fmt.Errorf("service reported %s after connect", state.Status)
	}
	fmt.Printf("[+] Connected (overlay: %s)\n", state.OverlayIP)
	if state.IsExitNode {
		fmt.Printf("[+] VPN mode via %s\n", state.ExitNodeName)
	}

	if noDaemon {
		fmt.Println("[+] Tunnel managed by helper process (stays connected)")
		return nil
	}

	fmt.Println("[*] Press Ctrl+C to disconnect")
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-sigCh:
			fmt.Println("\n[*] Disconnecting...")
			if err := svc.SendDisconnect(); err != nil {
				return fmt.Errorf("disconnect failed: %w", err)
			}
			fmt.Println("[+] Disconnected")
			return nil
		case <-ticker.C:
			st, err := svc.SendStatus()
			if err != nil {
				return fmt.Errorf("service status lost: %w", err)
			}
			if st == nil || st.Status != ipc.StatusConnected {
				return fmt.Errorf("service no longer reports connected")
			}
			fmt.Printf("\r[+] Connected | rx: %s | tx: %s    ", formatBytes(st.RxBytes), formatBytes(st.TxBytes))
		}
	}
}

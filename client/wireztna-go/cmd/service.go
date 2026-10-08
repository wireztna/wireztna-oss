package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/daemon"
	"github.com/wireztna/client/internal/dns"
	"github.com/wireztna/client/internal/ipc"
	"github.com/wireztna/client/internal/tunnel"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Run the existing Windows WireZTNA service runtime",
	Long: `Runs the legacy WireZTNA service runtime only for existing Windows
installations. New managed-service installation is disabled until IPC v2 has
OS-authenticated peer authorization.

macOS and Linux deliberately fail closed before starting legacy IPC. Their
supported CLI/TUI path remains direct and does not use a Unix service socket.`,
	RunE: runService,
}

func init() {
	rootCmd.AddCommand(serviceCmd)
	rootCmd.AddCommand(helperCmd)
	serviceCmd.Flags().Bool("foreground", false, "run in foreground (don't detach)")
	serviceCmd.Flags().Bool("install", false, "register as a Windows service and start")
	serviceCmd.Flags().Bool("uninstall", false, "stop and remove the Windows service")
}

// helperCmd runs the tunnel manager as an elevated user-session process (not a service).
// This avoids the Session 0 UDP routing issue on Windows.
var helperCmd = &cobra.Command{
	Use:    "helper",
	Short:  "Run as elevated helper (internal use)",
	Hidden: true, // Users don't call this directly — it's launched automatically
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceMain(make(chan struct{}))
	},
}

type serviceDNSManager interface {
	Configure(string, []string) error
	Cleanup() error
}

var (
	newServiceDNSManager  = func() serviceDNSManager { return dns.NewManager() }
	beginServiceLifecycle = config.BeginLifecycle
)

// serviceState holds the live state of the service.
type serviceState struct {
	mu                  sync.RWMutex
	operationMu         sync.Mutex
	operationCancel     context.CancelFunc
	status              string
	tunnel              *tunnel.Tunnel
	daemonCancel        context.CancelFunc
	cfg                 *config.ClientConfig
	apiClient           *api.Client
	groupID             string
	groupName           string
	exitNodeID          string
	exitNodeName        string
	isExitNode          bool
	sessionID           string
	expiresAt           time.Time
	overlayIP           string
	endpoint            string
	lastHandshake       time.Time
	rxBytes             int64
	txBytes             int64
	errorMsg            string
	lifecycleGeneration uint64
}

func newServiceState() *serviceState {
	return &serviceState{status: ipc.StatusDisconnected}
}

func (s *serviceState) beginOperation() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.operationCancel = cancel
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		if s.operationCancel != nil {
			s.operationCancel = nil
		}
		s.mu.Unlock()
	}
}

func (s *serviceState) setError(err error) ipc.Response {
	s.mu.Lock()
	s.status = ipc.StatusError
	s.errorMsg = err.Error()
	s.mu.Unlock()
	return ipc.Response{Success: false, Error: err.Error(), Data: s.getState()}
}

// cancelOperation invalidates blocking API work before disconnect waits for the
// shared mutation gate. It never waits while s.mu is held.
func (s *serviceState) cancelOperation() {
	s.mu.Lock()
	cancel := s.operationCancel
	daemonCancel := s.daemonCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if daemonCancel != nil {
		daemonCancel()
	}
}

func runService(cmd *cobra.Command, args []string) error {
	installFlag, _ := cmd.Flags().GetBool("install")
	uninstallFlag, _ := cmd.Flags().GetBool("uninstall")

	// Handle --install: register as Windows service and start
	if installFlag {
		return installService()
	}
	// Handle --uninstall: stop and remove Windows service
	if uninstallFlag {
		return uninstallService()
	}

	// On Windows, detect if we're running under the Service Control Manager
	// and wrap the service main accordingly.
	if isWindowsService() {
		return runAsWindowsService()
	}

	return runServiceMain(make(chan struct{}))
}

// runServiceMain is the actual service logic, called either directly
// (foreground mode / systemd / launchd) or from the Windows SCM wrapper.
// The stop channel is closed when the service should shut down.
func runServiceMain(stop <-chan struct{}) error {
	if err := legacyServiceRuntimeAllowed(); err != nil {
		return err
	}

	// A pre-v0.9.29 helper does not own the new mutex. Detect its live IPC pipe
	// first so an in-place upgrade cannot clean up the adapter underneath it.
	if existingServiceOwnerAvailable() {
		return fmt.Errorf("another WireZTNA IPC owner is already running")
	}

	// Win the single tunnel-owner election before touching shared config, the
	// global WireGuard adapter, or the named pipe. A losing SCM/helper process
	// must exit without tearing down the active owner's tunnel.
	releaseOwnership, err := acquireServiceOwnership()
	if err != nil {
		return err
	}
	defer releaseOwnership()

	// Create the install marker so CLI/enroll/login use the shared config path
	_ = config.MarkServiceInstalled()

	// Load config through the no-follow config helper: shared location when
	// installed, otherwise the user's ~/.wireztna directory.
	if err := config.ReadResolvedConfig(); err != nil {
		return fmt.Errorf("load service config: %w", err)
	}

	state := newServiceState()

	// Clean up any zombie wintun adapter from a previous crash (Windows only)
	tunnel.CleanupZombieAdapter("wg-wireztna")

	// Start IPC listener
	listener, err := ipc.Listen()
	if err != nil {
		return fmt.Errorf("failed to start IPC listener: %w", err)
	}
	defer listener.Close()

	fmt.Println("[service] WireZTNA service started, listening for IPC commands")

	// Handle shutdown signals (for foreground/systemd mode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		select {
		case <-sigCh:
		case <-stop:
		}
		fmt.Println("\n[service] Shutting down...")
		if resp := state.handleDisconnect(); !resp.Success {
			fmt.Fprintf(os.Stderr, "[service] shutdown cleanup failed: %s\n", resp.Error)
		}
		cancel()
		listener.Close()
	}()

	// Start background status poller
	go state.pollTunnelStatus(ctx)

	// Accept IPC connections
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				fmt.Fprintf(os.Stderr, "[service] accept error: %v\n", err)
				continue
			}
		}
		go state.handleConnection(conn)
	}
}

func (s *serviceState) handleConnection(conn net.Conn) {
	defer conn.Close()

	var req ipc.Request
	decoder := json.NewDecoder(conn)
	if err := decoder.Decode(&req); err != nil {
		writeResponse(conn, ipc.Response{Success: false, Error: "invalid request"})
		return
	}

	var resp ipc.Response

	switch req.Command {
	case ipc.CmdConnect:
		resp = s.handleConnect(req.GroupID)
	case ipc.CmdDisconnect:
		resp = s.handleDisconnect()
	case ipc.CmdStatus:
		resp = s.handleStatus()
	case ipc.CmdSwitch:
		resp = s.handleSwitch(req.GroupID)
	case ipc.CmdGroups:
		resp = s.handleGroups()
	case ipc.CmdExitNode:
		resp = s.handleExitNode(req.ExitNodeID)
	case ipc.CmdEnroll:
		resp = s.handleEnroll(req.EnrollURL)
	case ipc.CmdLogin:
		resp = s.handleLogin()
	case ipc.CmdOTPVerify:
		resp = s.handleOTPVerify(req.OTPCode)
	case ipc.CmdQuit:
		s.disconnect()
		resp = ipc.Response{Success: true}
		writeResponse(conn, resp)
		os.Exit(0)
	default:
		resp = ipc.Response{Success: false, Error: fmt.Sprintf("unknown command: %s", req.Command)}
	}

	writeResponse(conn, resp)
}

func (s *serviceState) handleConnect(groupID string) ipc.Response {
	if !s.operationMu.TryLock() {
		return ipc.Response{Success: false, Error: "connection in progress — please wait"}
	}
	defer s.operationMu.Unlock()

	s.mu.Lock()
	if s.status == ipc.StatusConnected {
		generation := s.lifecycleGeneration
		s.mu.Unlock()
		if generation == 0 || !config.IsLifecycleCurrent(generation) {
			err := "connected tunnel has stale durable lifecycle intent — disconnect before reconnecting"
			return ipc.Response{Success: false, Error: err, Data: s.getState()}
		}
		return ipc.Response{Success: true, Data: s.getState()}
	}
	if s.status == ipc.StatusConnecting || s.status == ipc.StatusReconnecting {
		s.mu.Unlock()
		return ipc.Response{Success: false, Error: "connection in progress — please wait"}
	}
	s.status = ipc.StatusConnecting
	s.errorMsg = ""
	s.mu.Unlock()

	generation, err := beginServiceLifecycle(false)
	if err != nil {
		return s.setError(fmt.Errorf("persist connect intent: %w", err))
	}
	ctx, finish := s.beginOperation()
	defer finish()
	if err := s.connect(ctx, generation, groupID); err != nil {
		s.mu.Lock()
		s.status = ipc.StatusError
		s.errorMsg = err.Error()
		s.mu.Unlock()
		return ipc.Response{Success: false, Error: err.Error()}
	}
	return ipc.Response{Success: true, Data: s.getState()}
}

func (s *serviceState) handleDisconnect() ipc.Response {
	_, firstStateErr := beginServiceLifecycle(true)
	s.cancelOperation()

	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	_, finalStateErr := beginServiceLifecycle(true)
	cleanupErr := s.disconnect()
	err := errors.Join(
		wrapDisconnectError("persist disconnect intent", firstStateErr),
		wrapDisconnectError("reassert disconnect intent", finalStateErr),
		wrapDisconnectError("disconnect cleanup", cleanupErr),
	)
	if err != nil {
		return ipc.Response{Success: false, Error: err.Error(), Data: s.getState()}
	}
	return ipc.Response{Success: true, Data: s.getState()}
}

func (s *serviceState) handleStatus() ipc.Response {
	return ipc.Response{Success: true, Data: s.getState()}
}

func (s *serviceState) handleSwitch(groupID string) ipc.Response {
	if !s.operationMu.TryLock() {
		return ipc.Response{Success: false, Error: "connection operation in progress — please wait"}
	}
	defer s.operationMu.Unlock()

	s.mu.RLock()
	connected := s.status == ipc.StatusConnected
	s.mu.RUnlock()
	if !connected {
		return ipc.Response{Success: false, Error: "not connected"}
	}
	generation, err := beginServiceLifecycle(false)
	if err != nil {
		return ipc.Response{Success: false, Error: fmt.Sprintf("persist switch intent: %v", err)}
	}
	ctx, finish := s.beginOperation()
	defer finish()

	s.mu.Lock()
	s.status = ipc.StatusReconnecting
	s.mu.Unlock()
	if err := s.disconnectTunnel(); err != nil {
		return s.setError(fmt.Errorf("previous tunnel cleanup: %w", err))
	}
	if err := s.connect(ctx, generation, groupID); err != nil {
		s.mu.Lock()
		s.status = ipc.StatusError
		s.errorMsg = err.Error()
		s.mu.Unlock()
		return ipc.Response{Success: false, Error: err.Error()}
	}
	return ipc.Response{Success: true, Data: s.getState()}
}

func (s *serviceState) handleGroups() ipc.Response {
	// Reload config in case user enrolled since service started
	_ = config.ReadResolvedConfig()

	apiURL := viper.GetString("api_url")
	if apiURL == "" {
		return ipc.Response{Success: false, Error: "not enrolled — run: wireztna enroll"}
	}

	token, err := config.LoadToken()
	if err != nil || token == "" {
		return ipc.Response{Success: false, Error: "not authenticated"}
	}

	client := api.NewClient(apiURL)
	client.SetToken(token)

	groups, err := client.GetAvailableGroups()
	if err != nil {
		return ipc.Response{Success: false, Error: fmt.Sprintf("failed to fetch groups: %v", err)}
	}

	state := &ipc.State{
		HasOverlap: groups.HasOverlap,
		VPNMode:    groups.VPNMode,
	}
	for _, g := range groups.Groups {
		state.Groups = append(state.Groups, ipc.Group{
			ID:    g.ID,
			Name:  g.Name,
			CIDRs: g.CIDRs,
		})
	}

	// Include exit nodes if user has VPN mode enabled
	for _, en := range groups.ExitNodes {
		state.ExitNodes = append(state.ExitNodes, ipc.ExitNode{
			ID:       en.ID,
			Name:     en.Name,
			Location: en.Location,
			Online:   en.Status == "online",
		})
	}

	return ipc.Response{Success: true, Data: state}
}

func (s *serviceState) handleExitNode(exitNodeID string) ipc.Response {
	if !s.operationMu.TryLock() {
		return ipc.Response{Success: false, Error: "connection operation in progress — please wait"}
	}
	defer s.operationMu.Unlock()

	s.mu.RLock()
	status := s.status
	s.mu.RUnlock()
	if status != ipc.StatusConnected && status != ipc.StatusDisconnected {
		return ipc.Response{Success: false, Error: "cannot change VPN mode in current state"}
	}

	generation, err := beginServiceLifecycle(false)
	if err != nil {
		return ipc.Response{Success: false, Error: fmt.Sprintf("persist connect intent: %v", err)}
	}
	ctx, finish := s.beginOperation()
	defer finish()

	// Set reconnecting BEFORE disconnect so the GUI poller never sees an
	// intermediate "disconnected" state that hides the Mode menu and
	// confuses the UI. The disconnect() call below will skip setting
	// status to disconnected because we override it here.
	s.mu.Lock()
	s.status = ipc.StatusReconnecting
	s.mu.Unlock()

	// Disconnect existing tunnel (if connected)
	if status == ipc.StatusConnected {
		if err := s.disconnectTunnel(); err != nil {
			return s.setError(fmt.Errorf("previous tunnel cleanup: %w", err))
		}
	}

	// Connect with exit node (or without, for split tunnel)
	if err := s.connectWithExitNode(ctx, generation, s.groupID, exitNodeID); err != nil {
		s.mu.Lock()
		s.status = ipc.StatusError
		s.errorMsg = err.Error()
		s.mu.Unlock()
		return ipc.Response{Success: false, Error: err.Error()}
	}

	return ipc.Response{Success: true, Data: s.getState()}
}

// connectWithExitNode connects the tunnel with an optional exit node.
func (s *serviceState) connectWithExitNode(ctx context.Context, generation uint64, groupID, exitNodeID string) error {
	return s.connectMode(ctx, generation, groupID, exitNodeID)
}

func (s *serviceState) connect(ctx context.Context, generation uint64, groupID string) error {
	return s.connectMode(ctx, generation, groupID, "")
}

func (s *serviceState) connectMode(ctx context.Context, generation uint64, groupID, exitNodeID string) error {
	if err := config.ReadResolvedConfig(); err != nil {
		return fmt.Errorf("reload client config: %w", err)
	}
	apiURL := viper.GetString("api_url")
	if apiURL == "" {
		return fmt.Errorf("not enrolled — run: wireztna enroll \"<url>\"")
	}
	token, err := config.LoadToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated — run: wireztna login")
	}
	if config.IsTokenExpired(token) {
		return fmt.Errorf("session expired — run: wireztna login")
	}

	client := api.NewClient(apiURL)
	client.SetToken(token)
	session, err := client.RenewSessionContext(api.WithLifecycleGeneration(ctx, generation), groupID, exitNodeID)
	if err != nil {
		return fmt.Errorf("session renewal failed: %w", err)
	}
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle)
	}

	exitNodeName := ""
	if exitNodeID != "" {
		if groups, groupsErr := client.GetAvailableGroupsContext(ctx); groupsErr == nil {
			for _, en := range groups.ExitNodes {
				if en.ID == exitNodeID {
					exitNodeName = en.Name
					break
				}
			}
		}
	}
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle)
	}

	cfg := config.Load()
	ifaceName := cfg.Interface
	if ifaceName == "" {
		ifaceName = "wg-wireztna"
	}
	allowedIPs := cfg.AllowedIPs
	if len(session.AllowedIPs) > 0 {
		allowedIPs = session.AllowedIPs
	}
	isExitNode := session.IsExitNode || exitNodeID != ""
	tunCfg := tunnel.Config{
		InterfaceName: ifaceName, PrivateKey: cfg.PrivateKey, OverlayIP: cfg.OverlayIP,
		BrokerPubKey: cfg.BrokerPublicKey, BrokerEndpoint: cfg.BrokerEndpoint,
		PresharedKey: session.PresharedKey, AllowedIPs: allowedIPs, DNS: cfg.TunnelDNS,
		IsExitNode: isExitNode,
		LogFunc:    func(level, msg string) { fmt.Fprintf(os.Stderr, "[tunnel:%s] %s\n", level, msg) },
	}
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle)
	}
	tun, err := tunnel.New(tunCfg)
	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w", err)
	}
	if err := tun.Up(); err != nil {
		return fmt.Errorf("failed to bring tunnel up: %w", err)
	}
	rollback := func(cause error) error {
		tun.Down()
		return errors.Join(cause, wrapDisconnectError("DNS cleanup", newServiceDNSManager().Cleanup()))
	}
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return rollback(fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle))
	}

	var zones []string
	if isExitNode {
		zones = []string{"."}
	} else {
		zones, err = client.GetDNSZonesContext(ctx)
		if err != nil {
			return rollback(fmt.Errorf("fetch DNS zones: %w", err))
		}
	}
	if len(zones) > 0 && cfg.TunnelDNS != "" {
		if err := newServiceDNSManager().Configure(cfg.TunnelDNS, zones); err != nil {
			return rollback(fmt.Errorf("configure DNS: %w", err))
		}
	}
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return rollback(fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle))
	}
	if err := waitForTunnelContext(ctx, cfg.BrokerOverlayIP, 50*time.Second); err != nil {
		return rollback(fmt.Errorf("verify tunnel health: %w", err))
	}

	state := &config.RuntimeState{
		SessionID: session.SessionID, ExpiresAt: session.ExpiresAt.Time,
		PresharedKey: session.PresharedKey, GroupID: groupID,
		ExitNodeID: exitNodeID, ExitNodeName: exitNodeName, IsExitNode: isExitNode,
		ConnectedAt: time.Now(), InterfaceName: ifaceName,
	}
	if err := config.SaveStateForLifecycle(generation, state); err != nil {
		return rollback(fmt.Errorf("persist runtime state: %w", err))
	}
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		return rollback(fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle))
	}

	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	renewBefore := time.Duration(cfg.RenewBefore) * time.Minute
	if renewBefore == 0 {
		renewBefore = 30 * time.Minute
	}
	d := daemon.New(daemon.Config{
		Client: client, Tunnel: tun, TunnelConfig: &tunCfg, GroupID: groupID,
		ExitNodeID: exitNodeID, RenewBefore: renewBefore, OperationMu: &s.operationMu,
		LifecycleGeneration: generation,
		OnTunnelReplaced: func(replacement *tunnel.Tunnel) {
			s.mu.Lock()
			if config.IsLifecycleCurrent(generation) {
				s.tunnel = replacement
			}
			s.mu.Unlock()
		},
	})

	s.mu.Lock()
	if !config.IsLifecycleCurrent(generation) || ctx.Err() != nil {
		s.mu.Unlock()
		daemonCancel()
		return rollback(fmt.Errorf("connection cancelled: %w", config.ErrStaleLifecycle))
	}
	s.status = ipc.StatusConnected
	s.tunnel = tun
	s.daemonCancel = daemonCancel
	s.cfg = cfg
	s.apiClient = client
	s.groupID = groupID
	s.groupName = ""
	s.exitNodeID = exitNodeID
	s.exitNodeName = exitNodeName
	s.isExitNode = isExitNode
	s.sessionID = session.SessionID
	s.expiresAt = session.ExpiresAt.Time
	s.overlayIP = cfg.OverlayIP
	s.errorMsg = ""
	s.lifecycleGeneration = generation
	s.mu.Unlock()
	go d.Run(daemonCtx)

	if isExitNode {
		fmt.Printf("[service] Connected via exit node '%s' (full tunnel)\n", exitNodeName)
	} else {
		fmt.Printf("[service] Connected (overlay: %s, split tunnel)\n", cfg.OverlayIP)
	}
	return nil
}

func (s *serviceState) disconnect() error {
	cleanupErr := s.disconnectTunnel()
	s.mu.Lock()
	s.status = ipc.StatusDisconnected
	s.sessionID = ""
	s.overlayIP = ""
	s.endpoint = ""
	s.lastHandshake = time.Time{}
	s.rxBytes = 0
	s.txBytes = 0
	s.errorMsg = ""
	s.lifecycleGeneration = 0
	s.mu.Unlock()
	if cleanupErr == nil {
		fmt.Println("[service] Disconnected")
	}
	return cleanupErr
}

// disconnectTunnel detaches state under s.mu, then performs potentially slow
// cleanup without holding it. The caller owns operationMu, so no daemon or
// foreground lifecycle mutation can overlap the teardown.
func (s *serviceState) disconnectTunnel() error {
	s.mu.Lock()
	cancel := s.daemonCancel
	tun := s.tunnel
	s.daemonCancel = nil
	s.tunnel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if tun != nil {
		tun.Down()
	}
	cleanupErr := newServiceDNSManager().Cleanup()
	if cleanupErr == nil {
		fmt.Println("[service] Tunnel torn down")
	}
	return cleanupErr
}

func (s *serviceState) getState() *ipc.State {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state := &ipc.State{
		Status:        s.status,
		OverlayIP:     s.overlayIP,
		Endpoint:      s.endpoint,
		LastHandshake: s.lastHandshake,
		RxBytes:       s.rxBytes,
		TxBytes:       s.txBytes,
		SessionID:     s.sessionID,
		ExpiresAt:     s.expiresAt,
		GroupName:     s.groupName,
		GroupID:       s.groupID,
		ErrorMessage:  s.errorMsg,
		IsExitNode:    s.isExitNode,
		ExitNodeName:  s.exitNodeName,
		ExitNodeID:    s.exitNodeID,
	}

	// Check enrollment and auth state for the GUI wizard
	apiURL := viper.GetString("api_url")
	if apiURL == "" {
		state.NeedsEnroll = true
	} else {
		token, err := config.LoadToken()
		if err != nil || token == "" || config.IsTokenExpired(token) {
			state.NeedsLogin = true
		}
		// Provide masked email for login display
		email := viper.GetString("email")
		if email == "" {
			email = viper.GetString("username")
		}
		state.Email = maskEmail(email)
	}

	return state
}

func (s *serviceState) pollTunnelStatus(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.RLock()
			status := s.status
			s.mu.RUnlock()

			if status != ipc.StatusConnected {
				continue
			}

			ifaceName := "wg-wireztna"
			s.mu.RLock()
			if s.cfg != nil && s.cfg.Interface != "" {
				ifaceName = s.cfg.Interface
			}
			s.mu.RUnlock()

			info, err := tunnel.GetStatus(ifaceName)
			if err != nil {
				s.mu.Lock()
				if s.status == ipc.StatusConnected {
					s.status = ipc.StatusError
					s.errorMsg = fmt.Sprintf("tunnel health check failed: %v", err)
				}
				s.mu.Unlock()
				continue
			}

			s.mu.Lock()
			s.overlayIP = info.OverlayIP
			s.endpoint = info.Endpoint
			s.lastHandshake = info.LastHandshake
			s.rxBytes = info.RxBytes
			s.txBytes = info.TxBytes
			s.mu.Unlock()
		}
	}
}

func writeResponse(conn net.Conn, resp ipc.Response) {
	encoder := json.NewEncoder(conn)
	_ = encoder.Encode(resp)
}

// ─── Enrollment & Auth Handlers ───

// handleEnroll registers this device with the control plane using an enrollment URL.
func (s *serviceState) handleEnroll(enrollURL string) ipc.Response {
	if enrollURL == "" {
		return ipc.Response{Success: false, Error: "enrollment URL is required"}
	}

	// Parse the enrollment URL to extract token and API base URL
	parsed, err := url.Parse(enrollURL)
	if err != nil {
		return ipc.Response{Success: false, Error: fmt.Sprintf("invalid URL: %v", err)}
	}

	token := parsed.Query().Get("token")
	if token == "" {
		return ipc.Response{Success: false, Error: "URL does not contain a 'token' parameter"}
	}

	// Derive API base URL (scheme + host, no port = port 80)
	apiURL := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)

	// Generate WireGuard keypair
	privKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return ipc.Response{Success: false, Error: fmt.Sprintf("key generation failed: %v", err)}
	}
	pubKey := privKey.PublicKey()

	// Call enrollment endpoint
	client := api.NewClient(apiURL)
	resp, err := client.Enroll(token, pubKey.String())
	if err != nil {
		return ipc.Response{Success: false, Error: fmt.Sprintf("enrollment failed: %v", err)}
	}

	// Write config
	enrollCfg := &config.EnrollConfig{
		APIURL:          apiURL,
		Email:           resp.Email,
		Username:        resp.Username,
		PrivateKey:      privKey.String(),
		OverlayIP:       resp.OverlayIP,
		BrokerPublicKey: resp.BrokerPublicKey,
		BrokerEndpoint:  resp.BrokerEndpoint,
		BrokerOverlayIP: resp.BrokerOverlayIP,
		AllowedIPs:      resp.AllowedIPs,
		DNSServer:       resp.DNSServer,
	}

	if err := config.WriteEnrollConfig(enrollCfg); err != nil {
		return ipc.Response{Success: false, Error: fmt.Sprintf("failed to write config: %v", err)}
	}

	// Reload viper so subsequent commands pick up the new config
	_ = config.ReadResolvedConfig()

	fmt.Printf("[service] Enrolled successfully (email: %s, overlay: %s)\n", resp.Email, resp.OverlayIP)
	return ipc.Response{Success: true, Data: s.getState()}
}

// handleLogin requests an OTP code to be sent to the user's email.
func (s *serviceState) handleLogin() ipc.Response {
	// Reload config
	_ = config.ReadResolvedConfig()

	apiURL := viper.GetString("api_url")
	if apiURL == "" {
		return ipc.Response{Success: false, Error: "not enrolled"}
	}

	email := viper.GetString("email")
	if email == "" {
		email = viper.GetString("username")
	}
	if email == "" {
		return ipc.Response{Success: false, Error: "no email configured — re-enroll"}
	}

	client := api.NewClient(apiURL)
	_, err := client.OTPRequest(email)
	if err != nil {
		return ipc.Response{Success: false, Error: fmt.Sprintf("failed to request code: %v", err)}
	}

	fmt.Printf("[service] OTP code requested for %s\n", maskEmail(email))
	return ipc.Response{
		Success: true,
		Data: &ipc.State{
			Email:      maskEmail(email),
			NeedsLogin: true,
		},
	}
}

// handleOTPVerify verifies the OTP code and stores the JWT token.
func (s *serviceState) handleOTPVerify(code string) ipc.Response {
	if code == "" {
		return ipc.Response{Success: false, Error: "code is required"}
	}

	apiURL := viper.GetString("api_url")
	if apiURL == "" {
		return ipc.Response{Success: false, Error: "not enrolled"}
	}

	email := viper.GetString("email")
	if email == "" {
		email = viper.GetString("username")
	}
	if email == "" {
		return ipc.Response{Success: false, Error: "no email configured"}
	}

	client := api.NewClient(apiURL)
	token, err := client.OTPVerify(email, code)
	if err != nil {
		return ipc.Response{Success: false, Error: fmt.Sprintf("verification failed: %v", err)}
	}

	// Save the JWT
	if err := config.SaveToken(token); err != nil {
		return ipc.Response{Success: false, Error: fmt.Sprintf("failed to save token: %v", err)}
	}

	fmt.Printf("[service] Login successful (email: %s)\n", maskEmail(email))
	return ipc.Response{Success: true, Data: s.getState()}
}

// maskEmail masks an email for display: "s****@domain.com"
func maskEmail(email string) string {
	if email == "" {
		return ""
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		// Not a proper email — mask the whole thing
		if len(email) <= 2 {
			return email
		}
		return email[:1] + strings.Repeat("*", len(email)-1)
	}
	local := email[:at]
	domain := email[at:]
	if len(local) <= 1 {
		return local + "****" + domain
	}
	return local[:1] + strings.Repeat("*", min(4, len(local)-1)) + domain
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

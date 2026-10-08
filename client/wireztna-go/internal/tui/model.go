// Package tui implements the interactive terminal UI using bubbletea.
package tui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wireztna/client/desktop/controller"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	debugpkg "github.com/wireztna/client/internal/debug"
	"github.com/wireztna/client/internal/dns"
	"github.com/wireztna/client/internal/ipc"
	"github.com/wireztna/client/internal/tunnel"
	"github.com/wireztna/client/pkg/version"
)

// State represents the connection state.
type State int

const (
	StateDisconnected State = iota
	StateConnecting
	StateConnected
	StateReconnecting
	StateError
	StateAuthRequired // JWT expired — need re-authentication
	StateLoggingIn    // Inline login in progress (OTP flow)
)

func (s State) String() string {
	switch s {
	case StateDisconnected:
		return "Disconnected"
	case StateConnecting:
		return "Connecting..."
	case StateConnected:
		return "Connected"
	case StateReconnecting:
		return "Reconnecting..."
	case StateError:
		return "Error"
	case StateAuthRequired:
		return "Auth Required"
	case StateLoggingIn:
		return "Logging in..."
	default:
		return "Unknown"
	}
}

// loginStep tracks which step of the inline login flow we're in.
type loginStep int

const (
	loginStepEmail loginStep = iota // Entering email
	loginStepOTP                    // Entering OTP code
)

// renewBefore is how long before session expiry to trigger auto-renewal.
const renewBefore = 5 * time.Minute

// renewCheckInterval is how often to check if renewal is needed (ticks are 2s, check every 30s).
const renewCheckInterval = 15 // every 15 ticks (30s)

var (
	verifyDirectTunnel = waitForDirectTunnelHealth
	saveTUIState       = config.SaveStateForLifecycle
	cleanupStaleTUIDNS = func() error { return dns.NewManager().Cleanup() }
)

var errStaleSessionOperation = errors.New("stale session operation")

type tunnelPSKUpdater interface {
	UpdatePSK(newPSK string) error
	Down()
}

type sessionCoordinator struct {
	executionMu sync.Mutex
	ipcMu       sync.Mutex
	generation  atomic.Uint64
}

// Model is the bubbletea model for the TUI.
type Model struct {
	// Connection state
	state            State
	errorMsg         string
	userDisconnected bool // explicit user intent; blocks polling/auto-reconnect adoption

	// Tunnel info
	overlayIP     string
	endpoint      string
	lastHandshake time.Time
	rxBytes       int64
	txBytes       int64

	// Session info
	sessionID  string
	expiresAt  time.Time
	ttlSeconds int
	groupName  string
	groupID    string

	// Groups
	groups        []api.GroupInfo
	hasOverlap    bool
	selectedGroup int

	// Exit nodes (VPN mode)
	exitNodes        []api.ExitNodeInfo
	managedExitNodes []api.ExitNodeInfo // complete online catalog before project scoping
	vpnMode          bool
	selectedExitNode int // 0 = split tunnel, 1+ = exit node index
	exitNodeID       string
	isExitNode       bool   // actual mode from service (not local selection)
	exitNodeName     string // active exit node name from service

	// UI state
	width    int
	height   int
	tab      int // 0=status, 1=projects, 2=network, 3=logs
	quitting bool

	// Cursor position in groups tab (0 = "All groups", 1+ = group index)
	cursor       int
	cursorInExit bool // true if cursor is in exit nodes section

	// Spinner for connecting animation
	spinnerIdx int

	// Traffic history for sparkline (last 20 samples of rx delta)
	rxHistory     [20]int64
	rxHistoryIdx  int
	rxHistoryFull bool
	lastRxSample  int64

	// Publisher info (fetched with session)
	publisherEndpoint string
	publisherName     string

	// Latency measurement (ping broker overlay every tick)
	latencyMs    int64 // last measured latency in ms (-1 = timeout)
	latencyColor int   // 0=good, 1=medium, 2=bad

	// Quick ping result (on-demand, 'p' key)
	pingResults []pingTarget
	pingRunning bool
	pingIdx     int // spinner index for ping animation

	// Debug info
	debugInfo   *debugpkg.Info
	debugLogger *debugpkg.Logger

	// Debug tab scroll state
	debugScroll   int  // scroll offset (lines from bottom, 0 = tail)
	debugFilter   int  // 0=all, 1=info+, 2=warn+, 3=error only
	debugAutoTail bool // if true, follows new entries (scroll stays at bottom)

	// Inline login flow (OTP)
	loginStep       loginStep
	loginEmail      string // email being entered
	loginOTP        string // OTP code being entered
	loginError      string // error message from login attempt
	loginRenewAfter bool   // if true, auto-connect after login succeeds

	// Auto-renewal state
	renewTickCount       int  // counter for renewal check interval
	renewInFlight        bool // true while a background renewal is in progress
	connectionGeneration uint64
	lifecycleGeneration  uint64
	lastRenewError       string

	// Dependencies
	apiClient          *api.Client
	tunnel             tunnelPSKUpdater
	cfg                *config.ClientConfig
	sessionCoordinator *sessionCoordinator

	// macOS service-managed mode. This mode is selected only by platform
	// composition and never by runtime service probing.
	managed              bool
	managedReady         bool
	managedBackend       ManagedBackend
	managedContext       context.Context
	managedCancel        context.CancelFunc
	managedHealth        controller.Health
	managedProgress      controller.ProgressStage
	managedOperationName string
	managedStreamID      string
	managedEpoch         uint64
	managedSequence      uint64
}

// NewModel creates the legacy/direct TUI model used outside macOS.
func NewModel(apiClient *api.Client, cfg *config.ClientConfig) Model {
	return Model{
		state:              StateDisconnected,
		apiClient:          apiClient,
		cfg:                cfg,
		tab:                0,
		debugLogger:        debugpkg.NewLogger(500),
		debugAutoTail:      true,
		sessionCoordinator: &sessionCoordinator{},
	}
}

// NewManagedModel creates a service-owned TUI. Connection state and mutations
// are available only through backend; API access remains available for OTP.
func NewManagedModel(apiClient *api.Client, cfg *config.ClientConfig, backend ManagedBackend) Model {
	ctx, cancel := context.WithCancel(context.Background())
	return Model{
		state:              StateDisconnected,
		apiClient:          apiClient,
		cfg:                cfg,
		tab:                0,
		debugLogger:        debugpkg.NewLogger(500),
		debugAutoTail:      true,
		sessionCoordinator: &sessionCoordinator{},
		managed:            true,
		managedBackend:     backend,
		managedContext:     ctx,
		managedCancel:      cancel,
	}
}

// SetInitialState sets the initial state of the model before Init() runs.
// Used to start the TUI in AuthRequired state when token is missing/expired.
func (m *Model) SetInitialState(s State) {
	m.state = s
}

// ─── Messages ───

type tickMsg time.Time
type tunnelStatusMsg struct {
	info *tunnel.StatusInfo
	err  error
	// Service-reported state (only populated when using IPC)
	serviceStatus string // "connected", "disconnected", etc.
	isExitNode    bool
	exitNodeName  string
	exitNodeID    string
	groupName     string
	groupID       string
	sessionID     string
	expiresAt     time.Time
}
type sessionInfoMsg struct {
	info *api.SessionInfoResponse
	err  error
}
type groupsMsg struct {
	groups *api.AvailableGroupsResponse
	err    error
}
type connectResultMsg struct {
	session             *api.SessionResponse
	tunnel              tunnelPSKUpdater
	lifecycleGeneration uint64
	err                 error
}
type disconnectMsg struct {
	err error
}
type connectionMutationMsg struct {
	generation uint64
	result     tea.Msg
}
type exitNodeCleanupAction int

const (
	exitNodeCleanupRequireAuth exitNodeCleanupAction = iota
	exitNodeCleanupReconnect
)

type exitNodeCleanupMsg struct {
	action    exitNodeCleanupAction
	dnsErr    error
	tunnelErr error
}
type retryExitNodeCleanupMsg struct {
	action exitNodeCleanupAction
}
type errorMsg struct {
	err error
}

type debugInfoMsg struct {
	info *debugpkg.Info
}

type latencyMsg struct {
	ms int64 // -1 = timeout/error
}

type pingResultMsg struct {
	results []pingTarget
}

// pingTarget holds the result of a single ping test
type pingTarget struct {
	name    string
	ip      string
	latency int64 // ms, -1 = timeout
	ok      bool
}

// renewResultMsg is returned by the background auto-renewal command.
type renewResultMsg struct {
	session    *api.SessionResponse
	err        error
	generation uint64
}

// loginOTPRequestMsg is returned after requesting an OTP code.
type loginOTPRequestMsg struct {
	err error
}

// loginOTPVerifyMsg is returned after verifying the OTP code.
type loginOTPVerifyMsg struct {
	token string
	err   error
}

// ─── Commands ───

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

type spinnerTickMsg struct{}

func spinnerTickCmd() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

func retryExitNodeCleanupCmd(action exitNodeCleanupAction) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return retryExitNodeCleanupMsg{action: action}
	})
}

// ─── Auto-renewal & Login Commands ───

// autoRenewCmd attempts a background session renewal + PSK hot-swap.
func (m Model) autoRenewCmd() tea.Cmd {
	generation := m.connectionGeneration
	return func() tea.Msg {
		session, err := m.renewSession(generation, m.groupID, m.exitNodeID)
		return renewResultMsg{session: session, err: err, generation: generation}
	}
}

// renewSession serializes control-plane renewals and rejects stale work before
// it can mutate the broker. Generation is atomic and independent from the
// execution lock so an explicit action can invalidate an API call in flight;
// that explicit call then waits and becomes the final mutation.
func (m Model) renewSession(generation uint64, groupID string, exitNodeID ...string) (*api.SessionResponse, error) {
	coordinator := m.sessionCoordinator
	if coordinator == nil {
		return m.apiClient.RenewSessionContext(api.WithLifecycleGeneration(context.Background(), m.lifecycleGeneration), groupID, exitNodeID...)
	}

	coordinator.executionMu.Lock()
	defer coordinator.executionMu.Unlock()
	if generation != coordinator.generation.Load() {
		return nil, errStaleSessionOperation
	}
	return m.apiClient.RenewSessionContext(api.WithLifecycleGeneration(context.Background(), m.lifecycleGeneration), groupID, exitNodeID...)
}

// loginRequestOTPCmd sends an OTP request to the user's email.
func (m Model) loginRequestOTPCmd(email string) tea.Cmd {
	return func() tea.Msg {
		// If IPC service is available (Windows service / helper), delegate
		if ipc.ServiceAvailable() {
			svc := ipc.NewClient()
			_, err := svc.SendLogin()
			return loginOTPRequestMsg{err: err}
		}
		// Direct API call (macOS/Linux without service)
		_, err := m.apiClient.OTPRequest(email)
		return loginOTPRequestMsg{err: err}
	}
}

// loginVerifyOTPCmd verifies the OTP code and returns a JWT token.
func (m Model) loginVerifyOTPCmd(email, code string) tea.Cmd {
	return func() tea.Msg {
		// If IPC service is available (Windows service / helper), delegate
		if ipc.ServiceAvailable() {
			svc := ipc.NewClient()
			err := svc.SendOTPVerify(code)
			if err != nil {
				return loginOTPVerifyMsg{token: "", err: err}
			}
			// Service stored the token — reload it for the TUI's API client
			token, _ := config.LoadToken()
			return loginOTPVerifyMsg{token: token, err: nil}
		}
		// Direct API call (macOS/Linux without service)
		token, err := m.apiClient.OTPVerify(email, code)
		return loginOTPVerifyMsg{token: token, err: err}
	}
}

func (m Model) fetchDebugInfo() tea.Cmd {
	return func() tea.Msg {
		ifaceName := m.cfg.Interface
		if ifaceName == "" {
			ifaceName = "wg-wireztna"
		}
		info := debugpkg.Gather(ifaceName)
		return debugInfoMsg{info: &info}
	}
}

func (m Model) measureLatency() tea.Cmd {
	return func() tea.Msg {
		// TCP connect to broker API port — measures real round-trip through the tunnel
		// Using the overlay DNS IP (10.200.0.1) on port 53 via TCP
		target := "10.200.0.1:53"
		if m.cfg.TunnelDNS != "" {
			target = m.cfg.TunnelDNS + ":53"
		}
		start := time.Now()
		conn, err := net.DialTimeout("tcp", target, 3*time.Second)
		if err != nil {
			// Fallback: try the API endpoint through the tunnel
			apiTarget := m.cfg.BrokerEndpoint
			if apiTarget == "" {
				return latencyMsg{ms: -1}
			}
			// Extract host from endpoint (could be host:port or just host)
			host := apiTarget
			if h, _, err2 := net.SplitHostPort(apiTarget); err2 == nil {
				host = h
			}
			start = time.Now()
			conn2, err2 := net.DialTimeout("tcp", host+":80", 3*time.Second)
			if err2 != nil {
				return latencyMsg{ms: -1}
			}
			conn2.Close()
			elapsed := time.Since(start).Milliseconds()
			return latencyMsg{ms: elapsed}
		}
		conn.Close()
		elapsed := time.Since(start).Milliseconds()
		return latencyMsg{ms: elapsed}
	}
}

func (m Model) runPingTest() tea.Cmd {
	return func() tea.Msg {
		var results []pingTarget

		// 1. Ping broker overlay
		results = append(results, pingOneTarget("Broker", "10.200.0.1"))

		// 2. Ping publishers from selected group
		for _, g := range m.groups {
			if g.Name == m.groupName || (m.groupID != "" && g.ID == m.groupID) {
				for _, p := range g.Publishers {
					if p.Status == "online" && len(p.ExposedCIDRs) > 0 {
						targetIP := firstHostFromCIDR(p.ExposedCIDRs[0])
						if targetIP != "" {
							results = append(results, pingOneTarget(p.Name, targetIP))
						}
					}
				}
				break
			}
		}

		if len(results) == 0 {
			results = append(results, pingTarget{name: "No targets", ip: "—", latency: -1, ok: false})
		}
		return pingResultMsg{results: results}
	}
}

func (m Model) fetchTunnelStatus() tea.Cmd {
	return func() tea.Msg {
		// If service is running, get full status from IPC
		if ipc.ServiceAvailable() {
			svc := ipc.NewClient()
			state, err := svc.SendStatus()
			if err != nil {
				return tunnelStatusMsg{info: nil, err: err}
			}
			if state.Status == ipc.StatusConnected {
				return tunnelStatusMsg{
					info: &tunnel.StatusInfo{
						OverlayIP:     state.OverlayIP,
						Endpoint:      state.Endpoint,
						LastHandshake: state.LastHandshake,
						RxBytes:       state.RxBytes,
						TxBytes:       state.TxBytes,
					},
					serviceStatus: state.Status,
					isExitNode:    state.IsExitNode,
					exitNodeName:  state.ExitNodeName,
					exitNodeID:    state.ExitNodeID,
					groupName:     state.GroupName,
					groupID:       state.GroupID,
					sessionID:     state.SessionID,
					expiresAt:     state.ExpiresAt,
				}
			}
			return tunnelStatusMsg{
				info:          nil,
				err:           fmt.Errorf("status: %s", state.Status),
				serviceStatus: state.Status,
			}
		}

		// Direct query (macOS/Linux)
		ifaceName := m.cfg.Interface
		if ifaceName == "" {
			ifaceName = "wg-wireztna"
		}
		info, err := tunnel.GetStatus(ifaceName)
		return tunnelStatusMsg{info: info, err: err}
	}
}

func (m Model) fetchSessionInfo() tea.Cmd {
	return func() tea.Msg {
		info, err := m.apiClient.GetSessionInfo()
		return sessionInfoMsg{info: info, err: err}
	}
}

func (m Model) fetchGroups() tea.Cmd {
	return func() tea.Msg {
		groups, err := m.apiClient.GetAvailableGroups()
		return groupsMsg{groups: groups, err: err}
	}
}

// connectionMutationCmd tags asynchronous connection mutations so stale
// results cannot overwrite a newer user intent in the Bubble Tea model.
func connectionMutationCmd(generation uint64, operation func() tea.Msg) tea.Cmd {
	return func() tea.Msg {
		result := operation()
		if result == nil {
			return nil
		}
		return connectionMutationMsg{generation: generation, result: result}
	}
}

// runIPCMutation serializes IPC mutations emitted by this TUI and validates
// their generation after acquiring the lock. This makes the latest user action
// the final command delivered even though Bubble Tea runs tea.Cmd concurrently.
func (m Model) runIPCMutation(generation uint64, operation func() tea.Msg) tea.Msg {
	coordinator := m.sessionCoordinator
	if coordinator == nil {
		return operation()
	}

	coordinator.ipcMu.Lock()
	defer coordinator.ipcMu.Unlock()
	if generation != coordinator.generation.Load() {
		return nil
	}
	return operation()
}

func sendConnectViaIPC(groupID string, exitNodeID ...string) tea.Msg {
	svc := ipc.NewClient()
	var err error
	if len(exitNodeID) > 0 && exitNodeID[0] != "" {
		err = svc.SendExitNode(exitNodeID[0])
	} else {
		err = svc.SendConnect(groupID)
	}
	if err != nil {
		return connectResultMsg{err: fmt.Errorf("service: %w", err)}
	}
	// Read session state from the service — do NOT call RenewSession again, as
	// the service already renewed the session with the PSK used by the tunnel.
	return readServiceSession(svc)
}

func (m Model) connectCmd(groupID string, exitNodeID ...string) tea.Cmd {
	generation := m.connectionGeneration
	return connectionMutationCmd(generation, func() tea.Msg {
		// Windows tunnel mutations must be serialized before probing/starting the
		// helper so a stale connect cannot arrive after a newer disconnect.
		if runtime.GOOS == "windows" {
			return m.runIPCMutation(generation, func() tea.Msg {
				if !ipc.ServiceAvailable() {
					if err := ipc.EnsureHelper(); err != nil {
						return connectResultMsg{err: fmt.Errorf("cannot start helper: %w", err)}
					}
				}
				return sendConnectViaIPC(groupID, exitNodeID...)
			})
		}

		// Other service-backed clients use the same ordering guarantee; direct
		// Unix tunnel creation retains its existing generation checkpoints.
		if ipc.ServiceAvailable() {
			return m.runIPCMutation(generation, func() tea.Msg {
				return sendConnectViaIPC(groupID, exitNodeID...)
			})
		}
		return m.connectDirect(groupID, exitNodeID...)
	})
}

// readServiceSession reads session info from the service after a successful IPC
// command, avoiding a redundant RenewSession API call.
func readServiceSession(svc *ipc.Client) tea.Msg {
	state, err := svc.SendStatus()
	if err != nil {
		return connectResultMsg{err: fmt.Errorf("service status: %w", err)}
	}
	if state == nil {
		return connectResultMsg{err: fmt.Errorf("service status returned no state")}
	}
	return connectResultMsg{session: &api.SessionResponse{
		SessionID:  state.SessionID,
		ExpiresAt:  api.FlexTime{Time: state.ExpiresAt},
		IsExitNode: state.IsExitNode,
	}}
}

// connectDirect creates the tunnel directly (all platforms).
func (m Model) connectDirect(groupID string, exitNodeID ...string) tea.Msg {
	generation := m.connectionGeneration
	if m.sessionCoordinator != nil {
		m.sessionCoordinator.executionMu.Lock()
		defer m.sessionCoordinator.executionMu.Unlock()
		if generation != m.sessionCoordinator.generation.Load() {
			return connectResultMsg{err: errStaleSessionOperation}
		}
	}
	// Hold the direct lifecycle gate across remote renewal, local apply, DNS,
	// health and persistence so another connect cannot overlap its mutations.
	session, err := m.apiClient.RenewSessionContext(api.WithLifecycleGeneration(context.Background(), m.lifecycleGeneration), groupID, exitNodeID...)
	if err != nil {
		return connectResultMsg{err: fmt.Errorf("session renewal: %w", err)}
	}
	if !m.lifecycleCurrent(generation) {
		return connectResultMsg{err: errStaleSessionOperation}
	}

	// Step 2: Recreate the WireGuard tunnel with the new PSK. Renew first so
	// an API failure leaves the existing tunnel untouched; once renewal
	// succeeds, cleanly remove DNS and routes from the previous mode.
	ifaceName := m.cfg.Interface
	if ifaceName == "" {
		ifaceName = "wg-wireztna"
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		if err := dns.NewManager().Cleanup(); err != nil {
			return connectResultMsg{err: fmt.Errorf("previous DNS cleanup: %w", err)}
		}
		if err := tunnel.DownWithEndpoint(ifaceName, m.cfg.BrokerEndpoint); err != nil {
			var routeCleanupErr *tunnel.BrokerRouteCleanupError
			if !errors.As(err, &routeCleanupErr) {
				return connectResultMsg{err: fmt.Errorf("previous tunnel teardown: %w", err)}
			}
			if m.debugLogger != nil {
				m.debugLogger.Warn("Previous broker route cleanup incomplete: " + err.Error())
			}
		}
	}

	// Use dynamic AllowedIPs from session (reflects current CIDRs)
	allowedIPs := m.cfg.AllowedIPs
	if len(session.AllowedIPs) > 0 {
		allowedIPs = session.AllowedIPs
	}

	tun, err := tunnel.New(tunnel.Config{
		InterfaceName:  ifaceName,
		PrivateKey:     m.cfg.PrivateKey,
		OverlayIP:      m.cfg.OverlayIP,
		BrokerPubKey:   m.cfg.BrokerPublicKey,
		BrokerEndpoint: m.cfg.BrokerEndpoint,
		PresharedKey:   session.PresharedKey,
		AllowedIPs:     allowedIPs,
		DNS:            m.cfg.TunnelDNS,
		IsExitNode:     session.IsExitNode,
		LogFunc: func(level, msg string) {
			if m.debugLogger == nil {
				return
			}
			switch level {
			case "error":
				m.debugLogger.Error(msg)
			case "warn":
				m.debugLogger.Warn(msg)
			case "info":
				m.debugLogger.Info(msg)
			default:
				m.debugLogger.Debug(msg)
			}
		},
	})
	if err != nil {
		return connectResultMsg{err: fmt.Errorf("tunnel setup: %w", err)}
	}

	if err := tun.Up(); err != nil {
		return connectResultMsg{err: fmt.Errorf("tunnel up: %w", err)}
	}
	rollback := func(cause error) tea.Msg {
		tun.Down()
		if cleanupErr := dns.NewManager().Cleanup(); cleanupErr != nil {
			cause = errors.Join(cause, fmt.Errorf("DNS cleanup: %w", cleanupErr))
		}
		return connectResultMsg{err: cause}
	}
	if !m.lifecycleCurrent(generation) {
		return rollback(errStaleSessionOperation)
	}

	// Step 3: Configure DNS for the new mode. macOS full-tunnel DNS is
	// managed by wg-quick; Linux needs an explicit systemd-resolved `~.` route.
	var zones []string
	if session.IsExitNode {
		if runtime.GOOS == "linux" {
			zones = []string{"."}
		}
	} else {
		zones, err = m.apiClient.GetDNSZones()
		if err != nil {
			return rollback(fmt.Errorf("fetch DNS zones: %w", err))
		}
	}
	if len(zones) > 0 && m.cfg.TunnelDNS != "" {
		if err := dns.NewManager().Configure(m.cfg.TunnelDNS, zones); err != nil {
			return rollback(fmt.Errorf("configure DNS: %w", err))
		}
	}
	if !m.lifecycleCurrent(generation) {
		return rollback(errStaleSessionOperation)
	}
	if err := verifyDirectTunnel(ifaceName, 50*time.Second, func() bool {
		return m.lifecycleCurrent(generation)
	}); err != nil {
		return rollback(fmt.Errorf("verify tunnel health: %w", err))
	}

	state := &config.RuntimeState{
		SessionID: session.SessionID, ExpiresAt: session.ExpiresAt.Time,
		PresharedKey: session.PresharedKey, GroupID: groupID,
		ExitNodeID: m.exitNodeID, IsExitNode: session.IsExitNode,
		ConnectedAt: time.Now(), InterfaceName: ifaceName,
	}
	if err := saveTUIState(m.lifecycleGeneration, state); err != nil {
		return rollback(fmt.Errorf("persist runtime state: %w", err))
	}
	if !m.lifecycleCurrent(generation) {
		return rollback(errStaleSessionOperation)
	}
	return connectResultMsg{session: session, tunnel: tun, lifecycleGeneration: m.lifecycleGeneration}
}

func (m Model) disconnectCmd() tea.Cmd {
	generation := m.connectionGeneration
	return connectionMutationCmd(generation, func() tea.Msg {
		// Service-backed mutations share the generation lock with connect/switch.
		// If a connect is already being sent, this disconnect runs after it; if
		// the connect has not started, its stale generation prevents the send.
		if runtime.GOOS == "windows" || ipc.ServiceAvailable() {
			return m.runIPCMutation(generation, m.disconnectNow)
		}
		return m.disconnectNow()
	})
}

func (m Model) disconnectNow() tea.Msg {
	// Persist intent before teardown so a concurrent health monitor cannot
	// recreate the tunnel after an explicit user disconnect.
	stateErr := config.SetUserDisconnected(true)

	// If service is running, delegate via IPC.
	if ipc.ServiceAvailable() {
		svc := ipc.NewClient()
		err := svc.SendDisconnect()
		if stateErr != nil {
			if err != nil {
				return disconnectMsg{err: fmt.Errorf("persist disconnect intent: %v; service: %w", stateErr, err)}
			}
			return disconnectMsg{err: fmt.Errorf("persist disconnect intent: %w", stateErr)}
		}
		return disconnectMsg{err: err}
	}

	// Direct teardown (macOS/Linux, plus a safe Windows no-owner fallback).
	ifaceName := m.cfg.Interface
	if ifaceName == "" {
		ifaceName = "wg-wireztna"
	}
	dnsErr := dns.NewManager().Cleanup()
	tunnelErr := tunnel.DownWithEndpoint(ifaceName, m.cfg.BrokerEndpoint)
	return disconnectMsg{err: errors.Join(
		wrapTUILifecycleError("persist disconnect intent", stateErr),
		wrapTUILifecycleError("tunnel teardown", tunnelErr),
		wrapTUILifecycleError("DNS cleanup", dnsErr),
	)}
}

// cleanupExpiredExitNodeCmd removes a full-tunnel route before authentication
// or session renewal. Unlike an explicit TUI quit, this cleanup is only used
// when the active exit-node session can no longer carry traffic.
func (m Model) cleanupExpiredExitNodeCmd(action exitNodeCleanupAction) tea.Cmd {
	return func() tea.Msg {
		ifaceName := m.cfg.Interface
		if ifaceName == "" {
			ifaceName = "wg-wireztna"
		}

		return exitNodeCleanupMsg{
			action:    action,
			dnsErr:    dns.NewManager().Cleanup(),
			tunnelErr: tunnel.DownWithEndpoint(ifaceName, m.cfg.BrokerEndpoint),
		}
	}
}

// switchToSplitCmd switches from an exit node back to split tunnel. Unix
// clients recreate the tunnel directly; service-backed Windows clients must
// use CmdExitNode("") because CmdConnect is a no-op while connected.
func (m Model) switchToSplitCmd() tea.Cmd {
	generation := m.connectionGeneration
	return connectionMutationCmd(generation, func() tea.Msg {
		if runtime.GOOS != "windows" && !ipc.ServiceAvailable() {
			return m.connectDirect(m.groupID)
		}
		return m.runIPCMutation(generation, func() tea.Msg {
			if !ipc.ServiceAvailable() {
				if err := ipc.EnsureHelper(); err != nil {
					return connectResultMsg{err: fmt.Errorf("cannot start helper: %w", err)}
				}
			}
			svc := ipc.NewClient()
			if err := svc.SendExitNode(""); err != nil {
				return connectResultMsg{err: fmt.Errorf("service: %w", err)}
			}
			return readServiceSession(svc)
		})
	})
}

// switchGroupCmd changes the active group while connected. Unix clients
// recreate directly with the selected group; service-backed Windows clients
// use CmdSwitch because CmdConnect is a no-op while connected.
func (m Model) switchGroupCmd(groupID string) tea.Cmd {
	generation := m.connectionGeneration
	return connectionMutationCmd(generation, func() tea.Msg {
		if runtime.GOOS != "windows" && !ipc.ServiceAvailable() {
			return m.connectDirect(groupID)
		}
		return m.runIPCMutation(generation, func() tea.Msg {
			if !ipc.ServiceAvailable() {
				if err := ipc.EnsureHelper(); err != nil {
					return connectResultMsg{err: fmt.Errorf("cannot start helper: %w", err)}
				}
			}
			svc := ipc.NewClient()
			if err := svc.SendSwitch(groupID); err != nil {
				return connectResultMsg{err: fmt.Errorf("service: %w", err)}
			}
			return readServiceSession(svc)
		})
	})
}

// ─── Bubbletea interface ───

func (m Model) Init() tea.Cmd {
	if m.managed {
		return tea.Batch(tickCmd(), m.managedStartCmd())
	}
	if m.debugLogger != nil {
		m.debugLogger.Info("TUI started — v" + version.Version)
		m.debugLogger.Debug("Config: " + m.cfg.APIURL)
		m.debugLogger.Debug("Overlay: " + m.cfg.OverlayIP)
		m.debugLogger.Debug("Broker: " + m.cfg.BrokerEndpoint)
	}
	cmds := []tea.Cmd{tickCmd(), m.restoreState()}
	// Only fetch tunnel status and groups if we have a valid token
	if m.state != StateAuthRequired {
		cmds = append(cmds, m.fetchTunnelStatus(), m.fetchGroups())
	}
	return tea.Batch(cmds...)
}

// restoreState reads persisted state.json to restore mode (exit node / split / group).
func (m Model) restoreState() tea.Cmd {
	return func() tea.Msg {
		state, err := config.LoadState()
		if err != nil {
			return nil
		}
		return restoreStateMsg{state: state}
	}
}

type restoreStateMsg struct {
	state *config.RuntimeState
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.managed {
		return m.updateManaged(msg)
	}
	// ─── Login flow guard: ignore all background messages during login ───
	if m.state == StateLoggingIn {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			// Handle key input for login form
			switch msg.String() {
			case "ctrl+c", "esc":
				m.state = StateAuthRequired
				m.loginEmail = ""
				m.loginOTP = ""
				m.loginError = ""
				if m.debugLogger != nil {
					m.debugLogger.Info("Login cancelled")
				}
				return m, nil
			case "enter":
				if m.loginStep == loginStepEmail {
					if m.loginEmail == "" {
						m.loginEmail = m.cfg.LoginIdentifier()
					}
					if m.loginEmail == "" {
						m.loginError = "Email required"
						return m, nil
					}
					m.loginError = ""
					if m.debugLogger != nil {
						m.debugLogger.Info("Requesting OTP for " + m.loginEmail + "...")
					}
					return m, m.loginRequestOTPCmd(m.loginEmail)
				} else if m.loginStep == loginStepOTP {
					if m.loginOTP == "" {
						m.loginError = "Enter the 6-digit code"
						return m, nil
					}
					m.loginError = ""
					if m.debugLogger != nil {
						m.debugLogger.Info("Verifying OTP code...")
					}
					return m, m.loginVerifyOTPCmd(m.loginEmail, m.loginOTP)
				}
			case "backspace":
				if m.loginStep == loginStepEmail && len(m.loginEmail) > 0 {
					m.loginEmail = m.loginEmail[:len(m.loginEmail)-1]
				} else if m.loginStep == loginStepOTP && len(m.loginOTP) > 0 {
					m.loginOTP = m.loginOTP[:len(m.loginOTP)-1]
				}
				return m, nil
			default:
				ch := msg.String()
				if len(ch) == 1 && ch[0] >= 32 && ch[0] <= 126 {
					if m.loginStep == loginStepEmail {
						m.loginEmail += ch
					} else if m.loginStep == loginStepOTP {
						if ch[0] >= '0' && ch[0] <= '9' && len(m.loginOTP) < 6 {
							m.loginOTP += ch
						}
					}
				}
				return m, nil
			}
			return m, nil
		case tea.WindowSizeMsg:
			m.width = msg.Width
			m.height = msg.Height
			return m, nil
		case spinnerTickMsg:
			m.spinnerIdx = (m.spinnerIdx + 1) % 8
			return m, spinnerTickCmd()
		case tickMsg:
			m.spinnerIdx = (m.spinnerIdx + 1) % 8
			return m, tickCmd()
		case loginOTPRequestMsg:
			if msg.err != nil {
				m.loginError = msg.err.Error()
				if m.debugLogger != nil {
					m.debugLogger.Error("OTP request failed: " + msg.err.Error())
				}
			} else {
				m.loginStep = loginStepOTP
				m.loginOTP = ""
				m.loginError = ""
				if m.debugLogger != nil {
					m.debugLogger.Info("OTP code sent to email — enter it below")
				}
			}
			return m, nil
		case loginOTPVerifyMsg:
			if msg.err != nil {
				m.loginError = msg.err.Error()
				if m.debugLogger != nil {
					m.debugLogger.Error("OTP verification failed: " + msg.err.Error())
				}
			} else {
				config.SaveToken(msg.token)
				m.apiClient.SetToken(msg.token)
				m.loginEmail = ""
				m.loginOTP = ""
				m.loginError = ""
				if m.debugLogger != nil {
					m.debugLogger.Info("Login successful — ready to connect")
				}
				if m.loginRenewAfter {
					m.loginRenewAfter = false
					m.state = StateReconnecting
					m.spinnerIdx = 0
					return m, tea.Batch(m.connectCmd(m.groupID, m.exitNodeID), spinnerTickCmd())
				}
				m.state = StateDisconnected
				return m, m.fetchGroups()
			}
			return m, nil
		default:
			// Ignore ALL other messages during login (tunnelStatusMsg, groupsMsg, etc.)
			return m, nil
		}
	}

	switch msg := msg.(type) {

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "l":
			// Inline login — available when auth expired or disconnected
			if m.state == StateAuthRequired || m.state == StateDisconnected || m.state == StateError {
				m.state = StateLoggingIn
				m.loginStep = loginStepEmail
				m.loginEmail = m.cfg.LoginIdentifier() // Pre-fill from config
				m.loginOTP = ""
				m.loginError = ""
				m.loginRenewAfter = m.loginRenewAfter || (m.overlayIP != "") // Reconnect after expiry cleanup
				m.spinnerIdx = 0
				if m.debugLogger != nil {
					m.debugLogger.Info("Starting login — enter email (or press Enter for " + m.cfg.LoginIdentifier() + ")")
				}
				return m, spinnerTickCmd()
			}
		case "c":
			if m.state == StateDisconnected || m.state == StateAuthRequired {
				// Check if token is expired before trying to connect
				if token, err := config.LoadToken(); err != nil || config.IsTokenExpired(token) {
					m.state = StateAuthRequired
					m.lastRenewError = "Authentication expired — press 'l' to login"
					if m.debugLogger != nil {
						m.debugLogger.Warn("Cannot connect: JWT expired — press 'l' to re-authenticate")
					}
					return m, nil
				}
				if !m.beginExplicitConnection() {
					return m, nil
				}
				m.state = StateConnecting
				m.spinnerIdx = 0
				if m.debugLogger != nil {
					m.debugLogger.Info("Connecting...")
				}
				groupID := ""
				if m.selectedGroup > 0 && m.selectedGroup <= len(m.groups) {
					groupID = m.groups[m.selectedGroup-1].ID
				}
				return m, tea.Batch(m.connectCmd(groupID, m.exitNodeID), spinnerTickCmd())
			}
		case "d":
			if m.state == StateConnected || m.state == StateConnecting || m.state == StateReconnecting {
				// Latch the explicit intent before launching asynchronous teardown.
				// This also cancels connection/reconnection work already in flight.
				m.advanceConnectionGeneration()
				m.userDisconnected = true
				m.state = StateDisconnected
				return m, m.disconnectCmd()
			}
		case "r":
			if m.state == StateConnected {
				if !m.beginExplicitConnection() {
					return m, nil
				}
				m.state = StateReconnecting
				m.spinnerIdx = 0
				return m, tea.Batch(m.connectCmd(m.groupID, m.exitNodeID), spinnerTickCmd())
			}
		case "tab":
			m.tab = (m.tab + 1) % 4
			if m.tab == 2 {
				return m, tea.Batch(tea.ClearScreen, m.fetchDebugInfo())
			}
			return m, tea.ClearScreen

		// ─── Debug/Logs tab scroll/filter ───
		case "pgdown", "ctrl+d":
			if m.tab == 3 {
				m.debugScroll -= 10
				if m.debugScroll < 0 {
					m.debugScroll = 0
					m.debugAutoTail = true
				}
			}
		case "pgup", "ctrl+u":
			if m.tab == 3 {
				m.debugAutoTail = false
				m.debugScroll += 10
			}
		case "G":
			// Jump to bottom (tail)
			if m.tab == 3 {
				m.debugScroll = 0
				m.debugAutoTail = true
			}
		case "g":
			// Jump to top
			if m.tab == 3 {
				m.debugAutoTail = false
				m.debugScroll = 99999 // will be clamped in view
			}
		case "f":
			// Cycle log filter
			if m.tab == 3 {
				m.debugFilter = (m.debugFilter + 1) % 4
				m.debugScroll = 0
				m.debugAutoTail = true
			}

		// ─── Cursor navigation (Groups tab) ───
		case "up", "k":
			if m.tab == 1 {
				if m.cursorInExit {
					if m.cursor > 0 {
						m.cursor--
					} else {
						m.cursorInExit = false
						m.cursor = len(m.groups)
					}
				} else {
					if m.cursor > 0 {
						m.cursor--
					}
				}
			} else if m.tab == 3 {
				m.debugAutoTail = false
				m.debugScroll++
			}
		case "down", "j":
			if m.tab == 1 {
				if m.cursorInExit {
					if m.cursor < len(m.exitNodes)-1 {
						m.cursor++
					}
				} else {
					maxIdx := len(m.groups)
					if m.cursor < maxIdx {
						m.cursor++
					} else if m.vpnMode && len(m.exitNodes) > 0 {
						m.cursorInExit = true
						m.cursor = 0
					}
				}
			} else if m.tab == 3 {
				if m.debugScroll > 0 {
					m.debugScroll--
				}
				if m.debugScroll == 0 {
					m.debugAutoTail = true
				}
			}
		case "enter":
			if m.tab == 1 {
				if m.cursorInExit {
					// Select exit node at cursor
					if m.cursor < len(m.exitNodes) {
						node := m.exitNodes[m.cursor]
						if node.Status == "online" {
							if !m.beginExplicitConnection() {
								return m, nil
							}
							m.selectedExitNode = m.cursor + 1
							m.exitNodeID = node.ID
							m.selectedGroup = 0
							m.groupID = ""
							m.groupName = ""
							m.state = StateReconnecting
							m.errorMsg = ""
							return m, m.connectCmd("", node.ID)
						}
					}
				} else {
					// Select group at cursor
					if m.cursor == 0 {
						// "All groups"
						if m.state == StateConnected && !m.beginExplicitConnection() {
							return m, nil
						}
						m.selectedGroup = 0
						m.groupName = "All groups"
						m.groupID = ""
						m.selectedExitNode = 0
						m.exitNodeID = ""
						if m.state == StateConnected {
							m.state = StateReconnecting
							m.errorMsg = ""
							return m, m.switchGroupCmd("")
						}
					} else if m.cursor <= len(m.groups) {
						g := m.groups[m.cursor-1]
						if g.OnlinePublishers == 0 {
							m.errorMsg = fmt.Sprintf("'%s' has no publishers online", g.Name)
							return m, nil
						}
						if m.state == StateConnected && !m.beginExplicitConnection() {
							return m, nil
						}
						m.selectedGroup = m.cursor
						m.selectedExitNode = 0
						m.exitNodeID = ""
						m.groupName = g.Name
						m.groupID = g.ID
						if m.state == StateConnected {
							m.state = StateReconnecting
							m.errorMsg = ""
							return m, m.switchGroupCmd(g.ID)
						}
					}
				}
			}

		// ─── Number keys still work (Groups tab) ───
		case "0":
			if m.tab == 1 {
				if m.state == StateConnected && !m.beginExplicitConnection() {
					return m, nil
				}
				m.selectedGroup = 0
				m.cursor = 0
				m.cursorInExit = false
				m.groupName = "All groups"
				m.groupID = ""

				if m.state == StateConnected {
					m.state = StateReconnecting
					m.errorMsg = ""
					return m, m.switchGroupCmd("")
				}
			}
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			if m.tab == 1 {
				idx := int(msg.String()[0] - '0')
				if idx <= len(m.groups) {
					g := m.groups[idx-1]

					if g.OnlinePublishers == 0 {
						m.errorMsg = fmt.Sprintf("'%s' has no publishers online", g.Name)
						return m, nil
					}
					if m.state == StateConnected && !m.beginExplicitConnection() {
						return m, nil
					}

					m.selectedGroup = idx
					m.cursor = idx
					m.cursorInExit = false
					m.selectedExitNode = 0
					m.exitNodeID = ""
					m.groupName = g.Name
					m.groupID = g.ID

					if m.state == StateConnected {
						m.state = StateReconnecting
						m.errorMsg = ""
						return m, m.switchGroupCmd(g.ID)
					}
				}
			}
		case "e":
			if m.tab == 1 && m.vpnMode && len(m.exitNodes) > 0 {
				m.selectedExitNode++
				if m.selectedExitNode > len(m.exitNodes) {
					m.selectedExitNode = 1
				}
				node := m.exitNodes[m.selectedExitNode-1]
				if node.Status == "online" {
					if !m.beginExplicitConnection() {
						return m, nil
					}
					m.exitNodeID = node.ID
					m.selectedGroup = 0
					m.groupID = ""
					m.groupName = ""
					m.state = StateReconnecting
					m.errorMsg = ""
					return m, m.connectCmd("", node.ID)
				}
			}
		case "s":
			if m.tab == 1 && m.selectedExitNode > 0 {
				if !m.beginExplicitConnection() {
					return m, nil
				}
				m.selectedExitNode = 0
				m.exitNodeID = ""
				m.cursorInExit = false
				m.state = StateReconnecting
				m.errorMsg = ""
				// Send CmdExitNode with empty ID to switch back to split tunnel.
				// Can't use connectCmd("") because it maps empty exitNodeID to CmdConnect.
				return m, m.switchToSplitCmd()
			}
		case "p":
			// Quick ping test (available when connected, re-runnable)
			if m.state == StateConnected {
				m.pingRunning = true
				m.pingResults = nil
				m.pingIdx = 0
				return m, tea.Batch(m.runPingTest(), spinnerTickCmd())
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		// Advance spinner when connecting
		if m.state == StateConnecting || m.state == StateReconnecting || m.state == StateLoggingIn {
			m.spinnerIdx = (m.spinnerIdx + 1) % 8
		}
		cmds := []tea.Cmd{tickCmd()}
		// Only poll tunnel status when it's relevant (not during login/auth flows)
		if m.state != StateLoggingIn && m.state != StateAuthRequired {
			cmds = append(cmds, m.fetchTunnelStatus())
		}
		if m.state == StateConnected {
			cmds = append(cmds, m.measureLatency())

			// Service-backed tunnels are service-owned; its daemon renews the
			// session and PSK. A second TUI renewal would race IPC mode/group
			// transitions and has no direct Tunnel handle to update.
			if runtime.GOOS == "windows" || ipc.ServiceAvailable() {
				return m, tea.Batch(cmds...)
			}

			// ─── Auto-renewal check (every ~30s) ───
			m.renewTickCount++
			if m.renewTickCount >= renewCheckInterval && !m.renewInFlight {
				m.renewTickCount = 0

				// Check if JWT token itself is expired first
				if token, err := config.LoadToken(); err == nil && config.IsTokenExpired(token) {
					// JWT expired — remove a dead full tunnel before asking the user to login.
					m.lastRenewError = "Authentication expired"
					if m.debugLogger != nil {
						m.debugLogger.Warn("JWT expired — press 'l' to re-authenticate")
					}
					if m.shouldCleanupExpiredExitNode() {
						m.state = StateReconnecting
						m.loginRenewAfter = true
						cmds = append(cmds, m.cleanupExpiredExitNodeCmd(exitNodeCleanupRequireAuth))
					} else {
						m.state = StateAuthRequired
					}
					return m, tea.Batch(cmds...)
				}

				// Check if session is approaching expiry
				if !m.expiresAt.IsZero() {
					remaining := time.Until(m.expiresAt)
					if remaining <= 0 {
						// A full tunnel must be removed before renewal; otherwise the
						// API request itself may be routed through the expired tunnel.
						if m.debugLogger != nil {
							m.debugLogger.Info("Session expired — auto-reconnecting...")
						}
						m.state = StateReconnecting
						m.spinnerIdx = 0
						if m.shouldCleanupExpiredExitNode() {
							cmds = append(cmds, m.cleanupExpiredExitNodeCmd(exitNodeCleanupReconnect), spinnerTickCmd())
						} else {
							cmds = append(cmds, m.connectCmd(m.groupID, m.exitNodeID), spinnerTickCmd())
						}
						return m, tea.Batch(cmds...)
					} else if remaining <= renewBefore {
						// Approaching expiry — trigger auto-renewal
						m.renewInFlight = true
						if m.debugLogger != nil {
							m.debugLogger.Info(fmt.Sprintf("Session expires in %s — auto-renewing...", remaining.Round(time.Second)))
						}
						cmds = append(cmds, m.autoRenewCmd())
					}
				}
			}
		}
		return m, tea.Batch(cmds...)

	case spinnerTickMsg:
		if m.state == StateConnecting || m.state == StateReconnecting || m.state == StateLoggingIn {
			m.spinnerIdx = (m.spinnerIdx + 1) % 8
			return m, spinnerTickCmd()
		}
		if m.pingRunning {
			m.pingIdx = (m.pingIdx + 1) % 8
			return m, spinnerTickCmd()
		}

	case tunnelStatusMsg:
		// A manual disconnect wins over status responses that were queued before
		// the teardown completed, as well as over stale interfaces during cleanup.
		if m.userDisconnected {
			m.state = StateDisconnected
			return m, nil
		}
		if msg.err != nil {
			// No tunnel found — mark disconnected only if we weren't connecting or logging in
			if m.state != StateConnecting && m.state != StateReconnecting && m.state != StateLoggingIn && m.state != StateAuthRequired {
				m.state = StateDisconnected
			}
		} else {
			// Record rx delta for sparkline
			if m.lastRxSample > 0 && msg.info.RxBytes >= m.lastRxSample {
				delta := msg.info.RxBytes - m.lastRxSample
				m.rxHistory[m.rxHistoryIdx] = delta
				m.rxHistoryIdx = (m.rxHistoryIdx + 1) % len(m.rxHistory)
				if m.rxHistoryIdx == 0 {
					m.rxHistoryFull = true
				}
			}
			m.lastRxSample = msg.info.RxBytes

			m.overlayIP = msg.info.OverlayIP
			m.endpoint = msg.info.Endpoint

			// Log handshake state changes
			if !msg.info.LastHandshake.Equal(m.lastHandshake) && !msg.info.LastHandshake.IsZero() {
				age := time.Since(msg.info.LastHandshake).Round(time.Second)
				if m.debugLogger != nil {
					if age > 180*time.Second {
						m.debugLogger.Warn(fmt.Sprintf("Handshake stale: %s ago", age))
					} else {
						m.debugLogger.Debug(fmt.Sprintf("Handshake refreshed: %s ago", age))
					}
				}
			}

			m.lastHandshake = msg.info.LastHandshake
			m.rxBytes = msg.info.RxBytes
			m.txBytes = msg.info.TxBytes

			// Sync mode/group state from service so changes made via
			// the GUI are reflected in the TUI (and vice versa).
			if msg.serviceStatus != "" {
				m.isExitNode = msg.isExitNode
				m.exitNodeName = msg.exitNodeName
				if msg.exitNodeID != "" || !msg.isExitNode {
					m.exitNodeID = msg.exitNodeID
				}
				if msg.groupName != "" {
					m.groupName = msg.groupName
				}
				if msg.groupID != "" {
					m.groupID = msg.groupID
				}
				if msg.sessionID != "" {
					m.sessionID = msg.sessionID
				}
				if !msg.expiresAt.IsZero() {
					m.expiresAt = msg.expiresAt
				}
			}

			// If we detect an active tunnel (from renew-session.sh or a previous connect),
			// adopt it and show as connected.
			if m.state == StateDisconnected || m.state == StateConnected {
				if !msg.info.LastHandshake.IsZero() && time.Since(msg.info.LastHandshake) < 180*time.Second {
					m.state = StateConnected
					// Fetch session info to populate session details
					if m.sessionID == "" {
						return m, m.fetchSessionInfo()
					}
				} else if msg.info.OverlayIP != "" {
					// ─── Auto-reconnect stale tunnel after suspend/resume ───
					// Interface exists but handshake is stale (laptop was asleep).
					// If we have a saved group/exitNode and valid JWT, reconnect
					// automatically instead of showing a dead tunnel.
					if m.state == StateDisconnected && (m.groupID != "" || m.exitNodeID != "") {
						token, err := config.LoadToken()
						if err == nil && token != "" && !config.IsTokenExpired(token) {
							if m.debugLogger != nil {
								m.debugLogger.Info("Tunnel stale after wake — auto-reconnecting...")
							}
							m.state = StateReconnecting
							m.spinnerIdx = 0
							return m, tea.Batch(m.connectCmd(m.groupID, m.exitNodeID), spinnerTickCmd())
						}
					}
					// Fallback: show as connected (stale but interface present)
					m.state = StateConnected
				}
			}
		}

	case sessionInfoMsg:
		if msg.err == nil {
			m.sessionID = msg.info.SessionID
			m.expiresAt = msg.info.ExpiresAt.Time
			m.ttlSeconds = msg.info.TTLRemaining
			if msg.info.SelectedGroupName != "" {
				m.groupName = msg.info.SelectedGroupName
			}
			// Store publisher info
			m.publisherEndpoint = m.cfg.BrokerEndpoint
			for _, g := range m.groups {
				if g.ID == msg.info.SelectedGroupID || g.Name == msg.info.SelectedGroupName {
					for _, p := range g.Publishers {
						if p.Status == "online" {
							m.publisherName = p.Name
							break
						}
					}
					break
				}
			}
		}

	case groupsMsg:
		if msg.err == nil {
			m.groups = msg.groups.Groups
			m.hasOverlap = msg.groups.HasOverlap
			m.vpnMode = msg.groups.VPNMode

			// Filter exit nodes — only show online publishers as VPN locations
			onlineNodes := make([]api.ExitNodeInfo, 0, len(msg.groups.ExitNodes))
			for _, node := range msg.groups.ExitNodes {
				if node.Status == "online" {
					onlineNodes = append(onlineNodes, node)
				}
			}
			m.exitNodes = onlineNodes

			// Reset exit node selection if the previously selected node went offline
			if m.selectedExitNode > len(m.exitNodes) {
				m.selectedExitNode = 0
				m.exitNodeID = ""
			}
		}

	case debugInfoMsg:
		m.debugInfo = msg.info

	case latencyMsg:
		m.latencyMs = msg.ms
		if msg.ms < 0 {
			m.latencyColor = 2 // bad
			if m.debugLogger != nil {
				m.debugLogger.Warn("Latency: timeout (broker unreachable)")
			}
		} else if msg.ms < 50 {
			m.latencyColor = 0 // good
			if msg.ms == 0 {
				m.latencyMs = 1
			}
		} else if msg.ms < 150 {
			m.latencyColor = 1 // medium
			if m.debugLogger != nil {
				m.debugLogger.Debug(fmt.Sprintf("Latency: %dms (elevated)", msg.ms))
			}
		} else {
			m.latencyColor = 2 // bad
			if m.debugLogger != nil {
				m.debugLogger.Warn(fmt.Sprintf("Latency: %dms (high)", msg.ms))
			}
		}

	case pingResultMsg:
		m.pingResults = msg.results
		m.pingRunning = false
		if m.debugLogger != nil {
			for _, t := range msg.results {
				if t.ok {
					m.debugLogger.Info(fmt.Sprintf("Ping %s (%s): %dms ✓", t.name, t.ip, t.latency))
				} else {
					m.debugLogger.Warn(fmt.Sprintf("Ping %s (%s): timeout ✗", t.name, t.ip))
				}
			}
		}

	case restoreStateMsg:
		if msg.state != nil {
			m.sessionID = msg.state.SessionID
			m.expiresAt = msg.state.ExpiresAt
			m.groupID = msg.state.GroupID
			m.groupName = msg.state.GroupName
			m.exitNodeID = msg.state.ExitNodeID
			m.isExitNode = msg.state.IsExitNode
			m.userDisconnected = msg.state.UserDisconnected
			m.lifecycleGeneration = msg.state.LifecycleGeneration

			if msg.state.IsExitNode {
				// Restore exit node selection index by matching ID
				for i, node := range m.exitNodes {
					if node.ID == msg.state.ExitNodeID {
						m.selectedExitNode = i + 1
						break
					}
				}
				// If exitNodes aren't loaded yet, just mark by ID
				if m.selectedExitNode == 0 && msg.state.ExitNodeID != "" {
					m.selectedExitNode = 1 // placeholder until groups load
				}
			} else if msg.state.GroupID != "" {
				// Restore group selection index
				for i, g := range m.groups {
					if g.ID == msg.state.GroupID {
						m.selectedGroup = i + 1
						break
					}
				}
			}

			if m.userDisconnected {
				m.state = StateDisconnected
				return m, nil
			}

			token, tokenErr := config.LoadToken()
			tokenValid := tokenErr == nil && token != "" && !config.IsTokenExpired(token)

			// A TUI that was closed cannot monitor expiry. On relaunch, remove an
			// orphaned full tunnel before login so its default route cannot blackhole
			// the authentication request.
			if !tokenValid && m.shouldCleanupExpiredExitNode() {
				m.state = StateReconnecting
				m.loginRenewAfter = true
				m.lastRenewError = "Authentication expired"
				if m.debugLogger != nil {
					m.debugLogger.Info("Expired authentication with active exit node — restoring local connectivity...")
				}
				return m, m.cleanupExpiredExitNodeCmd(exitNodeCleanupRequireAuth)
			}

			// ─── Auto-reconnect after suspend/resume or a closed TUI ───
			if !m.expiresAt.IsZero() && time.Now().After(m.expiresAt) && tokenValid {
				if m.debugLogger != nil {
					m.debugLogger.Info("Session expired while TUI was inactive — auto-reconnecting...")
				}
				m.state = StateReconnecting
				m.spinnerIdx = 0
				if m.shouldCleanupExpiredExitNode() {
					return m, tea.Batch(m.cleanupExpiredExitNodeCmd(exitNodeCleanupReconnect), spinnerTickCmd())
				}
				return m, tea.Batch(m.connectCmd(m.groupID, m.exitNodeID), spinnerTickCmd())
			}
		}

	case connectionMutationMsg:
		if msg.generation != m.connectionGeneration || msg.result == nil {
			return m, nil
		}
		return m.Update(msg.result)

	case connectResultMsg:
		if msg.lifecycleGeneration != 0 && !config.IsLifecycleCurrent(msg.lifecycleGeneration) {
			if msg.tunnel != nil {
				msg.tunnel.Down()
			}
			if cleanupErr := cleanupStaleTUIDNS(); cleanupErr != nil {
				m.errorMsg = "stale connect cleanup: " + cleanupErr.Error()
			}
			m.state = StateDisconnected
			m.tunnel = nil
			return m, nil
		}
		if m.userDisconnected {
			m.state = StateDisconnected
			return m, nil
		}
		if msg.err != nil {
			m.errorMsg = msg.err.Error()
			if isAuthenticationError(msg.err) {
				m.lastRenewError = "Authentication expired"
				m.loginRenewAfter = m.exitNodeID != ""
				if m.debugLogger != nil {
					m.debugLogger.Warn("Reconnect failed: authentication expired")
				}
				if m.shouldCleanupExpiredExitNode() {
					m.state = StateReconnecting
					return m, m.cleanupExpiredExitNodeCmd(exitNodeCleanupRequireAuth)
				}
				m.state = StateAuthRequired
			} else {
				m.state = StateError
			}
			if m.debugLogger != nil {
				m.debugLogger.Error("Connect failed: " + msg.err.Error())
			}
		} else {
			m.userDisconnected = false
			m.state = StateConnected
			if msg.tunnel != nil {
				// Direct Unix connections retain the active Tunnel so auto-renew can
				// apply the newly issued PSK before persisting it.
				m.tunnel = msg.tunnel
			}
			m.sessionID = msg.session.SessionID
			m.expiresAt = msg.session.ExpiresAt.Time
			m.ttlSeconds = msg.session.TTLSeconds
			m.isExitNode = msg.session.IsExitNode
			if m.debugLogger != nil {
				m.debugLogger.Info("Connected — session " + msg.session.SessionID[:8] + "...")
			}

			// Direct and service-backed connect paths persist before publishing
			// success; the UI must not perform a second, potentially stale write.

			return m, m.fetchTunnelStatus()
		}

	case disconnectMsg:
		m.userDisconnected = true
		m.state = StateDisconnected
		m.overlayIP = ""
		m.endpoint = ""
		m.lastHandshake = time.Time{}
		m.rxBytes = 0
		m.txBytes = 0
		m.isExitNode = false
		m.tunnel = nil
		if msg.err != nil {
			m.errorMsg = fmt.Sprintf("disconnect warning: %v", msg.err)
		}

	case exitNodeCleanupMsg:
		if msg.tunnelErr != nil {
			var routeCleanupErr *tunnel.BrokerRouteCleanupError
			if !errors.As(msg.tunnelErr, &routeCleanupErr) {
				// Preserve exit-node state and keep login blocked only when the
				// interface teardown itself failed; default routes may still blackhole OTP.
				m.errorMsg = fmt.Sprintf("full VPN cleanup failed, retrying: %v", msg.tunnelErr)
				m.state = StateReconnecting
				if msg.action == exitNodeCleanupRequireAuth {
					m.lastRenewError = "Authentication expired — retrying local connectivity restore"
				}
				if m.debugLogger != nil {
					m.debugLogger.Error(m.errorMsg)
				}
				return m, retryExitNodeCleanupCmd(msg.action)
			}

			// BrokerRouteCleanupError guarantees downOS succeeded. Continue with
			// authentication/reconnect and keep the residual route visible as a warning.
			m.errorMsg = fmt.Sprintf("full VPN removed; broker route cleanup warning: %v", msg.tunnelErr)
			if m.debugLogger != nil {
				m.debugLogger.Warn(m.errorMsg)
			}
		}

		m.overlayIP = ""
		m.endpoint = ""
		m.lastHandshake = time.Time{}
		m.rxBytes = 0
		m.txBytes = 0
		m.isExitNode = false
		m.tunnel = nil

		if msg.dnsErr != nil {
			m.errorMsg = fmt.Sprintf("full VPN removed; DNS cleanup warning: %v", msg.dnsErr)
			if m.debugLogger != nil {
				m.debugLogger.Warn(m.errorMsg)
			}
		}
		if m.debugLogger != nil {
			m.debugLogger.Info("Expired full VPN removed — local connectivity restored")
		}
		if msg.action == exitNodeCleanupReconnect {
			m.state = StateReconnecting
			m.spinnerIdx = 0
			return m, tea.Batch(m.connectCmd(m.groupID, m.exitNodeID), spinnerTickCmd())
		}
		m.state = StateAuthRequired

	case retryExitNodeCleanupMsg:
		return m, m.cleanupExpiredExitNodeCmd(msg.action)

	case renewResultMsg:
		// Results from a renewal that preceded an explicit mode/group/reconnect
		// action are stale. The serialized API call still completes first, but
		// the newer explicit renewal remains the final broker mutation.
		if msg.generation != m.connectionGeneration {
			return m, nil
		}
		m.renewInFlight = false
		// A renewal started before an explicit disconnect must not update the
		// tunnel or persist an older connected-state snapshot afterward.
		if m.userDisconnected {
			return m, nil
		}
		if msg.err != nil {
			errStr := msg.err.Error()
			// Detect auth failure (401/403) — remove full VPN before login.
			if isAuthenticationError(msg.err) {
				m.lastRenewError = "Authentication expired"
				if m.debugLogger != nil {
					m.debugLogger.Warn("Auto-renewal failed: JWT expired")
				}
				if m.shouldCleanupExpiredExitNode() {
					m.state = StateReconnecting
					m.loginRenewAfter = true
					return m, m.cleanupExpiredExitNodeCmd(exitNodeCleanupRequireAuth)
				}
				m.state = StateAuthRequired
			} else {
				m.lastRenewError = errStr
				if m.debugLogger != nil {
					m.debugLogger.Error("Auto-renewal failed: " + errStr)
				}
			}
		} else {
			if !config.IsLifecycleCurrent(m.lifecycleGeneration) {
				m.lastRenewError = config.ErrStaleLifecycle.Error()
				return m, nil
			}
			// The control plane has already issued this PSK. It must reach the
			// dataplane before session metadata is updated or persisted.
			var updateErr error
			if m.tunnel == nil {
				updateErr = fmt.Errorf("active tunnel handle unavailable")
			} else {
				updateErr = m.tunnel.UpdatePSK(msg.session.PresharedKey)
			}
			if updateErr != nil {
				m.lastRenewError = "PSK update failed: " + updateErr.Error()
				m.state = StateReconnecting
				m.spinnerIdx = 0
				if m.debugLogger != nil {
					m.debugLogger.Warn(m.lastRenewError + " — reconnecting without persisting unapplied session state")
				}
				if m.shouldCleanupExpiredExitNode() {
					return m, tea.Batch(m.cleanupExpiredExitNodeCmd(exitNodeCleanupReconnect), spinnerTickCmd())
				}
				return m, tea.Batch(m.connectCmd(m.groupID, m.exitNodeID), spinnerTickCmd())
			}

			// Persist only after the active dataplane accepted the PSK. A failed
			// durable commit is surfaced and never logged as renewal success; keep
			// the applied expiry in memory so the next tick cannot double-renew.
			state := &config.RuntimeState{
				SessionID: msg.session.SessionID, ExpiresAt: msg.session.ExpiresAt.Time,
				PresharedKey: msg.session.PresharedKey, GroupID: m.groupID,
				GroupName: m.groupName, ExitNodeID: m.exitNodeID,
				IsExitNode: msg.session.IsExitNode, ConnectedAt: time.Now(),
			}
			if err := saveTUIState(m.lifecycleGeneration, state); err != nil {
				m.lastRenewError = "State save failed after PSK apply: " + err.Error()
				m.expiresAt = msg.session.ExpiresAt.Time
				if m.debugLogger != nil {
					m.debugLogger.Error(m.lastRenewError)
				}
				return m, nil
			}
			if !config.IsLifecycleCurrent(m.lifecycleGeneration) {
				m.lastRenewError = config.ErrStaleLifecycle.Error()
				return m, nil
			}

			m.lastRenewError = ""
			m.sessionID = msg.session.SessionID
			m.expiresAt = msg.session.ExpiresAt.Time
			m.ttlSeconds = msg.session.TTLSeconds
			m.isExitNode = msg.session.IsExitNode
			if m.debugLogger != nil {
				m.debugLogger.Info(fmt.Sprintf("Session renewed — expires at %s", msg.session.ExpiresAt.Time.Local().Format("15:04:05")))
			}
		}

	case loginOTPRequestMsg:
		if msg.err != nil {
			m.loginError = msg.err.Error()
			if m.debugLogger != nil {
				m.debugLogger.Error("OTP request failed: " + msg.err.Error())
			}
		} else {
			m.loginStep = loginStepOTP
			m.loginOTP = ""
			m.loginError = ""
			if m.debugLogger != nil {
				m.debugLogger.Info("OTP code sent to email — enter it below")
			}
		}

	case loginOTPVerifyMsg:
		if msg.err != nil {
			m.loginError = msg.err.Error()
			if m.debugLogger != nil {
				m.debugLogger.Error("OTP verification failed: " + msg.err.Error())
			}
		} else {
			// Login successful — save token and restore connection
			config.SaveToken(msg.token)
			m.apiClient.SetToken(msg.token)
			m.loginEmail = ""
			m.loginOTP = ""
			m.loginError = ""
			if m.debugLogger != nil {
				m.debugLogger.Info("Login successful — reconnecting...")
			}

			// Auto-reconnect if we were connected before auth expired
			if m.loginRenewAfter {
				m.loginRenewAfter = false
				m.state = StateReconnecting
				m.spinnerIdx = 0
				return m, tea.Batch(m.connectCmd(m.groupID, m.exitNodeID), spinnerTickCmd())
			}
			m.state = StateDisconnected
			return m, m.fetchGroups()
		}
	}

	return m, nil
}

// ─── Helpers ───

func wrapTUILifecycleError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func waitForDirectTunnelHealth(ifaceName string, timeout time.Duration, current func() bool) error {
	deadline := time.Now().Add(timeout)
	lastErr := fmt.Errorf("no recent handshake")
	for time.Now().Before(deadline) {
		if current != nil && !current() {
			return errStaleSessionOperation
		}
		info, err := tunnel.GetStatus(ifaceName)
		if err == nil && !info.LastHandshake.IsZero() && time.Since(info.LastHandshake) < 180*time.Second {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("no recent handshake")
		}
		time.Sleep(time.Second)
	}
	return lastErr
}

func (m Model) connectionCurrent(generation uint64) bool {
	return m.sessionCoordinator == nil || generation == m.sessionCoordinator.generation.Load()
}

func (m Model) lifecycleCurrent(generation uint64) bool {
	return m.connectionCurrent(generation) && config.IsLifecycleCurrent(m.lifecycleGeneration)
}

func (m *Model) advanceConnectionGeneration() {
	if m.sessionCoordinator != nil {
		m.connectionGeneration = m.sessionCoordinator.generation.Add(1)
	} else {
		m.connectionGeneration++
	}
	m.renewInFlight = false
	m.renewTickCount = 0
}

func (m *Model) beginExplicitConnection() bool {
	// On Windows, the service owns the durable lifecycle generation.
	if runtime.GOOS != "windows" {
		generation, err := config.BeginLifecycle(false)
		if err != nil {
			m.state = StateError
			m.errorMsg = "Cannot persist connect intent: " + err.Error()
			return false
		}
		m.lifecycleGeneration = generation
	}
	m.advanceConnectionGeneration()
	m.userDisconnected = false
	return true
}

// shouldCleanupExpiredExitNode limits the forced teardown to direct Unix
// clients. Service-managed platforms own their tunnel lifecycle independently.
func (m Model) shouldCleanupExpiredExitNode() bool {
	return m.isExitNode && (runtime.GOOS == "darwin" || runtime.GOOS == "linux") && !ipc.ServiceAvailable()
}

func isAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "token rejected") ||
		strings.Contains(errStr, "HTTP 401")
}

// pingHost performs a quick connectivity test via TCP connect
func pingHost(host string) string {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", host+":80", 3*time.Second)
	if err != nil {
		// Try port 443
		conn, err = net.DialTimeout("tcp", host+":443", 3*time.Second)
		if err != nil {
			return "timeout"
		}
	}
	conn.Close()
	ms := time.Since(start).Milliseconds()
	if ms < 1 {
		return "<1ms ✓"
	}
	return fmt.Sprintf("%dms ✓", ms)
}

// pingOneTarget performs a connectivity test and returns a structured result
func pingOneTarget(name, ip string) pingTarget {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", ip+":80", 3*time.Second)
	if err != nil {
		// Fallback to UDP (for hosts without TCP 80)
		conn, err = net.DialTimeout("udp", ip+":53", 2*time.Second)
		if err != nil {
			return pingTarget{name: name, ip: ip, latency: -1, ok: false}
		}
		conn.Close()
		// UDP dial doesn't measure real latency, mark as reachable with 0ms
		return pingTarget{name: name, ip: ip, latency: 0, ok: true}
	}
	conn.Close()
	ms := time.Since(start).Milliseconds()
	if ms == 0 {
		ms = 1
	}
	return pingTarget{name: name, ip: ip, latency: ms, ok: true}
}

// firstHostFromCIDR extracts the first usable host IP from a CIDR string.
// E.g., "10.50.0.0/16" → "10.50.0.1", "192.168.1.0/24" → "192.168.1.1"
func firstHostFromCIDR(cidr string) string {
	parts := strings.Split(cidr, "/")
	if len(parts) != 2 {
		return ""
	}
	ip := net.ParseIP(parts[0])
	if ip == nil {
		return ""
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return ""
	}
	// Increment last octet by 1 to get first host
	ip4[3]++
	return ip4.String()
}

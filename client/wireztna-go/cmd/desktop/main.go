// wireztna-desktop is the system tray application for WireZTNA.
//
// Architecture (Windows):
//
//	Tray app (unprivileged) → IPC named pipe → WireZTNA Service (LocalSystem)
//	The service owns the wintun adapter and manages the tunnel.
//	The tray app is a UI-only controller that sends commands and reads state.
//
// Design: Pure native systray menus — no WebView2 windows for normal operation.
// WebView2 is only used for the first-time enrollment/OTP wizard.
//
// Build: go build -ldflags "-H windowsgui" -o wireztna-desktop.exe ./cmd/desktop
package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/systray"
	"github.com/wireztna/client/cmd/desktop/icons"
	"github.com/wireztna/client/internal/ipc"
	"github.com/wireztna/client/pkg/version"
)

// appState holds the current state for the tray UI.
type appState struct {
	mu           sync.RWMutex
	status       string
	overlayIP    string
	groups       []ipc.Group
	exitNodes    []ipc.ExitNode
	vpnMode      bool
	hasOverlap   bool
	isExitNode   bool
	exitNodeName string
	groupName    string
	groupID      string
	needsEnroll  bool
	needsLogin   bool
	email        string
	rxBytes      int64
	txBytes      int64
	expiresAt    time.Time
	sessionID    string
}

var (
	app       *appState
	ipcClient *ipc.Client

	// Menu items — top level
	mStatus     *systray.MenuItem
	mConnect    *systray.MenuItem
	mDisconnect *systray.MenuItem
	mMode       *systray.MenuItem
	mGroups     *systray.MenuItem
	mInfo       *systray.MenuItem // traffic + session info
	mTraffic    *systray.MenuItem
	mSession    *systray.MenuItem
	mSetup      *systray.MenuItem
	mQuit       *systray.MenuItem

	// Dynamic sub-menu items
	mSplitTunnel  *systray.MenuItem
	exitNodeItems []*systray.MenuItem
	groupItems    []*systray.MenuItem
)

func main() {
	// Prevent multiple tray instances (each would create its own system tray icon)
	ensureSingleInstance()

	app = &appState{status: trayStatusUnknown}
	ipcClient = ipc.NewClient()

	// Ensure the background service is running
	ensureServiceAvailable()

	systray.Run(onReady, onExit)
}

func onReady() {
	applyTrayPresentation(classifyTrayPresentation(nil, time.Now()))
	systray.SetTitle("")

	// ─── Status line (informational, disabled) ───
	mStatus = systray.AddMenuItem("◌ Checking status...", "")
	mStatus.Disable()

	systray.AddSeparator()

	// ─── Connect / Disconnect ───
	mConnect = systray.AddMenuItem("Connect", "Connect to WireZTNA network")
	mDisconnect = systray.AddMenuItem("Disconnect", "Disconnect from WireZTNA network")
	mDisconnect.Hide()

	// ─── Mode submenu (VPN / Split) — hidden until connected + VPN mode ───
	mMode = systray.AddMenuItem("Mode", "Switch between Split Tunnel and VPN")
	mMode.Hide()
	mSplitTunnel = mMode.AddSubMenuItem("Split Tunnel (internal only)", "Route only internal traffic through tunnel")
	go func() {
		for range mSplitTunnel.ClickedCh {
			go func() {
				updateStatus(ipc.StatusReconnecting)
				showNotification("Switching Mode", "Changing to Split Tunnel...")
				err := ipcWithTimeout(func() error { return ipcClient.SendExitNode("") }, 35*time.Second)
				if err != nil {
					showNotification("Mode Switch Failed", err.Error())
				} else {
					showNotification("Split Tunnel", "Now routing only internal traffic")
				}
				time.Sleep(500 * time.Millisecond)
				refreshState()
			}()
		}
	}()

	// ─── Groups submenu — hidden until overlap detected ───
	mGroups = systray.AddMenuItem("Project", "Select a project/group")
	mGroups.Hide()

	systray.AddSeparator()

	// ─── Info section (traffic + session) — hidden until connected ───
	mInfo = systray.AddMenuItem("", "")
	mInfo.Hide()
	mInfo.Disable()
	mTraffic = systray.AddMenuItem("", "")
	mTraffic.Hide()
	mTraffic.Disable()
	mSession = systray.AddMenuItem("", "")
	mSession.Hide()
	mSession.Disable()

	systray.AddSeparator()

	// ─── Setup wizard (only for enroll/login) ───
	mSetup = systray.AddMenuItem("Setup...", "Enrollment and login wizard")

	// ─── Version + Quit ───
	systray.AddSeparator()
	mVersion := systray.AddMenuItem(fmt.Sprintf("v%s", version.Version), "")
	mVersion.Disable()
	mQuit = systray.AddMenuItem("Quit WireZTNA", "Close the tray application")

	// Start background polling
	go pollLoop()

	// Handle menu clicks
	go handleClicks()
}

func onExit() {}

func handleClicks() {
	for {
		select {
		case <-mConnect.ClickedCh:
			go doConnect()
		case <-mDisconnect.ClickedCh:
			go doDisconnect()
		case <-mSetup.ClickedCh:
			go openSetupWizard()
		case <-mQuit.ClickedCh:
			systray.Quit()
			os.Exit(0)
		}
	}
}

// ─── Actions ───

func doConnect() {
	app.mu.RLock()
	needsEnroll := app.needsEnroll
	needsLogin := app.needsLogin
	app.mu.RUnlock()

	if needsEnroll || needsLogin {
		openSetupWizard()
		return
	}

	updateStatus(ipc.StatusConnecting)
	err := ipcWithTimeout(func() error { return ipcClient.SendConnect("") }, 35*time.Second)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "not enrolled") {
			openAuthFlow("enroll", nil)
			return
		}
		if strings.Contains(errMsg, "not authenticated") || strings.Contains(errMsg, "expired") {
			openAuthFlow("login", nil)
			return
		}
		showNotification("Connection Failed", errMsg)
		updateStatus(ipc.StatusError)
		return
	}
	showNotification("Connected", "WireZTNA tunnel is active")
	refreshState()
}

func doDisconnect() {
	err := ipcWithTimeout(func() error { return ipcClient.SendDisconnect() }, 10*time.Second)
	if err != nil {
		showNotification("Disconnect Failed", err.Error())
		return
	}
	showNotification("Disconnected", "Tunnel closed")
	refreshState()
}

func openSetupWizard() {
	app.mu.RLock()
	needsEnroll := app.needsEnroll
	app.mu.RUnlock()

	step := "login"
	if needsEnroll {
		step = "enroll"
	}
	openAuthFlow(step, func() {
		refreshState()
		showNotification("Connected", "WireZTNA is active")
	})
}

// ─── State Management ───

func pollLoop() {
	// Initial state fetch
	refreshState()

	// Auto-open wizard if not enrolled
	app.mu.RLock()
	needsEnroll := app.needsEnroll
	needsLogin := app.needsLogin
	status := app.status
	app.mu.RUnlock()

	if needsEnroll {
		showNotification("WireZTNA Setup", "Your device needs to be configured")
		openAuthFlow("enroll", func() { refreshState() })
	} else if needsLogin {
		showNotification("WireZTNA", "Sign in to connect")
		openAuthFlow("login", func() { refreshState() })
	} else if status == ipc.StatusDisconnected {
		showNotification("WireZTNA", "Ready — right-click to connect")
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		refreshState()
	}
}

func refreshState() {
	state, err := ipcClient.SendStatus()
	if err != nil {
		updateStatus("service_unavailable")
		return
	}

	app.mu.Lock()
	app.status = state.Status
	app.overlayIP = state.OverlayIP
	app.isExitNode = state.IsExitNode
	app.exitNodeName = state.ExitNodeName
	app.groupName = state.GroupName
	app.groupID = state.GroupID
	app.needsEnroll = state.NeedsEnroll
	app.needsLogin = state.NeedsLogin
	app.email = state.Email
	app.rxBytes = state.RxBytes
	app.txBytes = state.TxBytes
	app.expiresAt = state.ExpiresAt
	app.sessionID = state.SessionID
	app.mu.Unlock()

	updateUI(state)

	// Fetch groups/exit nodes periodically (only if ready)
	if !state.NeedsEnroll && !state.NeedsLogin {
		go fetchGroupsAndModes()
	}
}

func fetchGroupsAndModes() {
	groupState, err := ipcClient.SendGroups()
	if err != nil || groupState == nil {
		return
	}

	app.mu.Lock()
	app.groups = groupState.Groups
	app.exitNodes = groupState.ExitNodes
	app.vpnMode = groupState.VPNMode
	app.hasOverlap = groupState.HasOverlap
	app.mu.Unlock()

	updateModeMenu(groupState)
	updateGroupsMenu(groupState)
}

// ─── UI Updates ───

const (
	trayStatusUnknown            = "unknown"
	trayStatusServiceUnavailable = "service_unavailable"
	maxHealthyHandshakeAge       = 3 * time.Minute
)

type trayVisualState uint8

const (
	trayVisualAttention trayVisualState = iota
	trayVisualConnected
	trayVisualDisconnected
)

type trayPresentation struct {
	visual     trayVisualState
	tooltip    string
	statusText string
}

// classifyTrayPresentation maps observed service state to a platform-neutral
// tray presentation. A connected service is green only after WireGuard health
// has been confirmed by a recent, non-future handshake.
func classifyTrayPresentation(state *ipc.State, now time.Time) trayPresentation {
	if state == nil || state.Status == "" || state.Status == trayStatusUnknown {
		return trayPresentation{trayVisualAttention, "WireZTNA — Checking status...", "◌ Checking status..."}
	}
	if state.NeedsEnroll {
		return trayPresentation{trayVisualAttention, "WireZTNA — Setup required", "○ Setup required"}
	}
	if state.NeedsLogin {
		return trayPresentation{trayVisualAttention, "WireZTNA — Sign in required", "○ Sign in required"}
	}
	if state.ErrorMessage != "" {
		return trayPresentation{
			trayVisualAttention,
			"WireZTNA — Attention required",
			fmt.Sprintf("✕ %s", truncate(state.ErrorMessage, 40)),
		}
	}

	switch state.Status {
	case ipc.StatusConnected:
		if state.LastHandshake.IsZero() {
			return trayPresentation{trayVisualAttention, "WireZTNA — Checking tunnel health...", "◌ Connected — checking tunnel health"}
		}
		if state.LastHandshake.After(now) {
			return trayPresentation{trayVisualAttention, "WireZTNA — Tunnel health unavailable", "! Connected — invalid handshake time"}
		}
		if now.Sub(state.LastHandshake) > maxHealthyHandshakeAge {
			return trayPresentation{trayVisualAttention, "WireZTNA — Tunnel handshake stale", "! Connected — handshake stale"}
		}

		tooltip := "WireZTNA — Connected"
		statusText := "● Connected"
		if state.OverlayIP != "" {
			tooltip += fmt.Sprintf(" — %s", state.OverlayIP)
			statusText += fmt.Sprintf(" — %s", state.OverlayIP)
		}
		if state.IsExitNode && state.ExitNodeName != "" {
			tooltip += fmt.Sprintf(" (VPN: %s)", state.ExitNodeName)
			statusText += fmt.Sprintf("  [VPN: %s]", state.ExitNodeName)
		} else if state.GroupName != "" {
			statusText += fmt.Sprintf("  [%s]", state.GroupName)
		}
		return trayPresentation{trayVisualConnected, tooltip, statusText}

	case ipc.StatusDisconnected:
		return trayPresentation{trayVisualDisconnected, "WireZTNA — Disconnected", "○ Disconnected"}
	case ipc.StatusConnecting, ipc.StatusReconnecting:
		return trayPresentation{trayVisualAttention, "WireZTNA — Connecting...", "◌ Connecting..."}
	case trayStatusServiceUnavailable:
		return trayPresentation{trayVisualAttention, "WireZTNA — Service unavailable", "✕ Service not running"}
	default:
		return trayPresentation{trayVisualAttention, "WireZTNA — Attention required", "✕ Attention required"}
	}
}

func applyTrayPresentation(presentation trayPresentation) {
	switch presentation.visual {
	case trayVisualConnected:
		systray.SetIcon(icons.Connected)
	case trayVisualDisconnected:
		systray.SetIcon(icons.Disconnected)
	default:
		systray.SetIcon(icons.Attention)
	}
	systray.SetTooltip(presentation.tooltip)
}

func updateStatus(status string) {
	app.mu.Lock()
	app.status = status
	app.mu.Unlock()

	// A partial status update has no handshake evidence. In particular,
	// StatusConnected remains orange until the next full SendStatus response.
	applyTrayPresentation(classifyTrayPresentation(&ipc.State{Status: status}, time.Now()))
}

func updateUI(state *ipc.State) {
	presentation := classifyTrayPresentation(state, time.Now())
	applyTrayPresentation(presentation)

	switch state.Status {
	case ipc.StatusConnected:
		mStatus.SetTitle(presentation.statusText)

		mConnect.Hide()
		mDisconnect.Show()

		// Info items
		mTraffic.SetTitle(fmt.Sprintf("↑ %s   ↓ %s", formatBytes(state.TxBytes), formatBytes(state.RxBytes)))
		mTraffic.Show()

		if !state.ExpiresAt.IsZero() {
			remaining := time.Until(state.ExpiresAt)
			if remaining > 0 {
				mSession.SetTitle(fmt.Sprintf("Session: %s remaining", formatDuration(remaining)))
			} else {
				mSession.SetTitle("Session: expired")
			}
			mSession.Show()
		} else {
			mSession.Hide()
		}
		mInfo.Hide() // not used directly

		// Hide setup when connected
		mSetup.Hide()

	case ipc.StatusConnecting, ipc.StatusReconnecting:
		mStatus.SetTitle(presentation.statusText)
		mConnect.Hide()
		mDisconnect.Hide()
		mTraffic.Hide()
		mSession.Hide()
		mSetup.Hide()

	case trayStatusServiceUnavailable:
		mStatus.SetTitle(presentation.statusText)
		mConnect.Show()
		mDisconnect.Hide()
		mMode.Hide()
		mGroups.Hide()
		mTraffic.Hide()
		mSession.Hide()
		mSetup.Show()

	default: // Stable disconnected or an actionable error.
		mStatus.SetTitle(presentation.statusText)
		mConnect.Show()
		mDisconnect.Hide()
		mTraffic.Hide()
		mSession.Hide()
		mSetup.Show()
	}
}

func updateModeMenu(state *ipc.State) {
	if !state.VPNMode || len(state.ExitNodes) == 0 {
		mMode.Hide()
		return
	}

	app.mu.RLock()
	currentStatus := app.status
	isExit := app.isExitNode
	app.mu.RUnlock()

	if currentStatus != ipc.StatusConnected {
		mMode.Hide()
		return
	}

	mMode.Show()

	// Split tunnel label
	if !isExit {
		mSplitTunnel.SetTitle("● Split Tunnel (active)")
	} else {
		mSplitTunnel.SetTitle("○ Split Tunnel")
	}

	// Create exit node sub-items on demand
	for len(exitNodeItems) < len(state.ExitNodes) {
		item := mMode.AddSubMenuItem("", "")
		exitNodeItems = append(exitNodeItems, item)
		idx := len(exitNodeItems) - 1
		go func() {
			for range exitNodeItems[idx].ClickedCh {
				app.mu.RLock()
				if idx < len(app.exitNodes) {
					node := app.exitNodes[idx]
					app.mu.RUnlock()
					go func() {
						updateStatus(ipc.StatusReconnecting)
						showNotification("Switching Mode", fmt.Sprintf("Connecting via %s...", node.Name))
						err := ipcWithTimeout(func() error { return ipcClient.SendExitNode(node.ID) }, 35*time.Second)
						if err != nil {
							showNotification("Mode Switch Failed", err.Error())
						} else {
							showNotification("VPN Mode", fmt.Sprintf("Connected via %s", node.Name))
						}
						time.Sleep(500 * time.Millisecond)
						refreshState()
					}()
				} else {
					app.mu.RUnlock()
				}
			}
		}()
	}

	for i, item := range exitNodeItems {
		if i < len(state.ExitNodes) {
			node := state.ExitNodes[i]
			prefix := "○"
			if isExit && app.exitNodeName == node.Name {
				prefix = "●"
			}
			label := fmt.Sprintf("%s %s", prefix, node.Name)
			if node.Location != "" {
				label += fmt.Sprintf(" (%s)", node.Location)
			}
			if !node.Online {
				label += " — offline"
				item.Disable()
			} else {
				item.Enable()
			}
			item.SetTitle(label)
			item.Show()
		} else {
			item.Hide()
		}
	}
}

func updateGroupsMenu(state *ipc.State) {
	if len(state.Groups) <= 1 || !state.HasOverlap {
		mGroups.Hide()
		return
	}

	app.mu.RLock()
	currentStatus := app.status
	activeGroupID := app.groupID
	app.mu.RUnlock()

	if currentStatus != ipc.StatusConnected {
		mGroups.Hide()
		return
	}

	mGroups.Show()

	for len(groupItems) < len(state.Groups) {
		item := mGroups.AddSubMenuItem("", "")
		groupItems = append(groupItems, item)
		idx := len(groupItems) - 1
		go func() {
			for range groupItems[idx].ClickedCh {
				app.mu.RLock()
				if idx < len(app.groups) {
					g := app.groups[idx]
					app.mu.RUnlock()
					go func() {
						updateStatus(ipc.StatusReconnecting)
						showNotification("Switching Project", fmt.Sprintf("Changing to %s...", g.Name))
						err := ipcWithTimeout(func() error { return ipcClient.SendSwitch(g.ID) }, 35*time.Second)
						if err != nil {
							showNotification("Switch Failed", err.Error())
						} else {
							showNotification("Project Changed", fmt.Sprintf("Now connected to %s", g.Name))
						}
						time.Sleep(500 * time.Millisecond)
						refreshState()
					}()
				} else {
					app.mu.RUnlock()
				}
			}
		}()
	}

	for i, item := range groupItems {
		if i < len(state.Groups) {
			g := state.Groups[i]
			prefix := "○"
			if g.ID == activeGroupID {
				prefix = "●"
			}
			label := fmt.Sprintf("%s %s", prefix, g.Name)
			if len(g.CIDRs) > 0 {
				label += fmt.Sprintf(" (%s)", g.CIDRs[0])
			}
			item.SetTitle(label)
			item.Show()
		} else {
			item.Hide()
		}
	}
}

// ─── Helpers ───

// ipcWithTimeout executes an IPC call with a timeout to prevent hanging.
func ipcWithTimeout(fn func() error, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("operation timed out after %v", timeout)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// showNotification is implemented in platform-specific files:
// notify_windows.go (real toast) and notify_other.go (no-op).

// openWizard is implemented in platform-specific files:
// wizard_windows.go (webview2) and wizard_other.go (no-op).

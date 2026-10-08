package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wireztna/client/desktop/controller"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
)

type managedStartMsg struct {
	snapshot ManagedSnapshot
	err      error
}

type managedBackendUpdateMsg struct {
	update ManagedUpdate
	err    error
}

type managedOperationMsg struct {
	result ManagedOperationResult
	err    error
}

type managedRetryMsg struct{}

type managedOperationKind int

const (
	managedConnect managedOperationKind = iota
	managedSwitch
	managedDisconnect
)

func (m Model) managedStartCmd() tea.Cmd {
	return func() tea.Msg {
		if m.managedBackend == nil {
			return managedStartMsg{err: errors.New("managed backend is required")}
		}
		snapshot, err := m.managedBackend.Start(m.managedContext)
		return managedStartMsg{snapshot: snapshot, err: err}
	}
}

func (m Model) managedNextUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		update, err := m.managedBackend.NextUpdate(m.managedContext)
		return managedBackendUpdateMsg{update: update, err: err}
	}
}

func managedRetryCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return managedRetryMsg{} })
}

func (m Model) managedMutationCmd(kind managedOperationKind, selection ManagedSelection) tea.Cmd {
	generation := m.connectionGeneration
	return connectionMutationCmd(generation, func() tea.Msg {
		return m.runIPCMutation(generation, func() tea.Msg {
			var result ManagedOperationResult
			var err error
			switch kind {
			case managedConnect:
				result, err = m.managedBackend.Connect(m.managedContext, selection)
			case managedSwitch:
				result, err = m.managedBackend.Switch(m.managedContext, selection)
			case managedDisconnect:
				result, err = m.managedBackend.Disconnect(m.managedContext)
			}
			return managedOperationMsg{result: result, err: err}
		})
	})
}

func (m Model) managedLoginRequestOTPCmd(email string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.apiClient.OTPRequest(email)
		return loginOTPRequestMsg{err: err}
	}
}

func (m Model) managedLoginVerifyOTPCmd(email, code string) tea.Cmd {
	return func() tea.Msg {
		token, err := m.apiClient.OTPVerify(email, code)
		return loginOTPVerifyMsg{token: token, err: err}
	}
}

func (m Model) updateManaged(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.state == StateLoggingIn {
		return m.updateManagedLogin(message)
	}

	switch msg := message.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			if m.managedCancel != nil {
				m.managedCancel()
			}
			if m.managedBackend != nil {
				_ = m.managedBackend.Close()
			}
			return m, tea.Quit
		case "l":
			if m.state == StateAuthRequired || m.state == StateDisconnected || m.state == StateError {
				m.state = StateLoggingIn
				m.loginStep = loginStepEmail
				m.loginEmail = m.cfg.LoginIdentifier()
				m.loginOTP = ""
				m.loginError = ""
				return m, spinnerTickCmd()
			}
		case "c":
			if m.state == StateDisconnected || m.state == StateAuthRequired || m.state == StateError {
				if !m.managedReady {
					m.errorMsg = "Managed service unavailable; no direct fallback is permitted"
					return m, nil
				}
				selection, ok := m.currentManagedSelection()
				if !ok {
					m.errorMsg = "Select a concrete project before connecting; All groups is unavailable on managed macOS"
					m.tab = 1
					return m, nil
				}
				m.beginManagedExplicitConnection()
				m.state = StateConnecting
				m.managedOperationName = "Connecting"
				m.managedProgress = ""
				m.errorMsg = ""
				return m, tea.Batch(m.managedMutationCmd(managedConnect, selection), spinnerTickCmd())
			}
		case "d":
			if m.managedReady && (m.state == StateConnected || m.state == StateConnecting || m.state == StateReconnecting) {
				m.beginManagedExplicitConnection()
				m.state = StateReconnecting
				m.managedOperationName = "Disconnecting"
				m.managedProgress = ""
				m.errorMsg = ""
				return m, tea.Batch(m.managedMutationCmd(managedDisconnect, ManagedSelection{}), spinnerTickCmd())
			}
		case "tab":
			m.tab = (m.tab + 1) % 4
			return m, tea.ClearScreen
		case "up", "k":
			if m.tab == 1 {
				if m.cursorInExit {
					if m.cursor > 0 {
						m.cursor--
					} else {
						m.cursorInExit = false
						m.cursor = len(m.groups)
					}
				} else if m.cursor > 1 {
					m.cursor--
				}
			}
		case "down", "j":
			if m.tab == 1 {
				if m.cursorInExit {
					if m.cursor < len(m.exitNodes)-1 {
						m.cursor++
					}
				} else if m.cursor < len(m.groups) {
					m.cursor++
				} else if m.vpnMode && len(m.exitNodes) > 0 {
					m.cursorInExit = true
					m.cursor = 0
				}
			}
		case "enter":
			if m.tab == 1 {
				return m.selectManagedCursor()
			}
		case "0":
			if m.tab == 1 {
				m.errorMsg = "All groups is unavailable on managed macOS; select a concrete project"
				return m, nil
			}
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			if m.tab == 1 {
				index := int(msg.String()[0] - '0')
				if index <= len(m.groups) {
					m.cursor = index
					m.cursorInExit = false
					return m.selectManagedCursor()
				}
			}
		case "e":
			if m.tab == 1 && m.vpnMode && len(m.exitNodes) > 0 {
				index := m.selectedExitNode
				if index >= len(m.exitNodes) {
					index = 0
				}
				m.cursorInExit = true
				m.cursor = index
				return m.selectManagedCursor()
			}
		case "s":
			if m.tab == 1 && m.exitNodeID != "" {
				if !m.validManagedGroup(m.groupID) {
					m.errorMsg = "Select a concrete project before switching to split tunnel"
					return m, nil
				}
				m.selectedExitNode = 0
				m.exitNodeID = ""
				m.cursorInExit = false
				return m.startManagedSelection(ManagedSelection{GroupID: m.groupID})
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		if m.state == StateConnecting || m.state == StateReconnecting {
			m.spinnerIdx = (m.spinnerIdx + 1) % 8
		}
		return m, tickCmd()

	case spinnerTickMsg:
		if m.state == StateConnecting || m.state == StateReconnecting {
			m.spinnerIdx = (m.spinnerIdx + 1) % 8
			return m, spinnerTickCmd()
		}

	case managedRetryMsg:
		if !m.managedReady {
			return m, m.managedStartCmd()
		}

	case managedStartMsg:
		if msg.err != nil {
			m.managedReady = false
			m.applyManagedError(msg.err)
			return m, managedRetryCmd()
		}
		m.managedReady = true
		m.errorMsg = ""
		m.applyManagedSnapshot(msg.snapshot)
		return m, m.managedNextUpdateCmd()

	case managedBackendUpdateMsg:
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				return m, nil
			}
			m.managedReady = false
			m.applyManagedError(msg.err)
			return m, m.managedNextUpdateCmd()
		}
		if msg.update.Snapshot != nil {
			m.managedReady = true
			m.applyManagedSnapshot(*msg.update.Snapshot)
		}
		if msg.update.Event != nil {
			if msg.update.Event.Err != nil {
				m.managedReady = false
			}
			m.applyManagedEvent(*msg.update.Event)
		}
		return m, m.managedNextUpdateCmd()

	case connectionMutationMsg:
		if msg.generation != m.connectionGeneration || msg.result == nil {
			return m, nil
		}
		return m.updateManaged(msg.result)

	case managedOperationMsg:
		hasFinalSnapshot := msg.result.Snapshot.State != ""
		if hasFinalSnapshot {
			m.applyManagedSnapshot(msg.result.Snapshot)
		}
		m.managedOperationName = ""
		m.managedProgress = ""
		if msg.err != nil {
			if errors.Is(msg.err, ErrManagedAuthRequired) || errors.Is(msg.err, ErrManagedUpdateRequired) || !hasFinalSnapshot {
				m.applyManagedError(msg.err)
			} else {
				m.errorMsg = msg.err.Error()
			}
		} else {
			m.errorMsg = ""
		}
	}
	return m, nil
}

func (m Model) updateManagedLogin(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.state = StateAuthRequired
			m.loginEmail, m.loginOTP, m.loginError = "", "", ""
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
				return m, m.managedLoginRequestOTPCmd(m.loginEmail)
			}
			if m.loginOTP == "" {
				m.loginError = "Enter the 6-digit code"
				return m, nil
			}
			return m, m.managedLoginVerifyOTPCmd(m.loginEmail, m.loginOTP)
		case "backspace":
			if m.loginStep == loginStepEmail && len(m.loginEmail) > 0 {
				m.loginEmail = m.loginEmail[:len(m.loginEmail)-1]
			} else if m.loginStep == loginStepOTP && len(m.loginOTP) > 0 {
				m.loginOTP = m.loginOTP[:len(m.loginOTP)-1]
			}
		default:
			character := msg.String()
			if len(character) == 1 && character[0] >= 32 && character[0] <= 126 {
				if m.loginStep == loginStepEmail {
					m.loginEmail += character
				} else if character[0] >= '0' && character[0] <= '9' && len(m.loginOTP) < 6 {
					m.loginOTP += character
				}
			}
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case spinnerTickMsg:
		m.spinnerIdx = (m.spinnerIdx + 1) % 8
		return m, spinnerTickCmd()
	case tickMsg:
		m.spinnerIdx = (m.spinnerIdx + 1) % 8
		return m, tickCmd()
	case loginOTPRequestMsg:
		if msg.err != nil {
			m.loginError = msg.err.Error()
		} else {
			m.loginStep = loginStepOTP
			m.loginOTP, m.loginError = "", ""
		}
	case loginOTPVerifyMsg:
		if msg.err != nil {
			m.loginError = msg.err.Error()
			return m, nil
		}
		if err := config.SaveToken(msg.token); err != nil {
			m.loginError = err.Error()
			return m, nil
		}
		m.apiClient.SetToken(msg.token)
		m.loginEmail, m.loginOTP, m.loginError = "", "", ""
		m.state = StateConnecting
		m.managedOperationName = "Refreshing service state"
		return m, m.managedStartCmd()
	}
	return m, nil
}

func (m *Model) selectManagedCursor() (tea.Model, tea.Cmd) {
	if m.cursorInExit {
		if m.cursor < 0 || m.cursor >= len(m.exitNodes) {
			return *m, nil
		}
		node := m.exitNodes[m.cursor]
		if node.Status != "online" {
			m.errorMsg = fmt.Sprintf("'%s' is not online", node.Name)
			return *m, nil
		}
		if !m.validManagedGroup(m.groupID) {
			m.errorMsg = "Select a concrete project before choosing an exit node"
			return *m, nil
		}
		m.selectedExitNode = m.cursor + 1
		m.exitNodeID = node.ID
		return m.startManagedSelection(ManagedSelection{GroupID: m.groupID, ExitNodeID: node.ID})
	}
	if m.cursor < 1 || m.cursor > len(m.groups) {
		m.errorMsg = "All groups is unavailable on managed macOS; select a concrete project"
		return *m, nil
	}
	group := m.groups[m.cursor-1]
	if group.OnlinePublishers == 0 {
		m.errorMsg = fmt.Sprintf("'%s' has no resources online", group.Name)
		return *m, nil
	}
	m.selectedGroup = m.cursor
	m.groupID = group.ID
	m.groupName = group.Name
	m.refreshManagedExitNodes(group.ID)
	m.selectedExitNode = 0
	m.exitNodeID = ""
	m.cursorInExit = false
	if m.state == StateConnected || m.state == StateConnecting || m.state == StateReconnecting {
		return m.startManagedSelection(ManagedSelection{GroupID: group.ID})
	}
	m.errorMsg = ""
	return *m, nil
}

func (m *Model) startManagedSelection(selection ManagedSelection) (tea.Model, tea.Cmd) {
	if !m.managedReady {
		m.errorMsg = "Managed service unavailable; no direct fallback is permitted"
		return *m, nil
	}
	if !m.validManagedGroup(selection.GroupID) {
		m.errorMsg = "A concrete project from the service catalog is required"
		return *m, nil
	}
	if selection.ExitNodeID != "" && !m.validManagedExitNode(selection.GroupID, selection.ExitNodeID) {
		m.errorMsg = "The exit node is offline or unavailable to the selected project"
		return *m, nil
	}
	kind := managedConnect
	if m.state == StateConnected || m.state == StateConnecting || m.state == StateReconnecting {
		kind = managedSwitch
	}
	m.beginManagedExplicitConnection()
	m.state = StateReconnecting
	m.managedOperationName = "Switching connection"
	m.managedProgress = ""
	m.errorMsg = ""
	return *m, tea.Batch(m.managedMutationCmd(kind, selection), spinnerTickCmd())
}

func (m *Model) beginManagedExplicitConnection() {
	m.advanceConnectionGeneration()
	m.userDisconnected = false
}

func (m Model) currentManagedSelection() (ManagedSelection, bool) {
	if !m.validManagedGroup(m.groupID) {
		return ManagedSelection{}, false
	}
	if m.exitNodeID != "" && !m.validManagedExitNode(m.groupID, m.exitNodeID) {
		return ManagedSelection{}, false
	}
	return ManagedSelection{GroupID: m.groupID, ExitNodeID: m.exitNodeID}, true
}

func (m Model) validManagedGroup(groupID string) bool {
	if groupID == "" {
		return false
	}
	for _, group := range m.groups {
		if group.ID == groupID {
			return true
		}
	}
	return false
}

func (m Model) validManagedExitNode(groupID, exitNodeID string) bool {
	if groupID == "" || exitNodeID == "" {
		return false
	}
	publisherAllowed := false
	for _, group := range m.groups {
		if group.ID != groupID {
			continue
		}
		for _, publisher := range group.Publishers {
			if publisher.ID == exitNodeID {
				publisherAllowed = true
				break
			}
		}
		break
	}
	if !publisherAllowed {
		return false
	}
	for _, exitNode := range m.managedExitNodes {
		if exitNode.ID == exitNodeID && exitNode.Status == "online" {
			return true
		}
	}
	return false
}

func (m *Model) refreshManagedExitNodes(groupID string) {
	m.exitNodes = m.exitNodes[:0]
	for _, exitNode := range m.managedExitNodes {
		if m.validManagedExitNode(groupID, exitNode.ID) {
			m.exitNodes = append(m.exitNodes, exitNode)
		}
	}
}

func (m *Model) applyManagedSnapshot(snapshot ManagedSnapshot) {
	if snapshot.StreamID != "" && (snapshot.StreamID != m.managedStreamID || snapshot.Epoch != m.managedEpoch) {
		m.managedStreamID = snapshot.StreamID
		m.managedEpoch = snapshot.Epoch
		m.managedSequence = 0
	} else if snapshot.Sequence < m.managedSequence {
		return
	}
	m.managedSequence = snapshot.Sequence
	m.managedHealth = snapshot.Health
	m.groups = make([]api.GroupInfo, 0, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		group := api.GroupInfo{
			ID: project.ID, Name: project.Name, Description: project.Description,
			CIDRs: append([]string(nil), project.CIDRs...), OnlinePublishers: project.OnlineResources,
		}
		for _, resource := range project.Resources {
			group.Publishers = append(group.Publishers, api.PublisherInfo{
				ID: resource.ID, Name: resource.Name, Status: resource.Status,
				ExposedCIDRs: append([]string(nil), resource.ExposedCIDRs...),
			})
		}
		m.groups = append(m.groups, group)
	}
	m.managedExitNodes = make([]api.ExitNodeInfo, 0, len(snapshot.ExitNodes))
	for _, node := range snapshot.ExitNodes {
		if node.Status == "online" {
			m.managedExitNodes = append(m.managedExitNodes, api.ExitNodeInfo{ID: node.ID, Name: node.Name, Location: node.Location, Status: node.Status})
		}
	}
	m.hasOverlap = snapshot.HasOverlap
	m.vpnMode = snapshot.VPNMode

	selection := ManagedSelection{}
	if snapshot.AppliedConnected {
		selection = snapshot.Applied
	} else if snapshot.DesiredConnected {
		selection = snapshot.Desired
	} else if m.validManagedGroup(m.groupID) {
		selection = ManagedSelection{GroupID: m.groupID, ExitNodeID: m.exitNodeID}
	}
	if selection.GroupID != "" && m.validManagedGroup(selection.GroupID) {
		m.groupID = selection.GroupID
		if selection.ExitNodeID != "" && !m.validManagedExitNode(selection.GroupID, selection.ExitNodeID) {
			selection.ExitNodeID = ""
		}
		m.refreshManagedExitNodes(selection.GroupID)
		m.exitNodeID = selection.ExitNodeID
		m.isExitNode = selection.ExitNodeID != ""
		m.groupName = ""
		m.selectedGroup = 0
		for index, group := range m.groups {
			if group.ID == selection.GroupID {
				m.groupName = group.Name
				m.selectedGroup = index + 1
				break
			}
		}
		m.selectedExitNode = 0
		m.exitNodeName = ""
		for index, node := range m.exitNodes {
			if node.ID == selection.ExitNodeID {
				m.selectedExitNode = index + 1
				m.exitNodeName = node.Name
				break
			}
		}
	} else {
		m.groupID = ""
		m.groupName = ""
		m.selectedGroup = 0
		m.exitNodeID = ""
		m.exitNodeName = ""
		m.selectedExitNode = 0
		m.isExitNode = false
		m.refreshManagedExitNodes("")
	}
	if m.cursor == 0 && len(m.groups) > 0 {
		m.cursor = 1
	}
	m.state = managedTUIState(snapshot.State)
	switch snapshot.State {
	case controller.ConnectionStateDegraded:
		m.errorMsg = managedHealthDetail(snapshot.Health)
	case controller.ConnectionStateAuthRequired:
		m.lastRenewError = "The managed service requires authentication"
		m.errorMsg = m.lastRenewError
	case controller.ConnectionStateUpdateRequired:
		m.errorMsg = "The managed service requires a compatible client update"
	default:
		m.errorMsg = ""
	}
}

func (m *Model) applyManagedEvent(event ManagedEvent) {
	if event.Err != nil {
		if errors.Is(event.Err, ErrManagedAuthRequired) || errors.Is(event.Err, ErrManagedUpdateRequired) {
			m.applyManagedError(event.Err)
		} else {
			m.errorMsg = event.Err.Error()
			if event.State != "" {
				m.state = managedTUIState(event.State)
			} else {
				m.state = StateError
			}
		}
		return
	}
	if event.StreamID != "" && (event.StreamID != m.managedStreamID || event.Epoch != m.managedEpoch) {
		m.managedStreamID = event.StreamID
		m.managedEpoch = event.Epoch
		m.managedSequence = 0
	} else if event.Sequence <= m.managedSequence {
		return
	}
	m.managedSequence = event.Sequence
	if event.Progress != "" {
		m.managedProgress = event.Progress
	}
}

func (m *Model) applyManagedError(err error) {
	m.errorMsg = err.Error()
	switch {
	case errors.Is(err, ErrManagedAuthRequired):
		m.state = StateAuthRequired
		m.lastRenewError = err.Error()
	case errors.Is(err, ErrManagedUpdateRequired):
		m.state = StateError
	default:
		m.state = StateError
	}
}

func managedTUIState(state controller.ConnectionState) State {
	switch state {
	case controller.ConnectionStateDisconnected:
		return StateDisconnected
	case controller.ConnectionStateConnecting:
		return StateConnecting
	case controller.ConnectionStateConnected:
		return StateConnected
	case controller.ConnectionStateReconnecting:
		return StateReconnecting
	case controller.ConnectionStateAuthRequired:
		return StateAuthRequired
	default:
		return StateError
	}
}

func managedHealthDetail(health controller.Health) string {
	return fmt.Sprintf("Managed connection degraded (WireGuard: %s, routes: %s, DNS: %s, end-to-end: %s)",
		health.WireGuard, health.Routes, health.DNS, health.EndToEnd)
}

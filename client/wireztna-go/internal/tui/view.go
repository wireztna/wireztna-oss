package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/wireztna/client/pkg/version"
)

// ─── View ───

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	w := m.width
	if w < 40 {
		w = 80
	}
	h := m.height
	if h < 10 {
		h = 24
	}
	contentWidth := w - 2

	var sections []string

	// 1. Header: brand + status crumb bar
	sections = append(sections, m.renderHeader(contentWidth))

	// 2. Full-width separator
	sections = append(sections, m.hLine(contentWidth))

	// 3. Tab bar
	sections = append(sections, m.renderTabBar())

	// 4. Main content
	var content string
	switch m.tab {
	case 0:
		content = m.renderStatusTab(contentWidth)
	case 1:
		content = m.renderGroupsTab(contentWidth)
	case 2:
		content = m.renderNetworkTab(contentWidth)
	case 3:
		content = m.renderLogsTab(contentWidth)
	}
	sections = append(sections, content)

	// 5. Error banner
	if m.errorMsg != "" {
		sections = append(sections, sError.Render(" ⚠ "+m.errorMsg))
	}

	// 6. Footer separator + help + version
	sections = append(sections, m.hLine(contentWidth))
	sections = append(sections, m.renderFooter(contentWidth))

	body := lipgloss.JoinVertical(lipgloss.Left, sections...)

	// Pad to full terminal height to prevent leftover artifacts
	bodyLines := strings.Count(body, "\n") + 1
	if bodyLines < h {
		body += strings.Repeat("\n", h-bodyLines)
	}

	return body
}

// ─── Header ───

func (m Model) renderHeader(width int) string {
	// Left: brand
	brand := sHeader.Render("⬡ WireZTNA")

	// Right: compact status crumb
	crumb := m.renderCrumbBar()

	gap := width - lipgloss.Width(brand) - lipgloss.Width(crumb)
	if gap < 1 {
		gap = 1
	}

	return brand + strings.Repeat(" ", gap) + crumb
}

// renderCrumbBar shows a compact one-line status summary
func (m Model) renderCrumbBar() string {
	sep := sCrumbSep.Render(" │ ")
	var parts []string

	spinnerFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧"}

	// Status
	switch m.state {
	case StateConnected:
		parts = append(parts, sStatusOn.Render("● Connected"))
	case StateDisconnected:
		parts = append(parts, sStatusOff.Render("○ Disconnected"))
	case StateConnecting:
		frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
		parts = append(parts, sStatusInfo.Render(frame+" Connecting"))
	case StateReconnecting:
		frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
		label := "Renewing"
		if m.managed && m.managedOperationName != "" {
			label = m.managedOperationName
		}
		parts = append(parts, sStatusInfo.Render(frame+" "+label))
	case StateError:
		parts = append(parts, sStatusOff.Render("✗ Error"))
	case StateAuthRequired:
		parts = append(parts, sStatusWarn.Render("⚠ Auth Expired"))
	case StateLoggingIn:
		frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
		parts = append(parts, sStatusInfo.Render(frame+" Logging in"))
	}

	// Overlay IP
	if m.overlayIP != "" {
		parts = append(parts, sCrumbVal.Render(m.overlayIP))
	}

	// Project
	if m.groupName != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(cPrimary).Render(m.groupName))
	}

	// Mode
	if m.exitNodeID != "" {
		parts = append(parts, sStatusOn.Render("VPN"))
	} else if m.state == StateConnected {
		parts = append(parts, lipgloss.NewStyle().Foreground(cFgDim).Render("Split"))
	}

	return strings.Join(parts, sep)
}

// ─── Tab Bar ───

func (m Model) renderTabBar() string {
	tabs := []string{"Status", "Projects", "Network", "Logs"}
	var rendered []string

	for i, name := range tabs {
		if i == m.tab {
			rendered = append(rendered, sTabActive.Render(name))
		} else {
			rendered = append(rendered, sTabInactive.Render(name))
		}
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

// ─── Status Tab ───

func (m Model) renderStatusTab(width int) string {
	if m.managed {
		return m.renderManagedStatusTab(width)
	}
	var rows []string
	rows = append(rows, "")

	// ─── Login flow ───
	if m.state == StateLoggingIn {
		rows = append(rows, sPanelTitle.Render(" LOGIN"))
		rows = append(rows, m.hLine(width))
		rows = append(rows, "")

		if m.loginStep == loginStepEmail {
			rows = append(rows, "  "+sCrumb.Render("Enter your email (press Enter to use default):"))
			rows = append(rows, "")
			cursor := "█"
			rows = append(rows, "  Email: "+sStatusInfo.Render(m.loginEmail)+cursor)
		} else {
			rows = append(rows, "  "+sCrumb.Render("A code was sent to: ")+sStatusInfo.Render(m.loginEmail))
			rows = append(rows, "")
			cursor := "█"
			display := m.loginOTP
			for len(display) < 6 {
				display += "·"
			}
			rows = append(rows, "  Code:  "+sStatusInfo.Render(m.loginOTP)+cursor)
		}
		rows = append(rows, "")
		if m.loginError != "" {
			rows = append(rows, "  "+sStatusWarn.Render("✗ "+m.loginError))
			rows = append(rows, "")
		}
		rows = append(rows, "  "+lipgloss.NewStyle().Foreground(cFgDim).Render("Press Enter to submit · Esc to cancel"))
		rows = append(rows, "")
		return strings.Join(rows, "\n")
	}

	// ─── Auth required state ───
	if m.state == StateAuthRequired {
		rows = append(rows, sPanelTitle.Render(" SESSION EXPIRED"))
		rows = append(rows, m.hLine(width))
		rows = append(rows, "")
		rows = append(rows, "  "+sStatusWarn.Render("⚠ Authentication required"))
		rows = append(rows, "")
		if m.lastRenewError != "" {
			rows = append(rows, "  "+lipgloss.NewStyle().Foreground(cFgDim).Render("Reason: "+m.lastRenewError))
			rows = append(rows, "")
		}
		rows = append(rows, "  "+sCrumb.Render("Press 'l' to login · 'c' to reconnect · 'q' to quit"))
		rows = append(rows, "")
		return strings.Join(rows, "\n")
	}

	if m.state == StateDisconnected && m.overlayIP == "" {
		rows = append(rows, sCrumb.Render("  Not connected. Press 'c' to connect."))
		rows = append(rows, "")
		return strings.Join(rows, "\n")
	}

	// Show spinner prominently when connecting
	if m.state == StateConnecting || m.state == StateReconnecting {
		spinnerFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧"}
		frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
		label := "Connecting"
		if m.state == StateReconnecting {
			label = "Renewing session"
		}
		rows = append(rows, "")
		rows = append(rows, "  "+sStatusInfo.Render(frame+" "+label+"..."))
		rows = append(rows, "")
	}

	// CONNECTION section
	rows = append(rows, sPanelTitle.Render(" CONNECTION"))
	rows = append(rows, m.hLine(width))

	iface := m.cfg.Interface
	if iface == "" {
		iface = "wg-wireztna"
	}

	// Table with │ separators
	rows = append(rows, m.tableRow(width,
		[]string{"Interface", "Overlay IP", "Endpoint"},
		[]string{iface, m.overlayIP, m.endpoint},
	))

	// Handshake + Transfer row
	handshake := "never"
	if !m.lastHandshake.IsZero() {
		age := time.Since(m.lastHandshake).Round(time.Second)
		if age > 180*time.Second {
			handshake = sStatusWarn.Render(fmt.Sprintf("%s ⚠", age))
		} else {
			handshake = sStatusOn.Render(fmt.Sprintf("%s ✓", age))
		}
	}

	transfer := fmt.Sprintf("↓ %s  ↑ %s", formatBytes(m.rxBytes), formatBytes(m.txBytes))

	rows = append(rows, m.tableRow(width,
		[]string{"Handshake", "Transfer", "Traffic"},
		[]string{handshake, transfer, m.renderSparkline()},
	))

	rows = append(rows, "")

	// SESSION section
	if m.sessionID != "" || m.ttlSeconds > 0 {
		rows = append(rows, sPanelTitle.Render(" SESSION"))
		rows = append(rows, m.hLine(width))

		sessionID := "—"
		if m.sessionID != "" {
			sessionID = m.sessionID
			if len(sessionID) > 8 {
				sessionID = sessionID[:8] + "…"
			}
		}

		expires := "—"
		if !m.expiresAt.IsZero() {
			remaining := time.Until(m.expiresAt)
			if remaining < 0 {
				remaining = 0
			}
			remainStr := formatDuration(remaining)
			if remaining < 30*time.Minute {
				remainStr = sStatusWarn.Render(remainStr)
			}
			expires = m.expiresAt.Local().Format("15:04:05") + " (" + remainStr + " left)"
		}

		project := m.groupName
		if project == "" {
			project = "—"
		} else {
			project = sSelected.Render(project)
		}

		rows = append(rows, m.tableRow(width,
			[]string{"Session", "Project", "Expires"},
			[]string{sessionID, project, expires},
		))

		// Mode row
		mode := "Split tunnel (ZTNA)"
		if m.exitNodeID != "" || m.selectedExitNode > 0 {
			mode = "VPN (exit node)"
			if m.selectedExitNode > 0 && m.selectedExitNode <= len(m.exitNodes) {
				node := m.exitNodes[m.selectedExitNode-1]
				loc := node.Location
				if loc == "" {
					loc = node.Name
				}
				mode = sStatusOn.Render(fmt.Sprintf("VPN — full tunnel via %s", loc))
			}
		}
		rows = append(rows, m.fieldRow("Mode", mode))

		// TTL Progress bar
		if !m.expiresAt.IsZero() && m.ttlSeconds > 0 {
			rows = append(rows, m.fieldRow("TTL", m.renderTTLBar()))
		}

		// Latency
		if m.state == StateConnected {
			rows = append(rows, m.fieldRow("Latency", m.renderLatency()))
		}

		// Ping results (visual display)
		if m.pingRunning {
			spinnerFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧"}
			frame := spinnerFrames[m.pingIdx%len(spinnerFrames)]
			rows = append(rows, m.fieldRow("Ping", sStatusInfo.Render(frame+" testing connectivity...")))
		} else if len(m.pingResults) > 0 {
			rows = append(rows, "")
			rows = append(rows, sPanelTitle.Render(" PING TEST"))
			rows = append(rows, m.hLine(width))
			for _, t := range m.pingResults {
				var status string
				if t.ok {
					if t.latency <= 0 {
						status = sStatusOn.Render("reachable ●")
					} else if t.latency < 50 {
						status = sStatusOn.Render(fmt.Sprintf("%dms ●", t.latency))
					} else if t.latency < 150 {
						status = sStatusWarn.Render(fmt.Sprintf("%dms ◐", t.latency))
					} else {
						status = sStatusOff.Render(fmt.Sprintf("%dms ○", t.latency))
					}
				} else {
					status = sStatusOff.Render("timeout ✗")
				}
				ipStr := lipgloss.NewStyle().Foreground(cInfo).Render(t.ip)
				nameStr := lipgloss.NewStyle().Bold(true).Foreground(cFg).Render(t.name)
				rows = append(rows, fmt.Sprintf("  %s  %s  %s", nameStr, ipStr, status))
			}
		}

		rows = append(rows, "")
	}

	return strings.Join(rows, "\n")
}

// ─── Groups/Projects Tab ───

func (m Model) renderGroupsTab(width int) string {
	var rows []string
	rows = append(rows, "")

	if len(m.groups) == 0 {
		rows = append(rows, sCrumb.Render("  No groups available"))
		return strings.Join(rows, "\n")
	}

	if m.hasOverlap {
		rows = append(rows, " "+sStatusWarn.Render("⚠ CIDR overlap detected")+" — select a project:")
	} else {
		rows = append(rows, " "+sCrumb.Render("Select a project to connect:"))
	}
	rows = append(rows, "")

	// Table header
	colW := m.groupColWidths(width)
	header := fmt.Sprintf(" %s│%s│%s│%s",
		sTableHeader.Width(colW[0]).Render("#"),
		sTableHeader.Width(colW[1]).Render("PROJECT"),
		sTableHeader.Width(colW[2]).Render("CIDRs"),
		sTableHeader.Width(colW[3]).Render("PUBLISHERS"),
	)
	rows = append(rows, header)
	rows = append(rows, m.hLine(width))

	// Option 0: All groups is a legacy/direct selection. IPC v2 requires a
	// concrete GroupID for every managed selection, including exit nodes.
	if !m.managed {
		marker := "  "
		if !m.cursorInExit && m.cursor == 0 {
			marker = sSelected.Render("▸ ")
		}
		name := "All groups"
		if m.selectedGroup == 0 && m.groupName == "All groups" {
			name = sStatusOn.Render("All groups ●")
		}
		row := fmt.Sprintf(" %s%s│%s│%s│%s",
			marker,
			sTableCell.Width(colW[0]-2).Render("0"),
			sTableCell.Width(colW[1]).Render(name),
			sTableCellDim.Width(colW[2]).Render("*"),
			sTableCellDim.Width(colW[3]).Render("—"),
		)
		rows = append(rows, row)
	}

	// Group rows
	for i, g := range m.groups {
		marker := "  "
		if !m.cursorInExit && m.cursor == i+1 {
			marker = sSelected.Render("▸ ")
		}

		name := g.Name
		if m.groupName == g.Name {
			name = sStatusOn.Render(g.Name + " ●")
		}

		cidrs := "—"
		if len(g.CIDRs) > 0 {
			cidrs = strings.Join(g.CIDRs, ", ")
			maxCidr := colW[2] - 2
			if maxCidr < 5 {
				maxCidr = 12
			}
			if len(cidrs) > maxCidr {
				cidrs = cidrs[:maxCidr-1] + "…"
			}
		}

		pubStatus := fmt.Sprintf("%d/%d ●", g.OnlinePublishers, len(g.Publishers))
		if g.OnlinePublishers == 0 {
			name = sListItemDim.Render(g.Name)
			pubStatus = sStatusOff.Render("0/" + fmt.Sprintf("%d", len(g.Publishers)))
		}

		idx := fmt.Sprintf("%d", i+1)
		row := fmt.Sprintf(" %s%s│%s│%s│%s",
			marker,
			sTableCell.Width(colW[0]-2).Render(idx),
			sTableCell.Width(colW[1]).Render(name),
			sTableCellDim.Width(colW[2]).Render(cidrs),
			sTableCellDim.Width(colW[3]).Render(pubStatus),
		)
		rows = append(rows, row)
	}

	// Exit nodes
	if m.vpnMode && len(m.exitNodes) > 0 {
		rows = append(rows, "")
		rows = append(rows, " "+sPanelTitle.Render("VPN EXIT NODES"))
		rows = append(rows, m.hLine(width))

		for i, node := range m.exitNodes {
			marker := "  "
			if m.cursorInExit && m.cursor == i {
				marker = sSelected.Render("▸ ")
			}

			location := node.Location
			if location == "" {
				location = "unknown"
			}

			statusIcon := sStatusOn.Render("●")
			if node.Status != "online" {
				statusIcon = sStatusOff.Render("○")
			}

			name := fmt.Sprintf("%s (%s) %s", node.Name, location, statusIcon)
			if m.exitNodeID == node.ID {
				name = sStatusOn.Render(node.Name + " (" + location + ") ● active")
			}

			row := fmt.Sprintf(" %s%s│%s",
				marker,
				sTableCell.Width(4).Render(fmt.Sprintf("e%d", i+1)),
				sTableCell.Render(name),
			)
			rows = append(rows, row)
		}
	}

	return strings.Join(rows, "\n")
}

// ─── Debug Tab ───

// ─── Network Tab ───

func (m Model) renderNetworkTab(width int) string {
	if m.managed {
		return m.renderManagedNetworkTab(width)
	}
	var rows []string
	rows = append(rows, "")

	if m.debugInfo == nil {
		rows = append(rows, sCrumb.Render("  Loading network info..."))
		return strings.Join(rows, "\n")
	}

	// Publisher section
	if m.publisherName != "" || m.publisherEndpoint != "" {
		rows = append(rows, " "+sPanelTitle.Render("PUBLISHER"))
		rows = append(rows, m.hLine(width))
		if m.publisherName != "" {
			rows = append(rows, m.fieldRow("Name", sStatusOn.Render(m.publisherName)))
		}
		rows = append(rows, m.fieldRow("Broker", lipgloss.NewStyle().Bold(true).Foreground(cFg).Render(m.publisherEndpoint)))
		if m.groupName != "" {
			rows = append(rows, m.fieldRow("Project", lipgloss.NewStyle().Bold(true).Foreground(cPrimary).Render(m.groupName)))
		}
		for _, g := range m.groups {
			if g.Name == m.groupName || (m.groupID != "" && g.ID == m.groupID) {
				if len(g.CIDRs) > 0 {
					rows = append(rows, m.fieldRow("CIDRs", lipgloss.NewStyle().Bold(true).Foreground(cSecondary).Render(strings.Join(g.CIDRs, ", "))))
				}
				for _, p := range g.Publishers {
					icon := sStatusOn.Render("●")
					if p.Status != "online" {
						icon = sStatusOff.Render("○")
					}
					pName := lipgloss.NewStyle().Bold(true).Foreground(cFg).Render(p.Name)
					cidrs := ""
					if len(p.ExposedCIDRs) > 0 {
						cidrs = lipgloss.NewStyle().Foreground(cFgDim).Render(" → ") +
							lipgloss.NewStyle().Foreground(cInfo).Render(strings.Join(p.ExposedCIDRs, ", "))
					}
					rows = append(rows, m.fieldRow("", fmt.Sprintf("%s %s%s", icon, pName, cidrs)))
				}
				break
			}
		}
		rows = append(rows, "")
	}

	// WireGuard interface
	rows = append(rows, " "+sPanelTitle.Render("WIREGUARD INTERFACE"))
	rows = append(rows, m.hLine(width))
	for _, line := range strings.Split(m.debugInfo.WireGuard, "\n") {
		if line == "" {
			continue
		}
		rows = append(rows, " "+m.colorizeDebugLine(line))
	}
	rows = append(rows, "")

	// Routes
	rows = append(rows, " "+sPanelTitle.Render("ROUTES"))
	rows = append(rows, m.hLine(width))
	for _, line := range strings.Split(m.debugInfo.Routes, "\n") {
		if line == "" {
			continue
		}
		rows = append(rows, " "+m.colorizeDebugLine(line))
	}
	rows = append(rows, "")

	// DNS
	rows = append(rows, " "+sPanelTitle.Render("SPLIT DNS"))
	rows = append(rows, m.hLine(width))
	for _, line := range strings.Split(m.debugInfo.DNS, "\n") {
		if line == "" {
			continue
		}
		rows = append(rows, " "+m.colorizeDebugLine(line))
	}

	return strings.Join(rows, "\n")
}

// ─── Logs Tab ───

func (m Model) renderLogsTab(width int) string {
	var rows []string
	rows = append(rows, "")

	// Filter bar
	filters := []string{"ALL", "INFO+", "WARN+", "ERROR"}
	var filterParts []string
	for i, f := range filters {
		if i == m.debugFilter {
			filterParts = append(filterParts, sTabActive.Render(f))
		} else {
			filterParts = append(filterParts, lipgloss.NewStyle().Foreground(cFgDim).Render(f))
		}
	}
	filterBar := " Filter: " + strings.Join(filterParts, " ")

	// Tail indicator
	tailIndicator := ""
	if m.debugAutoTail {
		tailIndicator = sStatusOn.Render(" ● LIVE")
	} else {
		tailIndicator = lipgloss.NewStyle().Foreground(cFgDim).Render(" ○ PAUSED")
	}

	// Entry count
	var totalEntries int
	if m.debugLogger != nil {
		totalEntries = m.debugLogger.Count()
	}
	countStr := lipgloss.NewStyle().Foreground(cFgDim).Render(fmt.Sprintf(" (%d entries)", totalEntries))

	rows = append(rows, filterBar+tailIndicator+countStr)
	rows = append(rows, m.hLine(width))

	// Get filtered entries
	if m.debugLogger == nil {
		rows = append(rows, sCrumb.Render("  No log entries yet. Events will appear here."))
		return strings.Join(rows, "\n")
	}

	minLevel := []string{"debug", "info", "warn", "error"}[m.debugFilter]
	entries := m.debugLogger.EntriesFiltered(minLevel)

	if len(entries) == 0 {
		rows = append(rows, sCrumb.Render("  No entries matching filter."))
		return strings.Join(rows, "\n")
	}

	// Calculate visible lines (terminal height - header/footer/filter)
	visibleLines := m.height - 8
	if visibleLines < 5 {
		visibleLines = 15
	}

	// Apply scroll (from bottom)
	scrollOffset := m.debugScroll
	if scrollOffset > len(entries)-visibleLines {
		scrollOffset = len(entries) - visibleLines
	}
	if scrollOffset < 0 {
		scrollOffset = 0
	}

	// Slice the visible window
	endIdx := len(entries) - scrollOffset
	startIdx := endIdx - visibleLines
	if startIdx < 0 {
		startIdx = 0
	}

	visibleEntries := entries[startIdx:endIdx]

	// Render entries
	for _, entry := range visibleEntries {
		ts := lipgloss.NewStyle().Foreground(cFgDim).Render(entry.Time.Format("15:04:05"))

		var levelBadge string
		var msg string
		switch entry.Level {
		case "error":
			levelBadge = lipgloss.NewStyle().Bold(true).Foreground(cDanger).Render("ERR")
			msg = lipgloss.NewStyle().Foreground(cDanger).Render(entry.Message)
		case "warn":
			levelBadge = lipgloss.NewStyle().Bold(true).Foreground(cWarning).Render("WRN")
			msg = lipgloss.NewStyle().Foreground(cWarning).Render(entry.Message)
		case "info":
			levelBadge = lipgloss.NewStyle().Bold(true).Foreground(cSuccess).Render("INF")
			msg = lipgloss.NewStyle().Foreground(cFg).Render(entry.Message)
		case "debug":
			levelBadge = lipgloss.NewStyle().Foreground(cFgDim).Render("DBG")
			msg = lipgloss.NewStyle().Foreground(cFgDim).Render(entry.Message)
		default:
			levelBadge = lipgloss.NewStyle().Foreground(cFgDim).Render("---")
			msg = lipgloss.NewStyle().Foreground(cFgDim).Render(entry.Message)
		}

		rows = append(rows, fmt.Sprintf(" %s %s %s", ts, levelBadge, msg))
	}

	// Scroll position indicator
	if len(entries) > visibleLines {
		pos := len(entries) - scrollOffset
		pct := pos * 100 / len(entries)
		scrollInfo := lipgloss.NewStyle().Foreground(cFgDim).Render(
			fmt.Sprintf(" ─── %d/%d (%d%%) ───", pos, len(entries), pct))
		rows = append(rows, scrollInfo)
	}

	return strings.Join(rows, "\n")
}

// colorizeDebugLine adds syntax highlighting to debug output lines
func (m Model) colorizeDebugLine(line string) string {
	boldWhite := lipgloss.NewStyle().Bold(true).Foreground(cFg)
	dimStyle := lipgloss.NewStyle().Foreground(cFgDim)
	valStyle := lipgloss.NewStyle().Foreground(cSecondary)
	ipStyle := lipgloss.NewStyle().Bold(true).Foreground(cInfo)

	// Key: value pattern (e.g., "  interface: wg-wireztna")
	if idx := strings.Index(line, ": "); idx > 0 {
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+2:])

		// Highlight IPs and CIDR notation
		if isIPLike(val) {
			return "  " + dimStyle.Render(key+":") + " " + ipStyle.Render(val)
		}
		// Highlight interface names
		if strings.HasPrefix(val, "wg-") || strings.HasPrefix(val, "utun") {
			return "  " + dimStyle.Render(key+":") + " " + boldWhite.Render(val)
		}
		return "  " + dimStyle.Render(key+":") + " " + valStyle.Render(val)
	}

	// Lines with = (e.g., "AllowedIPs = 10.50.0.0/16")
	if idx := strings.Index(line, " = "); idx > 0 {
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+3:])
		if isIPLike(val) {
			return "  " + dimStyle.Render(key+" =") + " " + ipStyle.Render(val)
		}
		return "  " + dimStyle.Render(key+" =") + " " + valStyle.Render(val)
	}

	// Section-like lines (all caps or starts with keyword)
	trimmed := strings.TrimSpace(line)
	if trimmed == strings.ToUpper(trimmed) && len(trimmed) > 2 {
		return "  " + boldWhite.Render(trimmed)
	}

	// Route lines (contain "/", "via", "dev")
	if strings.Contains(line, " via ") || strings.Contains(line, " dev ") {
		parts := strings.Fields(line)
		var colored []string
		for _, p := range parts {
			if isIPLike(p) {
				colored = append(colored, ipStyle.Render(p))
			} else if p == "via" || p == "dev" {
				colored = append(colored, dimStyle.Render(p))
			} else if strings.HasPrefix(p, "wg-") || strings.HasPrefix(p, "utun") {
				colored = append(colored, boldWhite.Render(p))
			} else {
				colored = append(colored, dimStyle.Render(p))
			}
		}
		return "  " + strings.Join(colored, " ")
	}

	// Default: dim
	return "  " + dimStyle.Render(trimmed)
}

// isIPLike returns true if the string looks like an IP address or CIDR
func isIPLike(s string) bool {
	for _, c := range s {
		if c == '.' || c == ':' || c == '/' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		// Allow comma-separated IPs
		if c == ',' || c == ' ' {
			continue
		}
		return false
	}
	return len(s) > 4 && strings.Contains(s, ".")
}

// ─── Footer ───

func (m Model) renderFooter(width int) string {
	// Left: shortcuts
	var pairs []string
	switch m.state {
	case StateDisconnected:
		pairs = append(pairs, m.helpPair("c", "connect"), m.helpPair("l", "login"))
	case StateConnected:
		pairs = append(pairs, m.helpPair("d", "disconnect"))
		if !m.managed {
			pairs = append(pairs, m.helpPair("r", "renew"), m.helpPair("p", "ping"))
		}
	case StateAuthRequired:
		pairs = append(pairs, m.helpPair("l", "login"), m.helpPair("c", "connect"))
	case StateLoggingIn:
		pairs = append(pairs, m.helpPair("enter", "submit"), m.helpPair("esc", "cancel"))
	}
	if m.tab == 1 {
		pairs = append(pairs, m.helpPair("↑↓", "navigate"), m.helpPair("enter", "select"))
		if m.managed {
			pairs = append(pairs, m.helpPair("1-9", "quick"))
		} else {
			pairs = append(pairs, m.helpPair("0-9", "quick"))
		}
		if m.vpnMode && len(m.exitNodes) > 0 {
			pairs = append(pairs, m.helpPair("e", "vpn"), m.helpPair("s", "split"))
		}
	}
	if m.tab == 3 {
		pairs = append(pairs, m.helpPair("↑↓", "scroll"), m.helpPair("f", "filter"), m.helpPair("G", "tail"), m.helpPair("g", "top"))
	}
	pairs = append(pairs, m.helpPair("tab", "view"), m.helpPair("q", "quit"))
	left := strings.Join(pairs, "  ")

	// Right: version
	right := sVersion.Render("v" + version.Version)

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}

	return left + strings.Repeat(" ", gap) + right
}

func (m Model) helpPair(key, desc string) string {
	return sHelpKey.Render("<"+key+">") + sHelpDesc.Render(desc)
}

// ─── Sparkline ───

func (m Model) renderSparkline() string {
	blocks := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

	// Collect samples
	count := len(m.rxHistory)
	if !m.rxHistoryFull {
		count = m.rxHistoryIdx
	}
	if count == 0 {
		return sSparkLow.Render("▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁")
	}

	// Find max for normalization
	var maxVal int64
	for _, v := range m.rxHistory {
		if v > maxVal {
			maxVal = v
		}
	}
	if maxVal == 0 {
		maxVal = 1
	}

	// Build sparkline from history (ordered oldest → newest)
	var spark strings.Builder
	total := len(m.rxHistory)
	startIdx := 0
	if m.rxHistoryFull {
		startIdx = m.rxHistoryIdx // oldest sample
	}

	for i := 0; i < total; i++ {
		idx := (startIdx + i) % total
		val := m.rxHistory[idx]
		level := int(val * int64(len(blocks)-1) / maxVal)
		if level < 0 {
			level = 0
		}
		if level >= len(blocks) {
			level = len(blocks) - 1
		}

		ch := string(blocks[level])
		if level <= 2 {
			spark.WriteString(sSparkLow.Render(ch))
		} else if level <= 5 {
			spark.WriteString(sSparkMed.Render(ch))
		} else {
			spark.WriteString(sSparkHigh.Render(ch))
		}
	}

	return spark.String()
}

// ─── TTL Progress Bar ───

func (m Model) renderTTLBar() string {
	if m.expiresAt.IsZero() || m.ttlSeconds <= 0 {
		return "—"
	}

	remaining := time.Until(m.expiresAt)
	if remaining < 0 {
		remaining = 0
	}

	total := time.Duration(m.ttlSeconds) * time.Second
	ratio := float64(remaining) / float64(total)
	if ratio > 1 {
		ratio = 1
	}

	barWidth := 20
	filled := int(ratio * float64(barWidth))
	if filled < 0 {
		filled = 0
	}
	if filled > barWidth {
		filled = barWidth
	}
	empty := barWidth - filled

	// Color based on remaining percentage
	var barStyle lipgloss.Style
	if ratio > 0.5 {
		barStyle = lipgloss.NewStyle().Foreground(cSuccess)
	} else if ratio > 0.2 {
		barStyle = lipgloss.NewStyle().Foreground(cWarning)
	} else {
		barStyle = lipgloss.NewStyle().Foreground(cDanger)
	}

	emptyStyle := lipgloss.NewStyle().Foreground(cFgMuted)
	bar := barStyle.Render(strings.Repeat("█", filled)) + emptyStyle.Render(strings.Repeat("░", empty))

	pct := int(ratio * 100)
	return fmt.Sprintf("[%s] %d%%", bar, pct)
}

// ─── Latency Indicator ───

func (m Model) renderLatency() string {
	if m.latencyMs < 0 {
		return sStatusOff.Render("timeout")
	}
	if m.latencyMs == 0 && m.latencyColor == 0 {
		// No measurement yet
		return lipgloss.NewStyle().Foreground(cFgDim).Render("measuring...")
	}

	ms := fmt.Sprintf("%dms", m.latencyMs)
	switch m.latencyColor {
	case 0: // good (<50ms)
		return sStatusOn.Render(ms + " ●")
	case 1: // medium (50-150ms)
		return sStatusWarn.Render(ms + " ◐")
	default: // bad (>150ms)
		return sStatusOff.Render(ms + " ○")
	}
}

// ─── Table Helpers ───

// tableRow renders a multi-column row with │ separators, auto-sizing columns
func (m Model) tableRow(width int, headers []string, values []string) string {
	n := len(headers)
	if n == 0 {
		return ""
	}

	colW := (width - n) / n // equal distribution
	if colW < 10 {
		colW = 10
	}

	var parts []string
	for i := 0; i < n; i++ {
		label := lipgloss.NewStyle().Foreground(cFgDim).Render(headers[i] + ": ")
		val := values[i]
		if val == "" {
			val = "—"
		}
		cell := label + val
		// Pad to column width
		cellWidth := lipgloss.Width(cell)
		pad := colW - cellWidth
		if pad < 0 {
			pad = 0
		}
		parts = append(parts, " "+cell+strings.Repeat(" ", pad))
	}

	sep := sCrumbSep.Render("│")
	return strings.Join(parts, sep)
}

// groupColWidths calculates column widths for the groups table
func (m Model) groupColWidths(width int) [4]int {
	// #(4), PROJECT(flexible), CIDRs(18), PUBLISHERS(14)
	fixed := 4 + 18 + 14
	flexible := width - fixed - 6 // separators and padding
	if flexible < 12 {
		flexible = 12
	}
	return [4]int{4, flexible, 18, 14}
}

// hLine renders a full-width horizontal separator
func (m Model) hLine(width int) string {
	if width < 4 {
		width = 40
	}
	return lipgloss.NewStyle().Foreground(cBorder).Render(strings.Repeat("─", width))
}

// fieldRow renders a key:value row
func (m Model) fieldRow(label, value string) string {
	if value == "" {
		value = "—"
	}
	return " " + sFieldKey.Render(label) + sFieldVal.Render(value)
}

// ─── Formatters ───

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

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	h := int(d.Hours())
	mn := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, mn)
	}
	return fmt.Sprintf("%dm", mn)
}

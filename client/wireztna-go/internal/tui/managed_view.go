package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) renderManagedStatusTab(width int) string {
	var rows []string
	rows = append(rows, "")
	if m.state == StateLoggingIn {
		rows = append(rows, sPanelTitle.Render(" LOGIN"), m.hLine(width), "")
		if m.loginStep == loginStepEmail {
			rows = append(rows, "  "+sCrumb.Render("Enter your email (press Enter to use default):"), "")
			rows = append(rows, "  Email: "+sStatusInfo.Render(m.loginEmail)+"█")
		} else {
			rows = append(rows, "  "+sCrumb.Render("A code was sent to: ")+sStatusInfo.Render(m.loginEmail), "")
			rows = append(rows, "  Code:  "+sStatusInfo.Render(m.loginOTP)+"█")
		}
		if m.loginError != "" {
			rows = append(rows, "", "  "+sStatusWarn.Render("✗ "+m.loginError))
		}
		rows = append(rows, "", "  "+lipgloss.NewStyle().Foreground(cFgDim).Render("Press Enter to submit · Esc to cancel"), "")
		return strings.Join(rows, "\n")
	}
	if m.state == StateAuthRequired {
		rows = append(rows, sPanelTitle.Render(" AUTHENTICATION"), m.hLine(width), "")
		rows = append(rows, "  "+sStatusWarn.Render("⚠ The managed service requires authentication"), "")
		rows = append(rows, "  "+sCrumb.Render("Press 'l' to authenticate by OTP. Connection lifecycle remains service-owned."), "")
		return strings.Join(rows, "\n")
	}
	if !m.managedReady {
		rows = append(rows, sPanelTitle.Render(" MANAGED SERVICE"), m.hLine(width), "")
		rows = append(rows, "  "+sStatusWarn.Render("Service unavailable — retrying the fixed IPC v2 socket"), "")
		rows = append(rows, "  "+lipgloss.NewStyle().Foreground(cFgDim).Render("Direct tunnel, DNS, and legacy IPC fallback are disabled on macOS."), "")
		return strings.Join(rows, "\n")
	}

	rows = append(rows, sPanelTitle.Render(" MANAGED CONNECTION"), m.hLine(width))
	rows = append(rows, m.fieldRow("State", m.state.String()))
	project := m.groupName
	if project == "" {
		project = "Select a project in the Projects tab"
	}
	rows = append(rows, m.fieldRow("Project", project))
	mode := "Split tunnel (ZTNA)"
	if m.exitNodeID != "" {
		mode = "VPN (exit node)"
		if m.exitNodeName != "" {
			mode += " — " + m.exitNodeName
		}
	}
	rows = append(rows, m.fieldRow("Mode", mode))
	if m.managedOperationName != "" {
		stage := m.managedOperationName
		if m.managedProgress != "" {
			stage += " — " + strings.ReplaceAll(string(m.managedProgress), "_", " ")
		}
		rows = append(rows, m.fieldRow("Operation", sStatusInfo.Render(stage)))
	}
	rows = append(rows, "", sPanelTitle.Render(" SERVICE HEALTH"), m.hLine(width))
	rows = append(rows, m.tableRow(width,
		[]string{"WireGuard", "Routes", "DNS"},
		[]string{string(m.managedHealth.WireGuard), string(m.managedHealth.Routes), string(m.managedHealth.DNS)},
	))
	rows = append(rows, m.fieldRow("End-to-end", string(m.managedHealth.EndToEnd)))
	rows = append(rows, "", lipgloss.NewStyle().Foreground(cFgDim).Render("  Session ID, lease, endpoint, handshake, and traffic are not exposed by IPC v2."), "")
	return strings.Join(rows, "\n")
}

func (m Model) renderManagedNetworkTab(width int) string {
	var rows []string
	rows = append(rows, "", sPanelTitle.Render(" SERVICE-REPORTED HEALTH"), m.hLine(width))
	rows = append(rows, m.fieldRow("WireGuard", string(m.managedHealth.WireGuard)))
	rows = append(rows, m.fieldRow("Routes", string(m.managedHealth.Routes)))
	rows = append(rows, m.fieldRow("DNS", string(m.managedHealth.DNS)))
	rows = append(rows, m.fieldRow("End-to-end", string(m.managedHealth.EndToEnd)), "")

	if m.groupID != "" {
		rows = append(rows, sPanelTitle.Render(" CATALOG RESOURCES"), m.hLine(width))
		for _, group := range m.groups {
			if group.ID != m.groupID {
				continue
			}
			rows = append(rows, m.fieldRow("Project", group.Name))
			if len(group.CIDRs) > 0 {
				rows = append(rows, m.fieldRow("CIDRs", strings.Join(group.CIDRs, ", ")))
			}
			for _, publisher := range group.Publishers {
				detail := fmt.Sprintf("%s (%s)", publisher.Name, publisher.Status)
				if len(publisher.ExposedCIDRs) > 0 {
					detail += " → " + strings.Join(publisher.ExposedCIDRs, ", ")
				}
				rows = append(rows, m.fieldRow("", detail))
			}
			break
		}
	}
	rows = append(rows, "", lipgloss.NewStyle().Foreground(cFgDim).Render("  Local interface, route, DNS, and tunnel probing are disabled in managed mode."))
	return strings.Join(rows, "\n")
}

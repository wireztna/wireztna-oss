//go:build windows

package dns

import (
	"fmt"
	"os/exec"
	"strings"
)

// configureOS sets up split DNS on Windows using NRPT (Name Resolution Policy Table).
// NRPT rules tell Windows to send queries for specific domains to a designated DNS server
// while leaving all other DNS traffic going through the default resolver.
//
// This is the Windows equivalent of macOS /etc/resolver/ files or Linux resolvectl domains.
// Requires Windows 10 1809+ and administrator privileges.
func (m *Manager) configureOS() error {
	for _, zone := range m.zones {
		if zone == "" {
			continue
		}

		// Add NRPT rule via PowerShell
		// The namespace uses a leading dot for suffix matching: ".compute.internal"
		// matches anything.compute.internal
		namespace := "." + zone

		// PowerShell command to add NRPT rule
		// -Namespace: the DNS suffix to match
		// -NameServers: where to send matching queries
		// -DisplayName: for identification during cleanup
		psCmd := fmt.Sprintf(
			`Add-DnsClientNrptRule -Namespace "%s" -NameServers "%s" -DisplayName "WireZTNA:%s"`,
			namespace, m.tunnelDNS, zone,
		)

		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psCmd)
		output, err := cmd.CombinedOutput()
		if err != nil {
			// Check if rule already exists
			if strings.Contains(string(output), "already exists") {
				// Remove and re-add
				m.removeNrptRule(zone)
				cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psCmd)
				if output, err = cmd.CombinedOutput(); err != nil {
					return fmt.Errorf("NRPT rule for %s failed: %s: %w", zone, string(output), err)
				}
			} else {
				return fmt.Errorf("NRPT rule for %s failed: %s: %w", zone, string(output), err)
			}
		}
	}

	// Flush DNS cache so new rules take effect immediately
	exec.Command("ipconfig", "/flushdns").Run()

	return nil
}

// cleanupOS removes all WireZTNA-managed NRPT rules on Windows.
func (m *Manager) cleanupOS() error {
	// Get all NRPT rules and remove those with our DisplayName prefix
	psCmd := `Get-DnsClientNrptRule | Where-Object { $_.DisplayName -like "WireZTNA:*" } | Remove-DnsClientNrptRule -Force`

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If no rules found, PowerShell might error — that's fine
		if strings.Contains(string(output), "No NRPT rules") ||
			strings.Contains(string(output), "Cannot find") {
			return nil
		}
		return fmt.Errorf("NRPT cleanup failed: %s: %w", string(output), err)
	}

	// Flush DNS cache
	exec.Command("ipconfig", "/flushdns").Run()

	return nil
}

// removeNrptRule removes a specific NRPT rule by zone name.
func (m *Manager) removeNrptRule(zone string) {
	psCmd := fmt.Sprintf(
		`Get-DnsClientNrptRule | Where-Object { $_.DisplayName -eq "WireZTNA:%s" } | Remove-DnsClientNrptRule -Force`,
		zone,
	)
	exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psCmd).Run()
}

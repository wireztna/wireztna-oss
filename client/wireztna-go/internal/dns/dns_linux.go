//go:build linux

package dns

import (
	"fmt"
	"os/exec"
)

// configureOS sets up split DNS on Linux.
func (m *Manager) configureOS() error {
	return m.configureLinux()
}

// cleanupOS removes split DNS configuration on Linux.
func (m *Manager) cleanupOS() error {
	return m.cleanupLinux()
}

func (m *Manager) configureLinux() error {
	if _, err := exec.LookPath("resolvectl"); err != nil {
		return fmt.Errorf("systemd-resolved not available — manual DNS configuration required")
	}

	ifaceName := "wg-wireztna"
	iface, err := findInterface(ifaceName)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifaceName, err)
	}

	cmd := exec.Command("resolvectl", "dns", iface, m.tunnelDNS)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("resolvectl dns failed: %s: %w", string(output), err)
	}

	args := []string{"domain", iface}
	for _, zone := range m.zones {
		args = append(args, "~"+zone)
	}
	cmd = exec.Command("resolvectl", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("resolvectl domain failed: %s: %w", string(output), err)
	}
	return nil
}

func (m *Manager) cleanupLinux() error {
	if _, err := exec.LookPath("resolvectl"); err != nil {
		return nil
	}
	exec.Command("resolvectl", "revert", "wg-wireztna").Run()
	return nil
}

// findInterface returns the interface name if it exists.
func findInterface(name string) (string, error) {
	cmd := exec.Command("ip", "link", "show", name)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("interface %s not found", name)
	}
	return name, nil
}

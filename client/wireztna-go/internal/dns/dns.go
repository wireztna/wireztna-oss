// Package dns handles split DNS configuration across platforms.
//
// Platform-specific implementations are in:
//   - dns_darwin.go (macOS)
//   - dns_linux.go (Linux via systemd-resolved)
//   - dns_windows.go (Windows via NRPT)
package dns

import "fmt"

// Manager handles split DNS configuration and cleanup.
type Manager struct {
	zones     []string
	tunnelDNS string
}

// NewManager creates a new DNS manager.
func NewManager() *Manager {
	return &Manager{}
}

// Configure sets up split DNS for the given zones.
// Delegates to platform-specific implementation (configureOS).
func (m *Manager) Configure(tunnelDNS string, zones []string) error {
	m.tunnelDNS = tunnelDNS
	m.zones = zones

	if tunnelDNS == "" {
		return fmt.Errorf("tunnel DNS address required")
	}

	return m.configureOS()
}

// Cleanup removes all wireztna-managed DNS configuration.
// Delegates to platform-specific implementation (cleanupOS).
func (m *Manager) Cleanup() error {
	return m.cleanupOS()
}

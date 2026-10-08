package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// Default paths
	ConfigDir  = "/etc/wireztna-publisher"
	StateFile  = "state.json"
	PrivateKey = "private.key"
	WGConfFile = "wg-broker.conf"

	// Default values
	DefaultWGInterface      = "wg-broker"
	DefaultWGPort           = 51821
	DefaultHeartbeatInterval = 30
	DefaultWatchdogThreshold = 180
)

// PublisherState is the persisted enrollment state
type PublisherState struct {
	PublisherID     string    `json:"publisher_id"`
	PublisherApiKey string    `json:"publisher_api_key,omitempty"`
	EnrolledAt     time.Time `json:"enrolled_at"`
	BrokerEndpoint string    `json:"broker_endpoint"`
	BrokerPubKey   string    `json:"broker_public_key"`
	TunnelIP       string    `json:"tunnel_ip"`
	BrokerTunnelIP string    `json:"broker_tunnel_ip"`
	AllowedIPs     string    `json:"allowed_ips"`
	ControlPlaneURL string   `json:"control_plane_url"`
	PublisherName  string    `json:"publisher_name"`
}

// InstallConfig holds the runtime configuration for the publisher
type InstallConfig struct {
	ControlPlaneURL string
	EnrollmentToken string
	PublisherName   string
	WGInterface     string
	WGPort          int
	HeartbeatInterval int
	WatchdogThreshold int
	LocalDNS        string
}

// DefaultInstallConfig returns config with sane defaults
func DefaultInstallConfig() *InstallConfig {
	hostname, _ := os.Hostname()
	return &InstallConfig{
		PublisherName:     hostname,
		WGInterface:       DefaultWGInterface,
		WGPort:            DefaultWGPort,
		HeartbeatInterval: DefaultHeartbeatInterval,
		WatchdogThreshold: DefaultWatchdogThreshold,
	}
}

// EnsureConfigDir creates the config directory if it doesn't exist
func EnsureConfigDir() error {
	return os.MkdirAll(ConfigDir, 0700)
}

// StatePath returns the full path to the state file
func StatePath() string {
	return filepath.Join(ConfigDir, StateFile)
}

// PrivateKeyPath returns the full path to the private key file
func PrivateKeyPath() string {
	return filepath.Join(ConfigDir, PrivateKey)
}

// WGConfigPath returns the full path to the WireGuard config file
func WGConfigPath() string {
	return filepath.Join(ConfigDir, WGConfFile)
}

// IsEnrolled checks if the publisher has been enrolled
func IsEnrolled() bool {
	_, err := os.Stat(StatePath())
	return err == nil
}

// LoadState reads the persisted enrollment state
func LoadState() (*PublisherState, error) {
	data, err := os.ReadFile(StatePath())
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}
	var state PublisherState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state file: %w", err)
	}
	return &state, nil
}

// SaveState persists the enrollment state
func SaveState(state *PublisherState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}
	if err := os.WriteFile(StatePath(), data, 0600); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}
	return nil
}

// SavePrivateKey writes the WG private key to disk
func SavePrivateKey(key string) error {
	return os.WriteFile(PrivateKeyPath(), []byte(key), 0600)
}

// LoadPrivateKey reads the WG private key from disk
func LoadPrivateKey() (string, error) {
	data, err := os.ReadFile(PrivateKeyPath())
	if err != nil {
		return "", fmt.Errorf("failed to read private key: %w", err)
	}
	return string(data), nil
}

// RemoveAll deletes the entire config directory
func RemoveAll() error {
	return os.RemoveAll(ConfigDir)
}

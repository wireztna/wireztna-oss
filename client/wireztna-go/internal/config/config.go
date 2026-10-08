// Package config handles client configuration, token storage, and state.
package config

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// ClientConfig holds the client's tunnel configuration.
type ClientConfig struct {
	// API
	APIURL   string `yaml:"api_url"`
	Email    string `yaml:"email"`    // Primary login identifier (new)
	Username string `yaml:"username"` // Backward compat / display name

	// WireGuard identity
	PrivateKey string `yaml:"private_key"`
	OverlayIP  string `yaml:"overlay_ip"`

	// Broker connection
	BrokerPublicKey string `yaml:"broker_public_key"`
	BrokerEndpoint  string `yaml:"broker_endpoint"`
	BrokerOverlayIP string `yaml:"broker_overlay_ip"`

	// Tunnel
	AllowedIPs []string `yaml:"allowed_ips"`
	TunnelDNS  string   `yaml:"tunnel_dns"`

	// Behavior
	Interface   string `yaml:"interface"`
	RenewBefore int    `yaml:"renew_before_minutes"`
}

// LoginIdentifier returns the best identifier for login:
// email if available (new configs), otherwise username (old configs).
func (c *ClientConfig) LoginIdentifier() string {
	if c.Email != "" {
		return c.Email
	}
	return c.Username
}

// Load reads the client config from viper (merging config file + env + flags).
func Load() *ClientConfig {
	return loadFromViper(viper.GetViper())
}

func loadFromViper(values *viper.Viper) *ClientConfig {
	return &ClientConfig{
		APIURL:          values.GetString("api_url"),
		Email:           values.GetString("email"),
		Username:        values.GetString("username"),
		PrivateKey:      values.GetString("private_key"),
		OverlayIP:       values.GetString("overlay_ip"),
		BrokerPublicKey: values.GetString("broker_public_key"),
		BrokerEndpoint:  values.GetString("broker_endpoint"),
		BrokerOverlayIP: values.GetString("broker_overlay_ip"),
		AllowedIPs:      values.GetStringSlice("allowed_ips"),
		TunnelDNS:       values.GetString("tunnel_dns"),
		Interface:       values.GetString("interface"),
		RenewBefore:     values.GetInt("renew_before_minutes"),
	}
}

// ParseClientConfig parses one explicit YAML document without consulting or
// mutating the process-global viper state.
func ParseClientConfig(data []byte) (*ClientConfig, error) {
	values := viper.New()
	values.SetConfigType("yaml")
	if err := values.ReadConfig(bytes.NewReader(data)); err != nil {
		return nil, err
	}
	return loadFromViper(values), nil
}

// ReadConfigFile loads a YAML config through a platform no-follow handle.
// Explicit paths are required to exist; callers using the default location
// should use ReadResolvedConfig.
func ReadConfigFile(path string) error {
	data, err := readPrivateSharedFile(path)
	if err != nil {
		return err
	}
	viper.SetConfigType("yaml")
	return viper.ReadConfig(bytes.NewReader(data))
}

// ReadResolvedConfig safely loads the selected shared or user config. A fresh
// unenrolled installation has no config yet and is not an error.
func ReadResolvedConfig() error {
	dir, err := ResolveConfigDir()
	if err != nil {
		return err
	}
	if err := ReadConfigFile(filepath.Join(dir, "config.yaml")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// configDir returns the wireztna config directory.
// On Windows: always uses %USERPROFILE%\.wireztna for CLI operations.
// UserConfigDir returns the user-specific config directory (~/.wireztna).
// Exported for use by the service when it needs to fall back to user config.
func UserConfigDir() (string, error) {
	return configDir()
}

// The config written by `wireztna enroll` goes to the user's home.
// On macOS/Linux: uses ~/.wireztna (with sudo HOME fix on Linux).
func configDir() (string, error) {
	home := realUserHome()
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	dir := filepath.Join(home, ".wireztna")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := hardenLegacyPrivateFiles(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// hardenLegacyPrivateFiles repairs plaintext credentials created by older
// releases before callers read them. Refuse links and non-regular files so a
// privileged service cannot be tricked into changing permissions elsewhere.
func hardenLegacyPrivateFiles(dir string) error {
	for _, filename := range []string{"config.yaml", "token", "enrollment-private-key"} {
		path := filepath.Join(dir, filename)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect legacy private file %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse non-regular legacy private file %s", path)
		}
		if err := hardenSharedFilePermissions(path); err != nil {
			return fmt.Errorf("harden legacy private file %s: %w", path, err)
		}
	}
	return nil
}

// SaveToken persists the JWT token to disk.
func SaveToken(token string) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	tokenFile := filepath.Join(dir, "token")
	err = writePrivateSharedFile(tokenFile, []byte(token))
	if err != nil {
		// If the shared dir is not writable (non-admin user), fall back to user home
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return err // Return the original error
		}
		userDir := filepath.Join(home, ".wireztna")
		os.MkdirAll(userDir, 0700)
		tokenFile = filepath.Join(userDir, "token")
		if writeErr := writePrivateSharedFile(tokenFile, []byte(token)); writeErr != nil {
			return writeErr
		}
	}
	// Sync token to shared dir (ProgramData on Windows) so the helper always
	// has the current user's token — keeps TUI/GUI/CLI synchronized.
	syncFileToSharedDir("token", []byte(token))
	return nil
}

// LoadToken reads the cached JWT token from disk.
func LoadToken() (string, error) {
	// Try resolved config dir first (checks ProgramData on Windows if service installed)
	dir, err := ResolveConfigDir()
	if err == nil {
		tokenFile := filepath.Join(dir, "token")
		data, err := readPrivateSharedFile(tokenFile)
		if err == nil {
			return strings.TrimSpace(string(data)), nil
		}
	}

	// Fallback: user-specific config dir
	dir, err = configDir()
	if err != nil {
		return "", err
	}
	tokenFile := filepath.Join(dir, "token")
	data, err := readPrivateSharedFile(tokenFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// IsTokenExpired checks if a JWT token has expired by decoding the payload.
func IsTokenExpired(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return true
	}

	// Decode payload (with padding fix)
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		// Try without padding (some JWTs use raw base64url)
		decoded, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return true
		}
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return true
	}

	// Token is expired if exp is in the past (with 60s grace)
	return time.Now().Unix() > (claims.Exp - 60)
}

// AcquireRemoteMutation serializes control-plane session mutations across
// direct clients and the managed service sharing this identity. The separate
// lock intentionally does not block durable disconnect intent updates.
func AcquireRemoteMutation(ctx context.Context) (func() error, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dir, err := ResolveConfigDir()
	if err != nil {
		return nil, err
	}
	return acquireRemoteMutationFileLock(ctx, filepath.Join(dir, "remote-mutation.lock"))
}

// AcquireRemoteMutationForOwner acquires the same lock from an explicit,
// pre-validated owner configuration directory. Privileged Unix services use
// this path instead of deriving identity from HOME, which is normally absent
// from launchd and service environments.
func AcquireRemoteMutationForOwner(ctx context.Context, dir string, ownerUID uint32) (func() error, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, errors.New("remote mutation directory must be a clean absolute path")
	}
	return acquireRemoteMutationFileLockForOwner(ctx, dir, "remote-mutation.lock", ownerUID)
}

// ErrStaleLifecycle reports that a lifecycle operation was superseded by a
// newer connect/disconnect intent before it could commit runtime state.
var ErrStaleLifecycle = errors.New("stale lifecycle operation")

// BeginLifecycle records a new explicit lifecycle intent and returns its
// monotonic generation. Disconnect increments the generation before any
// teardown so in-flight writers can no longer commit stale connected state.
func BeginLifecycle(disconnected bool) (uint64, error) {
	var generation uint64
	err := withStateLock(func(stateFile string) error {
		state, err := loadStateFile(stateFile)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			state = &RuntimeState{}
		}
		state.LifecycleGeneration++
		state.UserDisconnected = disconnected
		generation = state.LifecycleGeneration
		return writeStateFile(stateFile, state)
	})
	return generation, err
}

// SaveState persists runtime state without a lifecycle compare-and-swap. New
// lifecycle code should use SaveStateForLifecycle; this wrapper remains for
// backward-compatible non-lifecycle callers while still refusing to overwrite
// an explicit disconnect.
func SaveState(state *RuntimeState) error {
	if state == nil {
		return fmt.Errorf("runtime state is nil")
	}
	return withStateLock(func(stateFile string) error {
		current, err := loadStateFile(stateFile)
		if err == nil {
			if current.UserDisconnected && !state.UserDisconnected {
				return ErrStaleLifecycle
			}
			state.LifecycleGeneration = current.LifecycleGeneration
		} else if !os.IsNotExist(err) {
			return err
		}
		return writeStateFile(stateFile, state)
	})
}

// SaveStateForLifecycle commits state only if generation still represents the
// latest explicit intent. It is the durable stale-result gate shared by direct
// clients, the managed service, and background renewal.
func SaveStateForLifecycle(generation uint64, state *RuntimeState) error {
	if state == nil {
		return fmt.Errorf("runtime state is nil")
	}
	return withStateLock(func(stateFile string) error {
		current, err := loadStateFile(stateFile)
		if err != nil {
			return err
		}
		if current.LifecycleGeneration != generation || current.UserDisconnected {
			return ErrStaleLifecycle
		}
		state.LifecycleGeneration = generation
		state.UserDisconnected = false
		return writeStateFile(stateFile, state)
	})
}

// LoadState reads the runtime state from disk while coordinating with writers.
func LoadState() (*RuntimeState, error) {
	var state *RuntimeState
	err := withStateLock(func(stateFile string) error {
		var err error
		state, err = loadStateFile(stateFile)
		return err
	})
	return state, err
}

// SetUserDisconnected records a new explicit lifecycle intent. Existing
// session metadata is retained for the next explicit connect.
func SetUserDisconnected(disconnected bool) error {
	_, err := BeginLifecycle(disconnected)
	return err
}

// IsLifecycleCurrent reports whether generation is still the latest connected
// intent. Read failures fail closed so stale work cannot mutate local state.
func IsLifecycleCurrent(generation uint64) bool {
	state, err := LoadState()
	return err == nil && state.LifecycleGeneration == generation && !state.UserDisconnected
}

// IsUserDisconnected reports whether the most recent explicit action was a
// disconnect. Missing state is backward-compatible; other read failures are
// treated as disconnected so a corrupt/transient state cannot trigger a tunnel.
func IsUserDisconnected() bool {
	state, err := LoadState()
	if err != nil {
		return !os.IsNotExist(err)
	}
	return state.UserDisconnected
}

var stateMu sync.Mutex

const stateLockTimeout = 5 * time.Second

func withStateLock(fn func(stateFile string) error) error {
	stateMu.Lock()
	defer stateMu.Unlock()

	dir, err := configDir()
	if err != nil {
		return err
	}
	release, err := acquireStateFileLock(filepath.Join(dir, "state.lock"))
	if err != nil {
		return err
	}
	defer release()
	return fn(filepath.Join(dir, "state.json"))
}

func loadStateFile(stateFile string) (*RuntimeState, error) {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, err
	}
	var state RuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func writeStateFile(stateFile string, state *RuntimeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(stateFile), ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceStateFile(tmpName, stateFile)
}

// RuntimeState holds ephemeral runtime info persisted across restarts.
type RuntimeState struct {
	SessionID           string    `json:"session_id"`
	ExpiresAt           time.Time `json:"expires_at"`
	PresharedKey        string    `json:"preshared_key"`
	GroupID             string    `json:"group_id,omitempty"`
	GroupName           string    `json:"group_name,omitempty"`
	ExitNodeID          string    `json:"exit_node_id,omitempty"`
	ExitNodeName        string    `json:"exit_node_name,omitempty"`
	IsExitNode          bool      `json:"is_exit_node,omitempty"`
	ConnectedAt         time.Time `json:"connected_at"`
	InterfaceName       string    `json:"interface_name"`
	UserDisconnected    bool      `json:"user_disconnected,omitempty"`
	LifecycleGeneration uint64    `json:"lifecycle_generation,omitempty"`
}

// WriteDefaultConfig creates a template config file if none exists.
func WriteDefaultConfig() error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	configFile := filepath.Join(dir, "config.yaml")

	if _, err := os.Stat(configFile); err == nil {
		return nil // Already exists
	}

	template := `# WireZTNA Client Configuration
# Generated by wireztna init

# Control Plane API URL (required)
api_url: ""

# Your email address (used for login)
# email: ""

# Display name (optional)
# username: ""

# WireGuard identity — generated at first connect or set manually
# private_key: ""
# overlay_ip: ""

# Broker connection info — provided by admin or fetched from control plane
# broker_public_key: ""
# broker_endpoint: ""
# broker_overlay_ip: "10.200.0.1"

# Tunnel settings
# tunnel_dns: "10.200.0.1"
# allowed_ips:
#   - "10.200.0.1/32"
#   - "10.50.0.0/16"

# Interface name
interface: "wg-wireztna"

# Renew session this many minutes before expiry (default: 30)
renew_before_minutes: 30
`
	fmt.Printf("Writing default config to %s\n", configFile)
	return writePrivateSharedFile(configFile, []byte(template))
}

// EnrollConfig holds the parameters received from enrollment.
type EnrollConfig struct {
	APIURL          string
	Email           string
	Username        string
	PrivateKey      string
	OverlayIP       string
	BrokerPublicKey string
	BrokerEndpoint  string
	BrokerOverlayIP string
	AllowedIPs      []string
	DNSServer       string
}

type enrollConfigDocument struct {
	APIURL          string   `yaml:"api_url"`
	Email           string   `yaml:"email"`
	Username        string   `yaml:"username"`
	PrivateKey      string   `yaml:"private_key"`
	OverlayIP       string   `yaml:"overlay_ip"`
	BrokerPublicKey string   `yaml:"broker_public_key"`
	BrokerEndpoint  string   `yaml:"broker_endpoint"`
	BrokerOverlayIP string   `yaml:"broker_overlay_ip"`
	TunnelDNS       string   `yaml:"tunnel_dns"`
	AllowedIPs      []string `yaml:"allowed_ips"`
	Interface       string   `yaml:"interface"`
	RenewBefore     int      `yaml:"renew_before_minutes"`
}

// WriteEnrollConfig atomically replaces config.yaml with validated enrollment data.
func WriteEnrollConfig(cfg *EnrollConfig) error {
	if cfg == nil {
		return errors.New("enrollment config is required")
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	content, err := yaml.Marshal(enrollConfigDocument{
		APIURL:          cfg.APIURL,
		Email:           cfg.Email,
		Username:        cfg.Username,
		PrivateKey:      cfg.PrivateKey,
		OverlayIP:       cfg.OverlayIP,
		BrokerPublicKey: cfg.BrokerPublicKey,
		BrokerEndpoint:  cfg.BrokerEndpoint,
		BrokerOverlayIP: cfg.BrokerOverlayIP,
		TunnelDNS:       cfg.DNSServer,
		AllowedIPs:      append([]string(nil), cfg.AllowedIPs...),
		Interface:       "wg-wireztna",
		RenewBefore:     30,
	})
	if err != nil {
		return fmt.Errorf("encode enrollment config: %w", err)
	}
	content = append([]byte("# WireZTNA Client Configuration\n# Generated by wireztna enroll\n\n"), content...)
	configFile := filepath.Join(dir, "config.yaml")
	if err := writePrivateSharedFile(configFile, content); err != nil {
		return err
	}
	// Keep the Windows elevated helper synchronized. On Unix SharedConfigDir
	// resolves to the user directory, so this becomes an inexpensive no-op.
	syncFileToSharedDir("config.yaml", content)
	return nil
}

// LoadPendingEnrollmentPrivateKey returns the durable key created before the
// one-shot enrollment POST. It allows an idempotent retry after a local write failure.
func LoadPendingEnrollmentPrivateKey() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	data, err := readPrivateSharedFile(filepath.Join(dir, "enrollment-private-key"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" || strings.ContainsAny(value, "\r\n\t ") {
		return "", errors.New("pending enrollment private key is invalid")
	}
	return value, nil
}

// SavePendingEnrollmentPrivateKey persists the generated key before contacting
// the control plane so the same public key can be used for a safe retry.
func SavePendingEnrollmentPrivateKey(value string) error {
	if value == "" || strings.ContainsAny(value, "\r\n\t ") {
		return errors.New("pending enrollment private key is invalid")
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	return writePrivateSharedFile(filepath.Join(dir, "enrollment-private-key"), []byte(value+"\n"))
}

// ClearPendingEnrollmentPrivateKey removes only the exact private regular file.
func ClearPendingEnrollmentPrivateKey() error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "enrollment-private-key")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse non-regular pending enrollment key %s", path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncPrivateDirectory(dir)
}

// syncFileToSharedDir copies a file to the shared config directory (C:\ProgramData\WireZTNA
// on Windows) so that the elevated helper process always reads the same config/token as the
// user's CLI/TUI. This is a best-effort operation — failures are silently ignored because
// the user may not have write access to ProgramData (non-admin). The helper's
// ResolveConfigDir() checks ProgramData first, so keeping it in sync means TUI, GUI, and
// CLI all use the same identity regardless of which one launches the helper.
func syncFileToSharedDir(filename string, data []byte) {
	shared, err := SharedConfigDir()
	if err != nil {
		return // Can't access shared dir — skip silently
	}
	syncFileToDir(shared, filename, data)
}

func syncFileToDir(dir, filename string, data []byte) {
	target := filepath.Join(dir, filename)
	// The platform helper opens with no-follow semantics and validates a regular
	// file before reading, so a privileged sync never dereferences a link.
	if err := hardenSharedFilePermissions(target); err == nil {
		existing, readErr := readPrivateSharedFile(target)
		if readErr != nil {
			return
		}
		if string(existing) == string(data) {
			return
		}
	} else if !os.IsNotExist(err) {
		return
	}
	if err := writePrivateSharedFile(target, data); err == nil {
		// Best effort repair after replacement as well.
		_ = hardenSharedFilePermissions(target)
	}
}

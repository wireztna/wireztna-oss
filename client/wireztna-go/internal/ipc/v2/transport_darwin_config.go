package v2

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultDarwinSocketPath is the local IPC endpoint used by the macOS service.
	DefaultDarwinSocketPath = "/var/run/wireztna/desktop-v2.sock"

	defaultDarwinMaxConnections   = 64
	maximumDarwinMaxConnections   = 1024
	defaultDarwinHandshakeTimeout = 5 * time.Second
	maximumDarwinHandshakeTimeout = time.Minute
	defaultDarwinIdleTimeout      = 2 * time.Minute
	maximumDarwinIdleTimeout      = 24 * time.Hour
	maximumDarwinSocketPathBytes  = 103
)

// ErrDarwinTransportUnsupported is returned by ServeDarwin on non-Darwin builds.
var ErrDarwinTransportUnsupported = errors.New("Darwin IPC transport is not supported on this platform")

// DarwinTransportConfig controls the lifecycle and resource bounds of the
// macOS Unix-domain socket. Zero-valued limits and timeouts use safe defaults.
type DarwinTransportConfig struct {
	SocketPath       string
	OwnerUID         uint64
	MaxConnections   int
	HandshakeTimeout time.Duration
	IdleTimeout      time.Duration
}

func (config DarwinTransportConfig) normalized() (DarwinTransportConfig, error) {
	if config.SocketPath == "" {
		config.SocketPath = DefaultDarwinSocketPath
	}
	if strings.IndexByte(config.SocketPath, 0) >= 0 {
		return DarwinTransportConfig{}, errors.New("Darwin socket path contains a NUL byte")
	}
	if !filepath.IsAbs(config.SocketPath) {
		return DarwinTransportConfig{}, errors.New("Darwin socket path must be absolute")
	}
	if filepath.Clean(config.SocketPath) != config.SocketPath {
		return DarwinTransportConfig{}, errors.New("Darwin socket path must be clean")
	}
	if filepath.Base(config.SocketPath) == "." || filepath.Base(config.SocketPath) == string(filepath.Separator) {
		return DarwinTransportConfig{}, errors.New("Darwin socket path must name a socket file")
	}
	if len([]byte(config.SocketPath)) > maximumDarwinSocketPathBytes {
		return DarwinTransportConfig{}, fmt.Errorf("Darwin socket path exceeds %d bytes", maximumDarwinSocketPathBytes)
	}
	if config.OwnerUID > math.MaxUint32 {
		return DarwinTransportConfig{}, errors.New("Darwin owner UID exceeds the operating-system UID range")
	}

	if config.MaxConnections == 0 {
		config.MaxConnections = defaultDarwinMaxConnections
	}
	if config.MaxConnections < 1 || config.MaxConnections > maximumDarwinMaxConnections {
		return DarwinTransportConfig{}, fmt.Errorf("Darwin max connections must be between 1 and %d", maximumDarwinMaxConnections)
	}

	if config.HandshakeTimeout == 0 {
		config.HandshakeTimeout = defaultDarwinHandshakeTimeout
	}
	if config.HandshakeTimeout < 0 || config.HandshakeTimeout > maximumDarwinHandshakeTimeout {
		return DarwinTransportConfig{}, fmt.Errorf("Darwin handshake timeout must be positive and no greater than %s", maximumDarwinHandshakeTimeout)
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = defaultDarwinIdleTimeout
	}
	if config.IdleTimeout < 0 || config.IdleTimeout > maximumDarwinIdleTimeout {
		return DarwinTransportConfig{}, fmt.Errorf("Darwin idle timeout must be positive and no greater than %s", maximumDarwinIdleTimeout)
	}
	return config, nil
}

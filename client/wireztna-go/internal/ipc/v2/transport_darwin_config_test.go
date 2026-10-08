package v2

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestDarwinTransportConfigDefaults(t *testing.T) {
	config, err := (DarwinTransportConfig{}).normalized()
	if err != nil {
		t.Fatalf("normalize defaults: %v", err)
	}
	if config.SocketPath != DefaultDarwinSocketPath {
		t.Fatalf("SocketPath = %q, want %q", config.SocketPath, DefaultDarwinSocketPath)
	}
	if config.MaxConnections != defaultDarwinMaxConnections {
		t.Fatalf("MaxConnections = %d, want %d", config.MaxConnections, defaultDarwinMaxConnections)
	}
	if config.HandshakeTimeout != defaultDarwinHandshakeTimeout {
		t.Fatalf("HandshakeTimeout = %s, want %s", config.HandshakeTimeout, defaultDarwinHandshakeTimeout)
	}
	if config.IdleTimeout != defaultDarwinIdleTimeout {
		t.Fatalf("IdleTimeout = %s, want %s", config.IdleTimeout, defaultDarwinIdleTimeout)
	}
}

func TestDarwinTransportConfigPreservesValidValues(t *testing.T) {
	input := DarwinTransportConfig{
		SocketPath:       "/tmp/wireztna-test.sock",
		OwnerUID:         501,
		MaxConnections:   7,
		HandshakeTimeout: 3 * time.Second,
		IdleTimeout:      30 * time.Second,
	}
	actual, err := input.normalized()
	if err != nil {
		t.Fatalf("normalize valid config: %v", err)
	}
	if actual != input {
		t.Fatalf("normalized config = %#v, want %#v", actual, input)
	}
}

func TestDarwinTransportConfigRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name   string
		config DarwinTransportConfig
	}{
		{name: "relative path", config: DarwinTransportConfig{SocketPath: "desktop.sock"}},
		{name: "unclean path", config: DarwinTransportConfig{SocketPath: "/tmp/../tmp/desktop.sock"}},
		{name: "NUL path", config: DarwinTransportConfig{SocketPath: "/tmp/desktop\x00.sock"}},
		{name: "long path", config: DarwinTransportConfig{SocketPath: "/" + strings.Repeat("a", maximumDarwinSocketPathBytes)}},
		{name: "UID overflow", config: DarwinTransportConfig{OwnerUID: uint64(math.MaxUint32) + 1}},
		{name: "negative connection limit", config: DarwinTransportConfig{MaxConnections: -1}},
		{name: "excessive connection limit", config: DarwinTransportConfig{MaxConnections: maximumDarwinMaxConnections + 1}},
		{name: "negative handshake timeout", config: DarwinTransportConfig{HandshakeTimeout: -time.Second}},
		{name: "excessive handshake timeout", config: DarwinTransportConfig{HandshakeTimeout: maximumDarwinHandshakeTimeout + time.Second}},
		{name: "negative idle timeout", config: DarwinTransportConfig{IdleTimeout: -time.Second}},
		{name: "excessive idle timeout", config: DarwinTransportConfig{IdleTimeout: maximumDarwinIdleTimeout + time.Second}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.config.normalized(); err == nil {
				t.Fatal("normalized unsafe config without error")
			}
		})
	}
}

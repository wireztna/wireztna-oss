package relay

import (
	"testing"
)

func TestValidateLocalPort(t *testing.T) {
	tests := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{"valid port 8080", 8080, false},
		{"valid port 1", 1, false},
		{"valid port 65535", 65535, false},
		{"invalid port 0", 0, true},
		{"invalid port -1", -1, true},
		{"invalid port 65536", 65536, true},
		{"invalid port 100000", 100000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLocalPort(tt.port)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateLocalPort(%d) error = %v, wantErr %v", tt.port, err, tt.wantErr)
			}
		})
	}
}

func TestValidateBindAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{"localhost with port", "127.0.0.1:8080", false},
		{"localhost no port", "127.0.0.1", false},
		{"localhost name", "localhost", false},
		{"ipv6 loopback", "::1", false},

		// Should reject non-loopback
		{"external IP", "0.0.0.0:8080", true},
		{"external IP no port", "0.0.0.0", true},
		{"specific external", "192.168.1.1:8080", true},
		{"all interfaces ipv6", "::", true},
		{"public IP", "203.0.113.10", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBindAddress(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBindAddress(%q) error = %v, wantErr %v", tt.addr, err, tt.wantErr)
			}
		})
	}
}

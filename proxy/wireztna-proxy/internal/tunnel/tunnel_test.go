package tunnel

import (
	"testing"
)

func TestCloseCodeExitCode(t *testing.T) {
	tests := []struct {
		name     string
		code     CloseCode
		wantExit int
	}{
		{"normal close", CloseNormal, 0},
		{"expired", CloseExpired, 0},
		{"revoked", CloseRevoked, 2},
		{"invalid pass", CloseInvalidPass, 2},
		{"unreachable", CloseUnreachable, 3},
		{"max connections", CloseMaxConns, 1},
		{"unknown code", CloseCode(5000), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.code.ExitCode()
			if got != tt.wantExit {
				t.Errorf("CloseCode(%d).ExitCode() = %d, want %d", tt.code, got, tt.wantExit)
			}
		})
	}
}

func TestTunnelErrorFormat(t *testing.T) {
	err := &TunnelError{
		Code:    CloseExpired,
		Message: "pass expired",
	}

	expected := "tunnel error (code 4001): pass expired"
	if err.Error() != expected {
		t.Errorf("TunnelError.Error() = %q, want %q", err.Error(), expected)
	}
}

func TestConnectInvalidURL(t *testing.T) {
	// Test with a completely invalid URL
	cfg := Config{
		BrokerURL: "://invalid",
		PassID:    "dap_test01",
	}

	_, err := Connect(cfg)
	if err == nil {
		t.Error("Expected error for invalid URL, got nil")
	}
}

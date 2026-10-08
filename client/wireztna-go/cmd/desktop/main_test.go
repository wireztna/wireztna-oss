package main

import (
	"testing"
	"time"

	"github.com/wireztna/client/internal/ipc"
)

func TestClassifyTrayPresentation(t *testing.T) {
	now := time.Date(2026, time.September, 12, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		state       *ipc.State
		wantVisual  trayVisualState
		wantTooltip string
	}{
		{
			name:        "startup before first status is attention",
			state:       nil,
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Checking status...",
		},
		{
			name:        "explicit unknown status is attention",
			state:       &ipc.State{Status: trayStatusUnknown},
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Checking status...",
		},
		{
			name:        "stable disconnected is gray",
			state:       &ipc.State{Status: ipc.StatusDisconnected},
			wantVisual:  trayVisualDisconnected,
			wantTooltip: "WireZTNA — Disconnected",
		},
		{
			name:        "disconnected requiring enrollment is attention",
			state:       &ipc.State{Status: ipc.StatusDisconnected, NeedsEnroll: true},
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Setup required",
		},
		{
			name:        "disconnected requiring login is attention",
			state:       &ipc.State{Status: ipc.StatusDisconnected, NeedsLogin: true},
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Sign in required",
		},
		{
			name:        "disconnected with error is attention",
			state:       &ipc.State{Status: ipc.StatusDisconnected, ErrorMessage: "authentication failed"},
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Attention required",
		},
		{
			name:        "connecting is attention",
			state:       &ipc.State{Status: ipc.StatusConnecting},
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Connecting...",
		},
		{
			name:        "reconnecting is attention",
			state:       &ipc.State{Status: ipc.StatusReconnecting},
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Connecting...",
		},
		{
			name:        "service unavailable is attention",
			state:       &ipc.State{Status: trayStatusServiceUnavailable},
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Service unavailable",
		},
		{
			name:        "partial connected update without handshake is attention",
			state:       &ipc.State{Status: ipc.StatusConnected},
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Checking tunnel health...",
		},
		{
			name: "connected with future handshake is attention",
			state: &ipc.State{
				Status:        ipc.StatusConnected,
				LastHandshake: now.Add(time.Nanosecond),
			},
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Tunnel health unavailable",
		},
		{
			name: "connected with stale handshake is attention",
			state: &ipc.State{
				Status:        ipc.StatusConnected,
				LastHandshake: now.Add(-maxHealthyHandshakeAge - time.Nanosecond),
			},
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Tunnel handshake stale",
		},
		{
			name: "connected at exact handshake age boundary is green",
			state: &ipc.State{
				Status:        ipc.StatusConnected,
				LastHandshake: now.Add(-maxHealthyHandshakeAge),
			},
			wantVisual:  trayVisualConnected,
			wantTooltip: "WireZTNA — Connected",
		},
		{
			name: "connected with recent handshake is green",
			state: &ipc.State{
				Status:        ipc.StatusConnected,
				LastHandshake: now.Add(-time.Minute),
				OverlayIP:     "10.200.0.42",
			},
			wantVisual:  trayVisualConnected,
			wantTooltip: "WireZTNA — Connected — 10.200.0.42",
		},
		{
			name: "connected with service error is attention",
			state: &ipc.State{
				Status:        ipc.StatusConnected,
				LastHandshake: now.Add(-time.Minute),
				ErrorMessage:  "tunnel degraded",
			},
			wantVisual:  trayVisualAttention,
			wantTooltip: "WireZTNA — Attention required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyTrayPresentation(tt.state, now)
			if got.visual != tt.wantVisual {
				t.Fatalf("visual = %v, want %v", got.visual, tt.wantVisual)
			}
			if got.tooltip != tt.wantTooltip {
				t.Fatalf("tooltip = %q, want %q", got.tooltip, tt.wantTooltip)
			}
			if got.statusText == "" {
				t.Fatal("statusText must not be empty")
			}
		})
	}
}

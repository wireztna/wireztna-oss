// Package ipc defines the protocol and transport for communication between
// the wireztna-desktop (tray) app and the wireztna-service (privileged daemon).
//
// On Windows: uses a named pipe (\\.\pipe\wireztna)
// On macOS/Linux: uses a unix domain socket (/var/run/wireztna.sock)
//
// The protocol is simple JSON request/response over the pipe/socket.
package ipc

import "time"

// Command types sent from the tray app to the service.
const (
	CmdConnect    = "connect"
	CmdDisconnect = "disconnect"
	CmdStatus     = "status"
	CmdSwitch     = "switch"
	CmdGroups     = "groups"
	CmdExitNode   = "exit_node" // Connect via a specific exit node (full tunnel / VPN mode)
	CmdQuit       = "quit"

	// Enrollment & authentication (GUI wizard flow)
	CmdEnroll    = "enroll"     // Enroll this device with an enrollment URL
	CmdLogin     = "login"      // Request OTP code (sends email)
	CmdOTPVerify = "otp_verify" // Verify OTP code and get JWT
)

// Connection states reported by the service.
const (
	StatusDisconnected = "disconnected"
	StatusConnecting   = "connecting"
	StatusConnected    = "connected"
	StatusReconnecting = "reconnecting"
	StatusError        = "error"
)

// Request is sent from the tray app to the service.
type Request struct {
	Command    string `json:"command"`
	GroupID    string `json:"group_id,omitempty"`
	ExitNodeID string `json:"exit_node_id,omitempty"` // Publisher ID for VPN/exit node mode
	EnrollURL  string `json:"enroll_url,omitempty"`   // Full enrollment URL for CmdEnroll
	OTPCode    string `json:"otp_code,omitempty"`     // 6-digit code for CmdOTPVerify
}

// Response is sent from the service back to the tray app.
type Response struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Data    *State `json:"data,omitempty"`
}

// State is the current connection state reported by the service.
type State struct {
	Status        string    `json:"status"`
	OverlayIP     string    `json:"overlay_ip,omitempty"`
	Endpoint      string    `json:"endpoint,omitempty"`
	LastHandshake time.Time `json:"last_handshake,omitempty"`
	RxBytes       int64     `json:"rx_bytes,omitempty"`
	TxBytes       int64     `json:"tx_bytes,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	GroupName     string    `json:"group_name,omitempty"`
	GroupID       string    `json:"group_id,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	Groups        []Group   `json:"groups,omitempty"`
	HasOverlap    bool      `json:"has_overlap,omitempty"`
	// VPN / Exit Node state
	IsExitNode     bool       `json:"is_exit_node,omitempty"`      // True if connected via exit node
	ExitNodeName   string     `json:"exit_node_name,omitempty"`    // Active exit node publisher name
	ExitNodeID     string     `json:"exit_node_id,omitempty"`      // Active exit node publisher ID
	ExitNodes      []ExitNode `json:"exit_nodes,omitempty"`        // Available exit node publishers
	VPNMode        bool       `json:"vpn_mode,omitempty"`          // User has vpn_mode enabled
	// Enrollment & auth state (for GUI wizard)
	NeedsEnroll bool   `json:"needs_enroll,omitempty"` // True if device not enrolled
	NeedsLogin  bool   `json:"needs_login,omitempty"`  // True if enrolled but no valid JWT
	Email       string `json:"email,omitempty"`         // Masked email for OTP display
}

// Group is a simplified group representation for the tray menu.
type Group struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	CIDRs []string `json:"cidrs,omitempty"`
}

// ExitNode represents a publisher available as a VPN exit location.
type ExitNode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location,omitempty"` // e.g., "Frankfurt, DE"
	Online   bool   `json:"online"`
}

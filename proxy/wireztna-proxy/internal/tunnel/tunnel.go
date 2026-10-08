// Package tunnel handles WebSocket connection to the broker and manages
// the tunnel lifecycle including reconnection logic.
package tunnel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// CloseCode represents WebSocket close codes from the broker.
type CloseCode int

const (
	CloseNormal        CloseCode = 1000 // Normal close (target TCP closed)
	CloseExpired       CloseCode = 4001 // Pass expired
	CloseRevoked       CloseCode = 4002 // Pass revoked
	CloseUnreachable   CloseCode = 4003 // Target unreachable
	CloseMaxConns      CloseCode = 4029 // Max connections reached
	CloseInvalidPass   CloseCode = 4000 // Pass not found / not valid
)

// ExitCode maps a close code to a process exit code.
func (c CloseCode) ExitCode() int {
	switch c {
	case CloseNormal, CloseExpired:
		return 0 // Normal termination
	case CloseRevoked, CloseInvalidPass:
		return 2 // Access denied
	case CloseUnreachable:
		return 3 // Target unreachable
	default:
		return 1 // Error
	}
}

// Config holds tunnel connection parameters.
type Config struct {
	BrokerURL string // WebSocket URL (wss://broker/api/v1/tunnel/{pass_id})
	PassID    string // Access pass ID
}

// Tunnel represents an active WebSocket connection to the broker.
type Tunnel struct {
	config Config
	conn   *websocket.Conn
	mu     sync.Mutex
	closed bool

	// firstMessage holds any data received during the initial handshake check.
	// If non-nil, the relay should deliver this before reading more from the WS.
	firstMessage []byte

	// ExpiresAt is set from the broker's response headers if available.
	ExpiresAt time.Time
}

// Connect establishes a WebSocket connection to the broker tunnel endpoint.
// Returns the connected tunnel or an error with the appropriate close code.
func Connect(cfg Config) (*Tunnel, error) {
	wsURL := cfg.BrokerURL
	if cfg.PassID != "" && cfg.BrokerURL != "" {
		// Build URL from components
		u, err := url.Parse(cfg.BrokerURL)
		if err != nil {
			return nil, fmt.Errorf("invalid broker URL: %w", err)
		}
		u.Path = fmt.Sprintf("/api/v1/tunnel/%s", cfg.PassID)
		// Ensure WebSocket scheme
		if u.Scheme == "https" {
			u.Scheme = "wss"
		} else if u.Scheme == "http" {
			u.Scheme = "ws"
		}
		wsURL = u.String()
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}

	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			// Try to read the response body for a more specific error message
			reason := ""
			if resp.Body != nil {
				bodyBytes, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if len(bodyBytes) > 0 {
					reason = strings.TrimSpace(string(bodyBytes))
					// Clean up common Starlette WebSocket rejection format
					reason = strings.Trim(reason, "\"")
				}
			}

			switch resp.StatusCode {
			case http.StatusForbidden:
				// Differentiate based on reason from the broker
				if strings.Contains(reason, "unreachable") || strings.Contains(reason, "Unreachable") {
					return nil, &TunnelError{
						Code:    CloseUnreachable,
						Message: "target unreachable: the publisher cannot reach the specified host:port",
					}
				}
				if strings.Contains(reason, "expired") || strings.Contains(reason, "Expired") {
					return nil, &TunnelError{
						Code:    CloseInvalidPass,
						Message: "access denied: pass expired",
					}
				}
				if strings.Contains(reason, "not active") {
					return nil, &TunnelError{
						Code:    CloseInvalidPass,
						Message: "access denied: pass not active (may be revoked or already used)",
					}
				}
				if strings.Contains(reason, "not found") || strings.Contains(reason, "Not found") {
					return nil, &TunnelError{
						Code:    CloseInvalidPass,
						Message: "access denied: pass not found",
					}
				}
				return nil, &TunnelError{
					Code:    CloseInvalidPass,
					Message: "access denied: invalid or expired pass",
				}
			case http.StatusTooManyRequests:
				return nil, &TunnelError{
					Code:    CloseMaxConns,
					Message: "maximum connections reached for this pass",
				}
			default:
				msg := fmt.Sprintf("broker returned HTTP %d", resp.StatusCode)
				if reason != "" {
					msg = fmt.Sprintf("broker error (%d): %s", resp.StatusCode, reason)
				}
				return nil, &TunnelError{
					Code:    CloseCode(resp.StatusCode),
					Message: msg,
				}
			}
		}
		return nil, fmt.Errorf("websocket dial failed: %w", err)
	}

	t := &Tunnel{
		config: cfg,
		conn:   conn,
	}

	// Check for immediate error message from broker.
	// If the broker couldn't set up the tunnel (target unreachable, pass invalid, etc.),
	// it accepts the WS and sends a JSON error message before closing.
	// The broker's TCP connect timeout is 10s, so we wait up to 15s.
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, msg, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{}) // Reset deadline

	if err != nil {
		// If we got a close error, parse the close code
		if closeErr, ok := err.(*websocket.CloseError); ok {
			switch CloseCode(closeErr.Code) {
			case CloseUnreachable:
				return nil, &TunnelError{
					Code:    CloseUnreachable,
					Message: "target unreachable: the publisher cannot reach the specified host:port",
				}
			case CloseInvalidPass:
				return nil, &TunnelError{
					Code:    CloseInvalidPass,
					Message: "access denied: " + closeErr.Text,
				}
			case CloseExpired:
				return nil, &TunnelError{
					Code:    CloseExpired,
					Message: "access denied: pass expired",
				}
			case CloseMaxConns:
				return nil, &TunnelError{
					Code:    CloseMaxConns,
					Message: "maximum connections reached for this pass",
				}
			default:
				return nil, &TunnelError{
					Code:    CloseCode(closeErr.Code),
					Message: closeErr.Text,
				}
			}
		}
		// Timeout on read = no error message = broker is ready for relay
		// (this is the normal path — the broker accepted and is waiting for TCP data)
		if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
			// Normal — no initial message, broker is ready
			return t, nil
		}
		// Other read error
		conn.Close()
		return nil, fmt.Errorf("connection error: %w", err)
	}

	// Got a message — check if it's a JSON error or ready signal
	var errMsg struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(msg, &errMsg) == nil {
		// Ready signal — tunnel is connected, proceed to relay
		if errMsg.Status == "ready" {
			return t, nil
		}
		// Error message
		if errMsg.Error != "" {
			conn.Close()
			switch errMsg.Error {
			case "target_unreachable":
				return nil, &TunnelError{
					Code:    CloseUnreachable,
					Message: errMsg.Message,
				}
			case "pass_not_found":
				return nil, &TunnelError{
					Code:    CloseInvalidPass,
					Message: "pass not found",
				}
			case "pass_not_active":
				return nil, &TunnelError{
					Code:    CloseInvalidPass,
					Message: errMsg.Message,
				}
			case "pass_expired":
				return nil, &TunnelError{
					Code:    CloseExpired,
					Message: "pass expired",
				}
			case "max_connections":
				return nil, &TunnelError{
					Code:    CloseMaxConns,
					Message: "maximum connections reached",
				}
			case "tunnel_error":
				return nil, &TunnelError{
					Code:    CloseUnreachable,
					Message: errMsg.Message,
				}
			default:
				return nil, &TunnelError{
					Code:    CloseUnreachable,
					Message: errMsg.Message,
				}
			}
		}
	}

	// Not an error message — it's actual relay data (shouldn't happen, but handle gracefully)
	// Store it for the relay to consume
	t.firstMessage = msg

	return t, nil
}

// ConnectWithRetry attempts to connect with exponential backoff.
// Retries up to maxRetries times with 5s base interval.
func ConnectWithRetry(cfg Config, maxRetries int) (*Tunnel, error) {
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt) * 5 * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			time.Sleep(backoff)
		}

		t, err := Connect(cfg)
		if err == nil {
			return t, nil
		}

		lastErr = err

		// Don't retry on permanent errors (invalid pass, revoked, etc.)
		if te, ok := err.(*TunnelError); ok {
			switch te.Code {
			case CloseInvalidPass, CloseRevoked, CloseExpired:
				return nil, err // Permanent failure, don't retry
			}
		}
	}

	return nil, fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// ReadMessage reads a binary message from the WebSocket.
// Returns the message bytes or an error. On close, returns a TunnelError
// with the close code.
func (t *Tunnel) ReadMessage() ([]byte, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, fmt.Errorf("tunnel closed")
	}
	conn := t.conn
	t.mu.Unlock()

	_, msg, err := conn.ReadMessage()
	if err != nil {
		if closeErr, ok := err.(*websocket.CloseError); ok {
			return nil, &TunnelError{
				Code:    CloseCode(closeErr.Code),
				Message: closeErr.Text,
			}
		}
		return nil, err
	}
	return msg, nil
}

// WriteMessage sends a binary message through the WebSocket.
func (t *Tunnel) WriteMessage(data []byte) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("tunnel closed")
	}
	conn := t.conn
	t.mu.Unlock()

	return conn.WriteMessage(websocket.BinaryMessage, data)
}

// Close gracefully closes the WebSocket connection.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true

	if t.conn != nil {
		// Send close message
		_ = t.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		return t.conn.Close()
	}
	return nil
}

// TunnelError represents a tunnel-specific error with a close code.
type TunnelError struct {
	Code    CloseCode
	Message string
}

func (e *TunnelError) Error() string {
	return fmt.Sprintf("tunnel error (code %d): %s", e.Code, e.Message)
}

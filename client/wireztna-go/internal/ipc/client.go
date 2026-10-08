package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Client sends commands to the wireztna service via IPC.
type Client struct{}

// NewClient creates a new IPC client.
func NewClient() *Client {
	return &Client{}
}

// Send sends a request to the service and returns the response.
// The deadline is command-aware: heavy commands (connect, exit_node, switch,
// enroll) that perform API calls + tunnel creation get 60s; lightweight
// commands (status, groups, disconnect, etc.) keep the original 10s to avoid
// freezing the GUI if the service is truly unresponsive.
func (c *Client) Send(req Request) (*Response, error) {
	conn, err := Connect()
	if err != nil {
		return nil, fmt.Errorf("cannot connect to wireztna service: %w", err)
	}
	defer conn.Close()

	// Set deadline for the entire request/response exchange
	_ = conn.SetDeadline(time.Now().Add(deadlineForCommand(req.Command)))

	return sendRequest(conn, req)
}

// deadlineForCommand returns a generous deadline for commands that perform
// network I/O + tunnel creation (API round-trips, wintun adapter, route
// table manipulation), and a short deadline for everything else.
func deadlineForCommand(cmd string) time.Duration {
	switch cmd {
	case CmdConnect, CmdExitNode, CmdSwitch, CmdEnroll:
		return 30 * time.Second
	default:
		return 10 * time.Second
	}
}

// SendStatus is a convenience method to get the current state.
func (c *Client) SendStatus() (*State, error) {
	resp, err := c.Send(Request{Command: CmdStatus})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("service error: %s", resp.Error)
	}
	return resp.Data, nil
}

// SendConnect asks the service to connect, optionally with a specific group.
func (c *Client) SendConnect(groupID string) error {
	resp, err := c.Send(Request{Command: CmdConnect, GroupID: groupID})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// SendDisconnect asks the service to disconnect.
func (c *Client) SendDisconnect() error {
	resp, err := c.Send(Request{Command: CmdDisconnect})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// SendSwitch asks the service to switch to a different group.
func (c *Client) SendSwitch(groupID string) error {
	resp, err := c.Send(Request{Command: CmdSwitch, GroupID: groupID})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// SendGroups fetches the available groups from the service.
func (c *Client) SendGroups() (*State, error) {
	resp, err := c.Send(Request{Command: CmdGroups})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("service error: %s", resp.Error)
	}
	return resp.Data, nil
}

// SendExitNode asks the service to connect via a specific exit node (VPN mode).
// Pass empty string to disconnect from exit node and return to split tunnel.
func (c *Client) SendExitNode(exitNodeID string) error {
	resp, err := c.Send(Request{Command: CmdExitNode, ExitNodeID: exitNodeID})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// SendEnroll asks the service to enroll this device using an enrollment URL.
func (c *Client) SendEnroll(enrollURL string) error {
	resp, err := c.Send(Request{Command: CmdEnroll, EnrollURL: enrollURL})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// SendLogin asks the service to send an OTP code to the user's email.
func (c *Client) SendLogin() (*State, error) {
	resp, err := c.Send(Request{Command: CmdLogin})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return resp.Data, nil
}

// SendOTPVerify asks the service to verify the OTP code and store the JWT.
func (c *Client) SendOTPVerify(code string) error {
	resp, err := c.Send(Request{Command: CmdOTPVerify, OTPCode: code})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// sendRequest encodes the request, sends it, and decodes the response.
func sendRequest(conn net.Conn, req Request) (*Response, error) {
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(req); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	var resp Response
	decoder := json.NewDecoder(conn)
	if err := decoder.Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return &resp, nil
}

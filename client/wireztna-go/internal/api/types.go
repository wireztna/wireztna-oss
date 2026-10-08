package api

import (
	"fmt"
	"strings"
	"time"
)

// FlexTime is a time.Time that handles multiple datetime formats from the API.
// Python's FastAPI may return timestamps with or without timezone suffix.
type FlexTime struct {
	time.Time
}

func (ft *FlexTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}

	// Try formats in order of likelihood
	formats := []string{
		time.RFC3339Nano,             // 2006-01-02T15:04:05.999999999Z07:00
		time.RFC3339,                 // 2006-01-02T15:04:05Z07:00
		"2006-01-02T15:04:05.999999", // Python datetime without TZ
		"2006-01-02T15:04:05",        // Python datetime without TZ (no fractional)
		"2006-01-02 15:04:05",        // Space-separated
	}

	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			ft.Time = t
			return nil
		}
	}

	return fmt.Errorf("cannot parse time %q", s)
}

// SessionResponse is the response from POST /sessions/renew.
type SessionResponse struct {
	SessionID      string   `json:"session_id"`
	PresharedKey   string   `json:"preshared_key"`
	ExpiresAt      FlexTime `json:"expires_at"`
	TTLSeconds     int      `json:"ttl_seconds"`
	AllowedIPs     []string `json:"allowed_ips"`
	IsExitNode     bool     `json:"is_exit_node"`
	BrokerEndpoint string   `json:"broker_endpoint,omitempty"`
}

// SessionRenewRequest is the optional body sent on POST /sessions/renew.
type SessionRenewRequest struct {
	ClientVersion string `json:"client_version,omitempty"`
	WGVersion     string `json:"wg_version,omitempty"`
	Platform      string `json:"platform,omitempty"` // e.g. "darwin/arm64", "windows/amd64", "linux/amd64"
}

// SessionInfoResponse is the response from GET /sessions/me.
type SessionInfoResponse struct {
	SessionID         string   `json:"session_id"`
	ExpiresAt         FlexTime `json:"expires_at"`
	TTLRemaining      int      `json:"ttl_remaining_seconds"`
	ClientIP          string   `json:"client_ip"`
	SelectedGroupID   string   `json:"selected_group_id"`
	SelectedGroupName string   `json:"selected_group_name"`
}

// AvailableGroupsResponse is the response from GET /sessions/available-groups.
type AvailableGroupsResponse struct {
	Groups               []GroupInfo     `json:"groups"`
	HasOverlap           bool            `json:"has_overlap"`
	OverlapDetails       []OverlapDetail `json:"overlap_details"`
	SelectionRecommended bool            `json:"selection_recommended"`
	AllowAll             bool            `json:"allow_all"`
	ExitNodes            []ExitNodeInfo  `json:"exit_nodes"`
	VPNMode              bool            `json:"vpn_mode"`
}

// ExitNodeInfo represents a publisher available as a VPN exit node.
type ExitNodeInfo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Location       string `json:"location"`
	Status         string `json:"status"`
	PublisherIndex *int   `json:"publisher_index"`
}

// GroupInfo represents a group with its publishers and CIDRs.
type GroupInfo struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Publishers       []PublisherInfo `json:"publishers"`
	OnlinePublishers int             `json:"online_publishers"`
	CIDRs            []string        `json:"cidrs"`
}

// PublisherInfo is a publisher within a group.
type PublisherInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	ExposedCIDRs []string `json:"exposed_cidrs"`
}

// OverlapDetail describes overlapping CIDRs between groups.
type OverlapDetail struct {
	Groups           []string `json:"groups"`
	OverlappingCIDRs []string `json:"overlapping_cidrs"`
}

// OIDCConfigResponse is the response from GET /auth/oidc/config.
type OIDCConfigResponse struct {
	Enabled     bool   `json:"enabled"`
	ProviderURL string `json:"provider_url,omitempty"`
	LoginURL    string `json:"login_url,omitempty"`
}

// OIDCDeviceStartResponse is the response from POST /auth/oidc/device.
type OIDCDeviceStartResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Message                 string `json:"message,omitempty"`
}

// OIDCDevicePollResponse is the response from POST /auth/oidc/device/poll.
type OIDCDevicePollResponse struct {
	Status      string `json:"status"` // "pending", "completed", "expired", "denied"
	AccessToken string `json:"access_token,omitempty"`
	TokenType   string `json:"token_type,omitempty"`
	Username    string `json:"username,omitempty"`
}

// OTPRequestResponse is the response from POST /auth/otp/request.
type OTPRequestResponse struct {
	Message   string `json:"message"`
	EmailHint string `json:"email_hint,omitempty"` // Masked email, e.g., "s***o@company.com"
	ExpiresIn int    `json:"expires_in"`           // Seconds until code expires
}

// OTPVerifyResponse is the response from POST /auth/otp/verify (same as login).
type OTPVerifyResponse struct {
	AccessToken            string `json:"access_token"`
	TokenType              string `json:"token_type"`
	MFARequired            bool   `json:"mfa_required"`
	PasswordChangeRequired bool   `json:"password_change_required"`
}

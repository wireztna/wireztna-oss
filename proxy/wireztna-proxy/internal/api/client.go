// Package api provides an HTTP client for the WireZTNA Control Plane API.
// Used by the register and connect subcommands to interact with the API
// without requiring the user to manually create passes via the portal.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is a simple HTTP client for the WireZTNA API.
type Client struct {
	BaseURL    string // e.g., "https://tenant.wireztna.com"
	Token      string // API key (wzk_...) or JWT
	HTTPClient *http.Client
}

// New creates a new API client.
func New(brokerURL string, token string) *Client {
	// Normalize broker URL
	u, err := url.Parse(brokerURL)
	if err == nil {
		// Ensure https scheme
		if u.Scheme == "" {
			u.Scheme = "https"
		}
		// Remove any path (we'll add our own)
		u.Path = ""
		brokerURL = u.String()
	}

	return &Client{
		BaseURL: brokerURL,
		Token:   token,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// RegisterRequest sends OTP to the provided email.
func (c *Client) RegisterRequest(email string) error {
	body := map[string]string{"email": email}
	resp, err := c.post("/api/v1/auth/register", body, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return c.parseError(resp)
	}
	return nil
}

// RegisterVerifyResponse is returned on successful registration.
type RegisterVerifyResponse struct {
	APIKey string `json:"api_key"`
	UserID string `json:"user_id"`
	Plan   string `json:"plan"`
}

// RegisterVerify completes registration with the OTP code.
func (c *Client) RegisterVerify(email, code string) (*RegisterVerifyResponse, error) {
	body := map[string]string{"email": email, "code": code}
	resp, err := c.post("/api/v1/auth/register/verify", body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 409 {
		return nil, fmt.Errorf("email already registered")
	}
	if resp.StatusCode == 401 {
		return nil, c.parseError(resp)
	}
	if resp.StatusCode != 201 {
		return nil, c.parseError(resp)
	}

	var result RegisterVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &result, nil
}

// CreatePassRequest is the request body for creating an access pass.
type CreatePassRequest struct {
	Label      string    `json:"label"`
	Scope      PassScope `json:"scope"`
	TTLSeconds int       `json:"ttl_seconds"`
}

// PassScope defines the scope of an access pass.
type PassScope struct {
	Publishers []string `json:"publishers"`
	CIDRs      []string `json:"cidrs"`
	Ports      []int    `json:"ports"`
}

// CreatePassResponse is returned when a pass is created.
type CreatePassResponse struct {
	PassID               string `json:"pass_id"`
	ConnectionURL        string `json:"connection_url"`
	TimeRemainingSeconds int    `json:"time_remaining_seconds"`
}

// CreateTokenResponse is returned when an enrollment token is created.
type CreateTokenResponse struct {
	ID       string `json:"id"`
	Token    string `json:"token"`     // Full enrollment URL (client tokens)
	TokenURL string `json:"token_url"` // Full enrollment URL (publisher tokens)
}

// CreatePublisherToken creates a publisher enrollment token via /me/publisher-tokens.
func (c *Client) CreatePublisherToken(name string, cidrs []string) (*CreateTokenResponse, error) {
	body := map[string]interface{}{}
	if name != "" {
		body["name"] = name
	}
	if len(cidrs) > 0 {
		body["exposed_cidrs"] = cidrs
	}

	resp, err := c.post("/api/v1/me/publisher-tokens", body, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		return nil, c.parseError(resp)
	}

	var result CreateTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &result, nil
}

// CreatePass creates a new access pass via the API.
func (c *Client) CreatePass(req CreatePassRequest) (*CreatePassResponse, error) {
	resp, err := c.post("/api/v1/access-passes", req, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		return nil, c.parseError(resp)
	}

	var result CreatePassResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &result, nil
}

// post sends a POST request with JSON body.
func (c *Client) post(path string, body interface{}, auth bool) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	reqURL := c.BaseURL + path
	req, err := http.NewRequest("POST", reqURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if auth && c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	return c.HTTPClient.Do(req)
}

// parseError extracts an error message from an API error response.
func (c *Client) parseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)

	var errResp struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Detail != "" {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Detail)
	}
	return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
}

// PublisherInfo represents a publisher returned by the /me endpoint.
type PublisherInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	ExposedCIDRs []string `json:"exposed_cidrs"`
}

// meResponse is the (partial) response from GET /api/v1/me.
type meResponse struct {
	AccessiblePublishers []PublisherInfo `json:"accessible_publishers"`
}

// ListPublishers returns the publishers accessible to the authenticated user.
func (c *Client) ListPublishers() ([]PublisherInfo, error) {
	resp, err := c.get("/api/v1/me", true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, c.parseError(resp)
	}

	var result meResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return result.AccessiblePublishers, nil
}

// get sends an authenticated GET request.
func (c *Client) get(path string, auth bool) (*http.Response, error) {
	reqURL := c.BaseURL + path
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if auth && c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	return c.HTTPClient.Do(req)
}

// delete sends an authenticated DELETE request.
func (c *Client) delete(path string, auth bool) (*http.Response, error) {
	reqURL := c.BaseURL + path
	req, err := http.NewRequest("DELETE", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if auth && c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	return c.HTTPClient.Do(req)
}

// PassInfo represents an access pass returned by the API.
type PassInfo struct {
	PassID           string `json:"pass_id"`
	Label            string `json:"label"`
	Status           string `json:"status"`
	ScopeSummary     string `json:"scope_summary"`
	CreatedAt        string `json:"created_at"`
	ExpiresAt        string `json:"expires_at"`
	BytesUploaded    int64  `json:"bytes_uploaded"`
	BytesDownloaded  int64  `json:"bytes_downloaded"`
	ConnectionsCount int    `json:"connections_count"`
}

// ListPasses returns the access passes for the authenticated user.
func (c *Client) ListPasses() ([]PassInfo, error) {
	resp, err := c.get("/api/v1/access-passes", true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, c.parseError(resp)
	}

	var result []PassInfo
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return result, nil
}

// RevokePass revokes an active access pass.
func (c *Client) RevokePass(passID string) error {
	resp, err := c.delete("/api/v1/access-passes/"+passID, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return c.parseError(resp)
	}
	return nil
}

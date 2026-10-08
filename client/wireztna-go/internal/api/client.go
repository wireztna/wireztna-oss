// Package api implements the HTTP client for the WireZTNA control plane.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/pkg/version"
)

// Client communicates with the WireZTNA control plane API.
type Client struct {
	baseURL          string
	token            string
	httpClient       *http.Client
	mutationDir      string
	mutationOwnerUID *uint32
}

// ClientOption configures one API client without changing standalone defaults.
type ClientOption func(*Client)

// WithRemoteMutationOwner pins session-mutation locking to an explicit owner
// configuration directory. Managed services use this instead of HOME-derived
// resolution so they coordinate with the owner's direct clients.
func WithRemoteMutationOwner(configDir string, ownerUID uint32) ClientOption {
	return func(client *Client) {
		uid := ownerUID
		client.mutationDir = configDir
		client.mutationOwnerUID = &uid
	}
}

// detectWGVersion tries to get the WireGuard version from the system.
// Returns empty string if not detectable (non-fatal).
func detectWGVersion() string {
	out, err := exec.Command("wg", "--version").Output()
	if err != nil {
		return ""
	}
	// Output is like "wireguard-tools v1.0.20210914 - https://git.zx2c4.com/wireguard-tools/"
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) >= 2 {
		return strings.TrimPrefix(parts[1], "v")
	}
	return strings.TrimSpace(string(out))
}

// NewClient creates a new API client.
func NewClient(baseURL string, options ...ClientOption) *Client {
	parsedBase, parseErr := url.Parse(baseURL)
	client := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("stopped after 10 redirects")
				}
				if parseErr != nil || !sameEffectiveOrigin(request.URL, parsedBase) {
					return errors.New("refusing API redirect outside the configured origin")
				}
				return nil
			},
		},
	}
	for _, option := range options {
		if option != nil {
			option(client)
		}
	}
	return client
}

func sameEffectiveOrigin(candidate, configured *url.URL) bool {
	if candidate == nil || configured == nil || candidate.Scheme == "" || configured.Scheme == "" ||
		candidate.Host == "" || configured.Host == "" ||
		!strings.EqualFold(candidate.Scheme, configured.Scheme) ||
		!strings.EqualFold(candidate.Hostname(), configured.Hostname()) {
		return false
	}
	return effectiveOriginPort(candidate) == effectiveOriginPort(configured)
}

func effectiveOriginPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

// SetToken sets the JWT bearer token for authenticated requests.
func (c *Client) SetToken(token string) {
	c.token = token
}

// Login authenticates with email (or username) + password and returns a JWT token.
func (c *Client) Login(identifier, password string) (string, error) {
	payload := map[string]string{
		"email":    identifier,
		"password": password,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Post(
		c.baseURL+"/api/v1/auth/login",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("invalid response: %w", err)
	}

	return result.AccessToken, nil
}

const maxAuthenticationResponseBytes = 64 * 1024

// ErrMFARequired prevents callers from treating an MFA-pending token as a complete session.
var ErrMFARequired = errors.New("authenticator verification is required")

func decodeExactAuthenticationResponse(body io.Reader, allowed []string, target any) error {
	payload, err := io.ReadAll(io.LimitReader(body, maxAuthenticationResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxAuthenticationResponseBytes {
		return errors.New("authentication response exceeds size limit")
	}

	allowedFields := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedFields[field] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return errors.New("authentication response must be a JSON object")
	}
	seen := make(map[string]struct{}, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		field, ok := token.(string)
		if !ok {
			return errors.New("authentication response contains an invalid field")
		}
		if _, ok := allowedFields[field]; !ok {
			return fmt.Errorf("authentication response contains unknown field %q", field)
		}
		if _, duplicate := seen[field]; duplicate {
			return fmt.Errorf("authentication response contains duplicate field %q", field)
		}
		seen[field] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return errors.New("authentication response is not a complete JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("authentication response must contain exactly one JSON value")
	}
	return json.Unmarshal(payload, target)
}

func boundedAuthenticationError(body io.Reader) string {
	payload, _ := io.ReadAll(io.LimitReader(body, 4*1024))
	return string(payload)
}

// OTPRequest asks the server to send an OTP code to the user's registered email.
func (c *Client) OTPRequest(identifier string) (*OTPRequestResponse, error) {
	payload := map[string]string{"email": identifier}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Post(
		c.baseURL+"/api/v1/auth/otp/request",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, boundedAuthenticationError(resp.Body))
	}

	var result OTPRequestResponse
	if err := decodeExactAuthenticationResponse(resp.Body, []string{"message", "email_hint", "expires_in"}, &result); err != nil {
		return nil, fmt.Errorf("invalid response: %w", err)
	}
	if strings.TrimSpace(result.Message) == "" || result.ExpiresIn <= 0 {
		return nil, errors.New("invalid response: OTP request metadata is incomplete")
	}

	return &result, nil
}

// OTPVerify sends the OTP code for verification and returns a complete JWT session.
func (c *Client) OTPVerify(identifier, code string) (string, error) {
	result, err := c.otpVerifyDetailed(identifier, code)
	if err != nil {
		return "", err
	}
	if result.MFARequired {
		return "", ErrMFARequired
	}
	return result.AccessToken, nil
}

func (c *Client) otpVerifyDetailed(identifier, code string) (*OTPVerifyResponse, error) {
	payload := map[string]string{
		"email": identifier,
		"code":  code,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Post(
		c.baseURL+"/api/v1/auth/otp/verify",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, boundedAuthenticationError(resp.Body))
	}

	var result OTPVerifyResponse
	if err := decodeExactAuthenticationResponse(
		resp.Body,
		[]string{"access_token", "token_type", "mfa_required", "password_change_required"},
		&result,
	); err != nil {
		return nil, fmt.Errorf("invalid response: %w", err)
	}
	if result.AccessToken == "" || result.TokenType != "bearer" {
		return nil, errors.New("invalid response: authentication token is incomplete")
	}
	return &result, nil
}

type lifecycleGenerationContextKey struct{}

// WithLifecycleGeneration makes RenewSessionContext revalidate durable intent
// after acquiring the cross-process mutation lock and before issuing POST.
func WithLifecycleGeneration(ctx context.Context, generation uint64) context.Context {
	return context.WithValue(ctx, lifecycleGenerationContextKey{}, generation)
}

// RenewSession requests a new PSK session from the control plane.
// groupID filters by group (for CIDR overlap resolution).
// exitNodeID selects a publisher as VPN exit node (full tunnel mode).
func (c *Client) RenewSession(groupID string, exitNodeID ...string) (*SessionResponse, error) {
	return c.RenewSessionContext(context.Background(), groupID, exitNodeID...)
}

// RenewSessionContext is RenewSession with caller-controlled cancellation.
// Lifecycle owners use it so an explicit disconnect can abort an in-flight
// remote mutation before it applies or persists stale state.
func (c *Client) RenewSessionContext(ctx context.Context, groupID string, exitNodeID ...string) (*SessionResponse, error) {
	var releaseMutation func() error
	var err error
	if c.mutationOwnerUID != nil {
		releaseMutation, err = config.AcquireRemoteMutationForOwner(ctx, c.mutationDir, *c.mutationOwnerUID)
	} else {
		releaseMutation, err = config.AcquireRemoteMutation(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("serialize session renewal: %w", err)
	}
	defer releaseMutation()
	if generation, ok := ctx.Value(lifecycleGenerationContextKey{}).(uint64); ok && !config.IsLifecycleCurrent(generation) {
		return nil, config.ErrStaleLifecycle
	}

	url := c.baseURL + "/api/v1/sessions/renew"
	params := []string{}
	if groupID != "" {
		params = append(params, "group_id="+groupID)
	}
	if len(exitNodeID) > 0 && exitNodeID[0] != "" {
		params = append(params, "exit_node_id="+exitNodeID[0])
	}
	if len(params) > 0 {
		url += "?" + strings.Join(params, "&")
	}

	// Send version info in the request body (backwards-compatible: API accepts empty body too)
	body := SessionRenewRequest{
		ClientVersion: version.Version,
		WGVersion:     detectWGVersion(),
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
	}
	bodyJSON, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("token rejected (HTTP %d) — re-authenticate with 'wireztna login'", resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	var session SessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, fmt.Errorf("invalid response: %w", err)
	}

	return &session, nil
}

// GetSessionInfo returns info about the current active session.
func (c *Client) GetSessionInfo() (*SessionInfoResponse, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/sessions/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var info SessionInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}

	return &info, nil
}

// GetAvailableGroups returns groups available to the user with overlap detection.
func (c *Client) GetAvailableGroups() (*AvailableGroupsResponse, error) {
	return c.GetAvailableGroupsContext(context.Background())
}

// GetAvailableGroupsContext is GetAvailableGroups with caller cancellation.
func (c *Client) GetAvailableGroupsContext(ctx context.Context) (*AvailableGroupsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/v1/sessions/available-groups", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var groups AvailableGroupsResponse
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return nil, err
	}

	return &groups, nil
}

// GetDNSZones returns the DNS zones the user can access through the tunnel.
func (c *Client) GetDNSZones() ([]string, error) {
	return c.GetDNSZonesContext(context.Background())
}

// GetDNSZonesContext is GetDNSZones with caller-controlled cancellation.
func (c *Client) GetDNSZonesContext(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/v1/sessions/dns-zones", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result struct {
		Zones []string `json:"zones"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Zones, nil
}

// EnrollResponse is the response from POST /clients/enroll.
type EnrollResponse struct {
	Email           string   `json:"email"`
	Username        string   `json:"username"`
	OverlayIP       string   `json:"overlay_ip"`
	BrokerPublicKey string   `json:"broker_public_key"`
	BrokerEndpoint  string   `json:"broker_endpoint"`
	BrokerOverlayIP string   `json:"broker_overlay_ip"`
	AllowedIPs      []string `json:"allowed_ips"`
	DNSServer       string   `json:"dns_server"`
	EnrolledAt      string   `json:"enrolled_at"`
}

// Enroll sends the enrollment token and public key to the control plane,
// receiving back the full client configuration. No authentication required.
func (c *Client) Enroll(token, publicKey string) (*EnrollResponse, error) {
	hostname, _ := os.Hostname()
	payload := map[string]string{
		"token":       token,
		"public_key":  publicKey,
		"device_name": hostname,
		"platform":    runtime.GOOS + "/" + runtime.GOARCH,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Post(
		c.baseURL+"/api/v1/clients/enroll",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("enrollment rejected: %s", boundedAuthenticationError(resp.Body))
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, boundedAuthenticationError(resp.Body))
	}

	var result EnrollResponse
	if err := decodeExactAuthenticationResponse(resp.Body, []string{
		"email", "username", "overlay_ip", "broker_public_key", "broker_endpoint",
		"broker_overlay_ip", "allowed_ips", "dns_server", "enrolled_at",
	}, &result); err != nil {
		return nil, fmt.Errorf("invalid enrollment response: %w", err)
	}

	return &result, nil
}

// ─── OIDC / SSO Methods ───

// GetOIDCConfig checks if OIDC/SSO is enabled on the server.
func (c *Client) GetOIDCConfig() (*OIDCConfigResponse, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/api/v1/auth/oidc/config")
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Old server without OIDC support
		return &OIDCConfigResponse{Enabled: false}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result OIDCConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// OIDCDeviceStart initiates the device authorization flow.
func (c *Client) OIDCDeviceStart() (*OIDCDeviceStartResponse, error) {
	resp, err := c.httpClient.Post(
		c.baseURL+"/api/v1/auth/oidc/device",
		"application/json",
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	var result OIDCDeviceStartResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// OIDCDevicePoll polls for device authorization completion.
func (c *Client) OIDCDevicePoll(deviceCode string) (*OIDCDevicePollResponse, error) {
	payload := map[string]string{"device_code": deviceCode}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Post(
		c.baseURL+"/api/v1/auth/oidc/device/poll",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	var result OIDCDevicePollResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

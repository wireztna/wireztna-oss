package enrollment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/wireztna/publisher/internal/config"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// EnrollRequest is the JSON body sent to POST /api/v1/publishers/enroll
type EnrollRequest struct {
	Token     string `json:"token"`
	PublicKey string `json:"public_key"`
	Name      string `json:"name"`
	LocalDNS  string `json:"local_dns,omitempty"`
}

// EnrollResponse is the JSON response from the enrollment endpoint
type EnrollResponse struct {
	PublisherID       string `json:"publisher_id"`
	PublisherApiKey   string `json:"publisher_api_key"`
	BrokerPublicKey   string `json:"broker_public_key"`
	BrokerEndpoint    string `json:"broker_endpoint"`
	TunnelIP          string `json:"tunnel_ip"`
	BrokerTunnelIP    string `json:"broker_tunnel_ip"`
	AllowedIPs        string `json:"allowed_ips"`
	ConnectionInfoURL string `json:"connection_info_url"`
	PollForConnection bool   `json:"poll_for_connection"`
}

// ConnectionInfo is the response from GET /api/v1/publishers/{id}/connection-info
type ConnectionInfo struct {
	Ready          bool   `json:"ready"`
	BrokerPublicKey string `json:"broker_public_key"`
	BrokerEndpoint  string `json:"broker_endpoint"`
	BrokerTunnelIP  string `json:"broker_tunnel_ip"`
	TunnelIP        string `json:"tunnel_ip"`
	AllowedIPs      string `json:"allowed_ips"`
}

// ParseEnrollmentURL extracts the control plane URL and token from an enrollment URL
// Expected format: http://host/api/v1/publishers/enroll?token=abc123
// Or just: http://host/api/v1/clients/enroll?token=abc123 (publisher variant)
func ParseEnrollmentURL(enrollURL string) (controlPlaneURL string, token string, err error) {
	u, err := url.Parse(enrollURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid enrollment URL: %w", err)
	}

	token = u.Query().Get("token")
	if token == "" {
		return "", "", fmt.Errorf("enrollment URL missing 'token' parameter")
	}

	// Control plane URL is the scheme + host (without the path)
	controlPlaneURL = fmt.Sprintf("%s://%s", u.Scheme, u.Host)

	return controlPlaneURL, token, nil
}

// Enroll performs the full enrollment flow:
// 1. Generate WireGuard keypair
// 2. POST to enrollment endpoint
// 3. Poll for connection info if needed
// 4. Save state to disk
func Enroll(cfg *config.InstallConfig) (*config.PublisherState, string, error) {
	if config.IsEnrolled() {
		return nil, "", fmt.Errorf("publisher already enrolled. Use 'uninstall' first to re-enroll")
	}

	if err := config.EnsureConfigDir(); err != nil {
		return nil, "", fmt.Errorf("failed to create config directory: %w", err)
	}

	// 1. Generate WireGuard keypair
	fmt.Println("[*] Generating WireGuard keypair...")
	privateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate private key: %w", err)
	}
	publicKey := privateKey.PublicKey()

	// Save private key to disk
	if err := config.SavePrivateKey(privateKey.String()); err != nil {
		return nil, "", fmt.Errorf("failed to save private key: %w", err)
	}
	fmt.Printf("[✓] Public key: %s\n", publicKey.String())

	// 2. Auto-detect local DNS if not set
	localDNS := cfg.LocalDNS
	if localDNS == "" {
		localDNS = detectLocalDNS()
		if localDNS != "" {
			fmt.Printf("[*] Auto-detected local DNS: %s\n", localDNS)
		}
	}

	// 3. POST enrollment request
	fmt.Printf("[*] Enrolling with control plane at %s...\n", cfg.ControlPlaneURL)

	reqBody := EnrollRequest{
		Token:     cfg.EnrollmentToken,
		PublicKey: publicKey.String(),
		Name:      cfg.PublisherName,
		LocalDNS:  localDNS,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", fmt.Errorf("failed to marshal enrollment request: %w", err)
	}

	enrollEndpoint := fmt.Sprintf("%s/api/v1/publishers/enroll", cfg.ControlPlaneURL)
	resp, err := http.Post(enrollEndpoint, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, "", fmt.Errorf("failed to reach control plane: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return nil, "", fmt.Errorf("enrollment failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var enrollResp EnrollResponse
	if err := json.Unmarshal(respBody, &enrollResp); err != nil {
		return nil, "", fmt.Errorf("failed to parse enrollment response: %w", err)
	}

	fmt.Println("[✓] Enrollment successful")
	fmt.Printf("    Publisher ID:    %s\n", enrollResp.PublisherID)
	fmt.Printf("    Tunnel IP:       %s\n", enrollResp.TunnelIP)

	// 4. Poll for connection info if needed
	if enrollResp.PollForConnection && enrollResp.ConnectionInfoURL != "" {
		fmt.Println("[*] Waiting for broker to provision namespace...")
		connInfo, err := pollConnectionInfo(enrollResp.ConnectionInfoURL)
		if err != nil {
			return nil, "", err
		}
		enrollResp.BrokerPublicKey = connInfo.BrokerPublicKey
		enrollResp.BrokerEndpoint = connInfo.BrokerEndpoint
		enrollResp.BrokerTunnelIP = connInfo.BrokerTunnelIP
		enrollResp.TunnelIP = connInfo.TunnelIP
		if connInfo.AllowedIPs != "" {
			enrollResp.AllowedIPs = connInfo.AllowedIPs
		}
		fmt.Println("[✓] Broker namespace ready")
	}

	fmt.Printf("    Broker endpoint: %s\n", enrollResp.BrokerEndpoint)

	// Default allowed IPs if empty
	if enrollResp.AllowedIPs == "" {
		enrollResp.AllowedIPs = "10.200.0.0/16,10.100.0.0/16"
	}

	// 5. Save state
	state := &config.PublisherState{
		PublisherID:     enrollResp.PublisherID,
		PublisherApiKey: enrollResp.PublisherApiKey,
		EnrolledAt:      time.Now().UTC(),
		BrokerEndpoint:  enrollResp.BrokerEndpoint,
		BrokerPubKey:    enrollResp.BrokerPublicKey,
		TunnelIP:        enrollResp.TunnelIP,
		BrokerTunnelIP:  enrollResp.BrokerTunnelIP,
		AllowedIPs:      enrollResp.AllowedIPs,
		ControlPlaneURL: cfg.ControlPlaneURL,
		PublisherName:   cfg.PublisherName,
	}

	if err := config.SaveState(state); err != nil {
		return nil, "", fmt.Errorf("failed to save enrollment state: %w", err)
	}

	return state, privateKey.String(), nil
}

// pollConnectionInfo polls the connection-info endpoint until the broker namespace is ready
func pollConnectionInfo(infoURL string) (*ConnectionInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	maxAttempts := 60 // 5 minutes (60 * 5s)

	for i := 0; i < maxAttempts; i++ {
		resp, err := client.Get(infoURL)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var info ConnectionInfo
		if err := json.Unmarshal(body, &info); err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		if info.Ready {
			return &info, nil
		}

		time.Sleep(5 * time.Second)
	}

	return nil, fmt.Errorf("timed out waiting for broker namespace (5 minutes)")
}

// detectLocalDNS reads /etc/resolv.conf to find the system DNS resolver
func detectLocalDNS() string {
	// Try host resolv.conf first (if mounted)
	paths := []string{"/host/etc/resolv.conf", "/etc/resolv.conf"}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		for _, line := range bytes.Split(data, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, []byte("nameserver")) {
				fields := bytes.Fields(line)
				if len(fields) >= 2 {
					ns := string(fields[1])
					// Skip Docker internal DNS and localhost
					if ns != "127.0.0.11" && ns != "127.0.0.1" {
						return ns
					}
				}
			}
		}
	}

	return ""
}

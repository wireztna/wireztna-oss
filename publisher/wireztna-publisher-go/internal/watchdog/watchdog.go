package watchdog

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/wireztna/publisher/internal/config"
	"github.com/wireztna/publisher/internal/tunnel"
)

// Watchdog monitors the WireGuard tunnel and auto-recovers stale connections
type Watchdog struct {
	state              *config.PublisherState
	threshold          time.Duration
	interval           time.Duration
	cooldown           time.Duration
	lastRecovery       time.Time
	client             *http.Client
	stopCh             chan struct{}
}

// ConnectionInfo mirrors the API response for connection-info
type ConnectionInfo struct {
	Ready           bool   `json:"ready"`
	BrokerPublicKey string `json:"broker_public_key"`
	BrokerEndpoint  string `json:"broker_endpoint"`
	BrokerTunnelIP  string `json:"broker_tunnel_ip"`
	TunnelIP        string `json:"tunnel_ip"`
	AllowedIPs      string `json:"allowed_ips"`
}

// New creates a new watchdog
func New(state *config.PublisherState, thresholdSeconds int, intervalSeconds int) *Watchdog {
	return &Watchdog{
		state:     state,
		threshold: time.Duration(thresholdSeconds) * time.Second,
		interval:  time.Duration(intervalSeconds) * time.Second,
		cooldown:  60 * time.Second,
		client:    &http.Client{Timeout: 10 * time.Second},
		stopCh:    make(chan struct{}),
	}
}

// Start begins the watchdog loop (blocking)
func (w *Watchdog) Start() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.check()
		case <-w.stopCh:
			return
		}
	}
}

// Stop signals the watchdog to exit
func (w *Watchdog) Stop() {
	close(w.stopCh)
}

// check performs a single watchdog cycle
func (w *Watchdog) check() {
	status, err := tunnel.GetPeerStatus()
	if err != nil {
		return
	}

	// Tunnel is healthy
	if status.HasHandshake && status.HandshakeAge < w.threshold {
		return
	}

	// Cooldown: don't attempt recovery too frequently
	if time.Since(w.lastRecovery) < w.cooldown {
		return
	}
	w.lastRecovery = time.Now()

	handshakeStr := "never"
	if status.HasHandshake {
		handshakeStr = fmt.Sprintf("%ds", int(status.HandshakeAge.Seconds()))
	}
	fmt.Printf("[WATCHDOG] Handshake stale (%s > %ds) — checking for endpoint change...\n",
		handshakeStr, int(w.threshold.Seconds()))

	// Poll connection-info for potential config change
	connInfo, err := w.fetchConnectionInfo()
	if err != nil {
		fmt.Printf("[WATCHDOG] Failed to fetch connection-info: %v\n", err)
		w.bounceTunnel()
		return
	}

	if !connInfo.Ready {
		fmt.Println("[WATCHDOG] Broker namespace not ready — will retry next cycle")
		return
	}

	// Check if broker config has changed
	changed := false
	if connInfo.BrokerPublicKey != "" && connInfo.BrokerPublicKey != w.state.BrokerPubKey {
		fmt.Printf("[WATCHDOG] Broker public key changed: %s... -> %s...\n",
			truncate(w.state.BrokerPubKey, 12), truncate(connInfo.BrokerPublicKey, 12))
		changed = true
	}
	if connInfo.BrokerEndpoint != "" && connInfo.BrokerEndpoint != w.state.BrokerEndpoint {
		fmt.Printf("[WATCHDOG] Broker endpoint changed: %s -> %s\n",
			w.state.BrokerEndpoint, connInfo.BrokerEndpoint)
		changed = true
	}

	if changed {
		fmt.Println("[WATCHDOG] Applying new broker config...")
		allowedIPs := connInfo.AllowedIPs
		if allowedIPs == "" {
			allowedIPs = w.state.AllowedIPs
		}

		if err := tunnel.UpdatePeer(connInfo.BrokerPublicKey, connInfo.BrokerEndpoint, allowedIPs); err != nil {
			fmt.Printf("[WATCHDOG] Failed to update peer: %v\n", err)
			return
		}

		// Update persisted state
		w.state.BrokerPubKey = connInfo.BrokerPublicKey
		w.state.BrokerEndpoint = connInfo.BrokerEndpoint
		if connInfo.AllowedIPs != "" {
			w.state.AllowedIPs = connInfo.AllowedIPs
		}
		_ = config.SaveState(w.state)

		fmt.Println("[WATCHDOG] Recovery applied — waiting for handshake...")
	} else {
		// Config unchanged but tunnel stale — bounce the interface
		w.bounceTunnel()
	}
}

// bounceTunnel destroys and recreates the WireGuard interface
func (w *Watchdog) bounceTunnel() {
	fmt.Println("[WATCHDOG] Config unchanged but tunnel stale — bouncing interface...")

	tunnel.Destroy()
	time.Sleep(2 * time.Second)

	privateKey, err := config.LoadPrivateKey()
	if err != nil {
		fmt.Printf("[WATCHDOG] Failed to load private key for recovery: %v\n", err)
		return
	}

	if err := tunnel.Create(w.state, privateKey, config.DefaultWGPort); err != nil {
		fmt.Printf("[WATCHDOG] Failed to recreate tunnel: %v\n", err)
		return
	}

	fmt.Println("[WATCHDOG] Interface bounced")
}

// fetchConnectionInfo queries the broker for current connection parameters
func (w *Watchdog) fetchConnectionInfo() (*ConnectionInfo, error) {
	url := fmt.Sprintf("%s/api/v1/publishers/%s/connection-info",
		w.state.ControlPlaneURL, w.state.PublisherID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if w.state.PublisherApiKey != "" {
		req.Header.Set("X-API-Key", w.state.PublisherApiKey)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var info ConnectionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}

	return &info, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

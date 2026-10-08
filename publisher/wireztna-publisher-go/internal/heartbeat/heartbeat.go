package heartbeat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/wireztna/publisher/internal/config"
	"github.com/wireztna/publisher/internal/tunnel"
	"github.com/wireztna/publisher/pkg/version"
)

// HeartbeatPayload is the JSON body sent to POST /api/v1/publishers/{id}/heartbeat
type HeartbeatPayload struct {
	PublisherID       string  `json:"publisher_id"`
	Timestamp        string  `json:"timestamp"`
	UptimeSeconds    int64   `json:"uptime_seconds"`
	HandshakeAge     *int64  `json:"handshake_age_seconds"`
	RxBytes          int64   `json:"rx_bytes"`
	TxBytes          int64   `json:"tx_bytes"`
	PeerCount        int     `json:"peer_count"`
	WGInterface      string  `json:"wg_interface"`
	Status           string  `json:"status"`
	LocalDNS         *string `json:"local_dns"`
	AgentVersion     *string `json:"agent_version"`
}

// Reporter handles periodic heartbeat sending
type Reporter struct {
	state    *config.PublisherState
	interval time.Duration
	client   *http.Client
	stopCh   chan struct{}
}

// NewReporter creates a new heartbeat reporter
func NewReporter(state *config.PublisherState, intervalSeconds int) *Reporter {
	return &Reporter{
		state:    state,
		interval: time.Duration(intervalSeconds) * time.Second,
		client:   &http.Client{Timeout: 10 * time.Second},
		stopCh:   make(chan struct{}),
	}
}

// Start begins the heartbeat loop (blocking)
func (r *Reporter) Start() {
	fmt.Printf("[*] Starting heartbeat loop (interval: %v)\n", r.interval)

	// Send first heartbeat immediately
	r.send()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.send()
		case <-r.stopCh:
			fmt.Println("[*] Heartbeat stopped")
			return
		}
	}
}

// Stop signals the heartbeat loop to exit
func (r *Reporter) Stop() {
	close(r.stopCh)
}

// send gathers metrics and sends a single heartbeat
func (r *Reporter) send() {
	payload := r.gatherMetrics()

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("[WARN] Failed to marshal heartbeat: %v\n", err)
		return
	}

	url := fmt.Sprintf("%s/api/v1/publishers/%s/heartbeat", r.state.ControlPlaneURL, r.state.PublisherID)
	req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		fmt.Printf("[WARN] Failed to create heartbeat request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if r.state.PublisherApiKey != "" {
		req.Header.Set("X-API-Key", r.state.PublisherApiKey)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		fmt.Printf("[WARN] Heartbeat failed: %v\n", err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		fmt.Printf("[WARN] Heartbeat returned HTTP %d\n", resp.StatusCode)
	}
}

// gatherMetrics collects current system and WireGuard metrics
func (r *Reporter) gatherMetrics() *HeartbeatPayload {
	payload := &HeartbeatPayload{
		PublisherID: r.state.PublisherID,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		WGInterface: config.DefaultWGInterface,
		Status:      "online",
		PeerCount:   1,
	}

	// Uptime
	payload.UptimeSeconds = getUptimeSeconds()

	// WireGuard peer status
	peerStatus, err := tunnel.GetPeerStatus()
	if err == nil {
		payload.RxBytes = peerStatus.RxBytes
		payload.TxBytes = peerStatus.TxBytes
		if peerStatus.HasHandshake {
			age := int64(peerStatus.HandshakeAge.Seconds())
			payload.HandshakeAge = &age
		}
	}

	// Local DNS
	localDNS := detectLocalDNS()
	if localDNS != "" {
		payload.LocalDNS = &localDNS
	}

	// Agent version
	v := version.Version
	if v != "" && v != "dev" {
		payload.AgentVersion = &v
	}

	return payload
}

// getUptimeSeconds reads system uptime from /proc/uptime
func getUptimeSeconds() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}

	var uptime float64
	fmt.Sscanf(string(data), "%f", &uptime)
	return int64(uptime)
}

// detectLocalDNS reads /etc/resolv.conf to find the system DNS
func detectLocalDNS() string {
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
					if ns != "127.0.0.11" && ns != "127.0.0.1" {
						return ns
					}
				}
			}
		}
	}
	return ""
}

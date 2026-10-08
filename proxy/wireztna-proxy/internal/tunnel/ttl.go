package tunnel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// QueryPassTTL attempts to get the remaining TTL for a pass by calling
// the broker's REST API. Returns seconds remaining or 0 if unavailable.
// This works because the tunnel WS URL has the same base as the REST API.
func QueryPassTTL(wsURL string, passID string) int {
	// Convert wss://broker/api/v1/tunnel/xxx to https://broker/api/v1/access-passes/xxx
	u, err := url.Parse(wsURL)
	if err != nil {
		return 0
	}

	// Change scheme to https
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else if u.Scheme == "ws" {
		u.Scheme = "http"
	}

	// Change path from /api/v1/tunnel/{id} to /api/v1/access-passes/{id}/ttl
	// But the access-passes endpoint requires JWT auth which we don't have.
	// Instead, use a lightweight public endpoint that only returns TTL.
	u.Path = fmt.Sprintf("/api/v1/tunnel/%s/ttl", passID)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0
	}

	var result struct {
		TTL int `json:"ttl_seconds"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0
	}
	return result.TTL
}

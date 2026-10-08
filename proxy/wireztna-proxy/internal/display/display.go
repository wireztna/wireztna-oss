// Package display handles status output and TTL countdown for wireztna-proxy.
package display

import (
	"fmt"
	"os"
	"time"
)

// Display manages status output to stderr.
type Display struct {
	expiresAt time.Time
	done      chan struct{}
}

// New creates a new Display with the given expiration time.
func New(expiresAt time.Time) *Display {
	return &Display{
		expiresAt: expiresAt,
		done:      make(chan struct{}),
	}
}

// PrintStartup prints connection info at startup.
func (d *Display) PrintStartup(localAddr string, passID string, scope string) {
	fmt.Fprintf(os.Stderr, "[wzctl] Connected to broker (scope: %s)\n", scope)
	fmt.Fprintf(os.Stderr, "[wzctl] Listening on %s\n", localAddr)
	d.printTTL()
}

// StartCountdown starts a goroutine that prints TTL every 30 seconds.
func (d *Display) StartCountdown() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-d.done:
				return
			case <-ticker.C:
				remaining := time.Until(d.expiresAt)
				if remaining <= 0 {
					fmt.Fprintf(os.Stderr, "[wzctl] Pass expired\n")
					return
				}
				d.printTTL()
			}
		}
	}()
}

// Stop stops the countdown goroutine.
func (d *Display) Stop() {
	close(d.done)
}

// PrintDisconnect prints disconnection info.
func (d *Display) PrintDisconnect(reason string, bytesIn int64, bytesOut int64) {
	fmt.Fprintf(os.Stderr, "[wzctl] Disconnected: %s\n", reason)
	fmt.Fprintf(os.Stderr, "[wzctl] Traffic: %s uploaded, %s downloaded\n",
		formatBytes(bytesOut), formatBytes(bytesIn))
}

// PrintError prints an error message.
func (d *Display) PrintError(msg string) {
	fmt.Fprintf(os.Stderr, "[wzctl] Error: %s\n", msg)
}

func (d *Display) printTTL() {
	remaining := time.Until(d.expiresAt)
	if remaining <= 0 {
		fmt.Fprintf(os.Stderr, "[wzctl] Pass expired\n")
		return
	}

	minutes := int(remaining.Minutes())
	seconds := int(remaining.Seconds()) % 60
	fmt.Fprintf(os.Stderr, "[wzctl] Pass expires in %dm %ds\n", minutes, seconds)
}

func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

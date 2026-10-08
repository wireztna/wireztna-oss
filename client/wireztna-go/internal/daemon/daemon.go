// Package daemon implements background session renewal and tunnel health monitoring.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/dns"
	"github.com/wireztna/client/internal/tunnel"
)

type sessionClient interface {
	RenewSessionContext(context.Context, string, ...string) (*api.SessionResponse, error)
	GetDNSZonesContext(context.Context) ([]string, error)
}

type lifecycleTunnel interface {
	Down()
	Up() error
	UpdatePSK(string) error
}

type dnsManager interface {
	Configure(string, []string) error
	Cleanup() error
}

// Package-private seams keep lifecycle failure tests host-independent.
var (
	newTunnel        = func(cfg tunnel.Config) (lifecycleTunnel, error) { return tunnel.New(cfg) }
	newDNSManager    = func() dnsManager { return dns.NewManager() }
	getTunnelStatus  = tunnel.GetStatus
	saveState        = config.SaveStateForLifecycle
	lifecycleCurrent = func(ctx context.Context, generation uint64) bool {
		if ctx.Err() != nil {
			return false
		}
		if generation == 0 {
			return !config.IsUserDisconnected()
		}
		return config.IsLifecycleCurrent(generation)
	}
)

// Config holds daemon configuration.
type Config struct {
	Client              sessionClient
	Tunnel              lifecycleTunnel
	TunnelConfig        *tunnel.Config
	GroupID             string
	ExitNodeID          string
	RenewBefore         time.Duration
	Verbose             bool
	OperationMu         *sync.Mutex
	LifecycleGeneration uint64
	OnTunnelReplaced    func(*tunnel.Tunnel)
}

// Daemon manages background session renewal and health checks.
type Daemon struct {
	config               Config
	staleCount           int
	reconnecting         bool
	reconnectMu          sync.Mutex
	lastAppliedSessionID string
	lastAppliedExpiresAt time.Time
}

const (
	staleThreshold       = 2
	maxReconnectAttempts = 3
	reconnectCooldown    = 30 * time.Second
	healthTimeout        = 50 * time.Second
)

// New creates a new daemon.
func New(cfg Config) *Daemon {
	if cfg.RenewBefore == 0 {
		cfg.RenewBefore = 30 * time.Minute
	}
	return &Daemon{config: cfg}
}

// Run starts the daemon loop. It blocks until the context is cancelled.
func (d *Daemon) Run(ctx context.Context) {
	healthTicker := time.NewTicker(30 * time.Second)
	defer healthTicker.Stop()
	renewTicker := time.NewTicker(60 * time.Second)
	defer renewTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-healthTicker.C:
			d.checkHealth(ctx)
		case <-renewTicker.C:
			if err := d.checkRenewal(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, config.ErrStaleLifecycle) {
				fmt.Printf("[daemon] ERROR: renewal failed: %v\n", err)
			}
		}
	}
}

func (d *Daemon) current(ctx context.Context) bool {
	return lifecycleCurrent(ctx, d.config.LifecycleGeneration)
}

func (d *Daemon) withMutation(ctx context.Context, fn func() error) error {
	if d.config.OperationMu != nil {
		d.config.OperationMu.Lock()
		defer d.config.OperationMu.Unlock()
	}
	if !d.current(ctx) {
		return config.ErrStaleLifecycle
	}
	return fn()
}

func (d *Daemon) checkHealth(ctx context.Context) {
	if !d.current(ctx) {
		d.staleCount = 0
		return
	}
	ifaceName := "wg-wireztna"
	if d.config.TunnelConfig != nil && d.config.TunnelConfig.InterfaceName != "" {
		ifaceName = d.config.TunnelConfig.InterfaceName
	}
	info, err := getTunnelStatus(ifaceName)
	if err != nil {
		if d.config.Verbose {
			fmt.Printf("[daemon] tunnel check failed: %v\n", err)
		}
		d.staleCount = staleThreshold
		d.tryReconnect(ctx)
		return
	}
	if info.LastHandshake.IsZero() || time.Since(info.LastHandshake) > 24*time.Hour {
		if d.config.Verbose {
			fmt.Println("[daemon] no handshake yet — waiting for broker")
		}
		return
	}
	if age := time.Since(info.LastHandshake); age > 180*time.Second {
		d.staleCount++
		fmt.Printf("[daemon] WARNING: handshake stale (%s ago, count %d/%d)\n", age.Round(time.Second), d.staleCount, staleThreshold)
		if d.staleCount >= staleThreshold {
			d.tryReconnect(ctx)
		}
		return
	}
	d.staleCount = 0
}

func (d *Daemon) tryReconnect(ctx context.Context) {
	if !d.current(ctx) {
		d.staleCount = 0
		return
	}
	d.reconnectMu.Lock()
	if d.reconnecting {
		d.reconnectMu.Unlock()
		return
	}
	d.reconnecting = true
	d.reconnectMu.Unlock()
	defer func() {
		d.reconnectMu.Lock()
		d.reconnecting = false
		d.reconnectMu.Unlock()
	}()

	fmt.Println("[daemon] Tunnel stale — initiating auto-reconnect...")
	for attempt := 1; attempt <= maxReconnectAttempts; attempt++ {
		if !d.current(ctx) {
			d.staleCount = 0
			return
		}
		if attempt > 1 {
			if err := waitContext(ctx, reconnectCooldown); err != nil {
				return
			}
		}
		if err := d.reconnect(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, config.ErrStaleLifecycle) {
				return
			}
			fmt.Printf("[daemon] Reconnect attempt %d failed: %v\n", attempt, err)
			continue
		}
		d.staleCount = 0
		fmt.Println("[daemon] Reconnected successfully — tunnel is healthy and state is persisted")
		return
	}
	fmt.Println("[daemon] ERROR: All reconnect attempts failed. Run 'wireztna disconnect' and 'wireztna connect' manually.")
}

func (d *Daemon) reconnect(ctx context.Context) error {
	return d.withMutation(ctx, func() error {
		if d.config.Tunnel == nil || d.config.TunnelConfig == nil {
			return fmt.Errorf("incomplete tunnel configuration")
		}

		d.config.Tunnel.Down()
		if err := waitContext(ctx, time.Second); err != nil {
			return err
		}
		if !d.current(ctx) {
			return config.ErrStaleLifecycle
		}

		session, err := d.config.Client.RenewSessionContext(api.WithLifecycleGeneration(ctx, d.config.LifecycleGeneration), d.config.GroupID, d.config.ExitNodeID)
		if err != nil {
			return fmt.Errorf("session renewal: %w", err)
		}
		if !d.current(ctx) {
			return config.ErrStaleLifecycle
		}

		tunCfg := *d.config.TunnelConfig
		tunCfg.PresharedKey = session.PresharedKey
		if len(session.AllowedIPs) > 0 {
			tunCfg.AllowedIPs = session.AllowedIPs
		}
		tunCfg.IsExitNode = session.IsExitNode
		newTun, err := newTunnel(tunCfg)
		if err != nil {
			return fmt.Errorf("create tunnel: %w", err)
		}
		if err := newTun.Up(); err != nil {
			return fmt.Errorf("bring tunnel up: %w", err)
		}
		rollback := func(cause error) error {
			newTun.Down()
			return errors.Join(cause, wrapCleanupError("DNS cleanup", newDNSManager().Cleanup()))
		}
		if !d.current(ctx) {
			return rollback(config.ErrStaleLifecycle)
		}

		var zones []string
		if tunCfg.IsExitNode {
			zones = []string{"."}
		} else {
			zones, err = d.config.Client.GetDNSZonesContext(ctx)
			if err != nil {
				return rollback(fmt.Errorf("fetch DNS zones: %w", err))
			}
		}
		if len(zones) > 0 && tunCfg.DNS != "" {
			if err := newDNSManager().Configure(tunCfg.DNS, zones); err != nil {
				return rollback(fmt.Errorf("configure DNS: %w", err))
			}
		}
		if !d.current(ctx) {
			return rollback(config.ErrStaleLifecycle)
		}
		if err := waitForHealthyTunnel(ctx, tunCfg.InterfaceName, healthTimeout); err != nil {
			return rollback(fmt.Errorf("verify tunnel health: %w", err))
		}

		state, loadErr := config.LoadState()
		if loadErr != nil {
			return rollback(fmt.Errorf("load runtime state: %w", loadErr))
		}
		state.SessionID = session.SessionID
		state.ExpiresAt = session.ExpiresAt.Time
		state.PresharedKey = session.PresharedKey
		state.GroupID = d.config.GroupID
		state.ExitNodeID = d.config.ExitNodeID
		state.IsExitNode = tunCfg.IsExitNode || d.config.ExitNodeID != ""
		if !state.IsExitNode {
			state.ExitNodeName = ""
		}
		state.ConnectedAt = time.Now()
		state.InterfaceName = tunCfg.InterfaceName
		if err := saveState(d.config.LifecycleGeneration, state); err != nil {
			return rollback(fmt.Errorf("persist runtime state: %w", err))
		}
		if !d.current(ctx) {
			return rollback(config.ErrStaleLifecycle)
		}

		d.config.Tunnel = newTun
		d.config.TunnelConfig = &tunCfg
		d.lastAppliedSessionID = session.SessionID
		d.lastAppliedExpiresAt = session.ExpiresAt.Time
		if concrete, ok := newTun.(*tunnel.Tunnel); ok && d.config.OnTunnelReplaced != nil {
			d.config.OnTunnelReplaced(concrete)
		}
		fmt.Printf("[daemon] Tunnel recreated — session expires at %s\n", session.ExpiresAt.Time.Local().Format("15:04:05"))
		return nil
	})
}

func (d *Daemon) checkRenewal(ctx context.Context) error {
	return d.withMutation(ctx, func() error {
		state, err := config.LoadState()
		if err != nil {
			return fmt.Errorf("load runtime state: %w", err)
		}
		expiresAt := state.ExpiresAt
		if d.lastAppliedExpiresAt.After(expiresAt) {
			expiresAt = d.lastAppliedExpiresAt
		}
		remaining := time.Until(expiresAt)
		if remaining > d.config.RenewBefore {
			if d.config.Verbose {
				fmt.Printf("[daemon] Session OK — %s remaining\n", remaining.Round(time.Minute))
			}
			return nil
		}

		fmt.Printf("[daemon] Session expires in %s — renewing...\n", remaining.Round(time.Second))
		session, err := d.config.Client.RenewSessionContext(api.WithLifecycleGeneration(ctx, d.config.LifecycleGeneration), d.config.GroupID, d.config.ExitNodeID)
		if err != nil {
			return err
		}
		if !d.current(ctx) {
			return config.ErrStaleLifecycle
		}
		if err := d.config.Tunnel.UpdatePSK(session.PresharedKey); err != nil {
			return fmt.Errorf("apply renewed PSK: %w", err)
		}
		d.lastAppliedSessionID = session.SessionID
		d.lastAppliedExpiresAt = session.ExpiresAt.Time
		if !d.current(ctx) {
			return config.ErrStaleLifecycle
		}
		state.SessionID = session.SessionID
		state.ExpiresAt = session.ExpiresAt.Time
		state.PresharedKey = session.PresharedKey
		if err := saveState(d.config.LifecycleGeneration, state); err != nil {
			return fmt.Errorf("persist renewed session: %w", err)
		}
		fmt.Printf("[daemon] Session renewed — expires at %s\n", session.ExpiresAt.Time.Local().Format("15:04:05"))
		return nil
	})
}

func waitForHealthyTunnel(ctx context.Context, ifaceName string, timeout time.Duration) error {
	if ifaceName == "" {
		ifaceName = "wg-wireztna"
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastErr error
	for {
		info, err := getTunnelStatus(ifaceName)
		if err == nil && !info.LastHandshake.IsZero() && time.Since(info.LastHandshake) < 180*time.Second {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("no recent handshake")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return lastErr
		case <-ticker.C:
		}
	}
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func wrapCleanupError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

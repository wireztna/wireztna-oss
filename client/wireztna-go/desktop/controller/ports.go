package controller

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"
)

// OwnerID identifies the local owner whose resources a mutation may change.
type OwnerID string

// SecretRef is an opaque, owner-scoped reference understood by SecretStore.
type SecretRef string

// InterfaceID identifies a WireZTNA-owned logical interface without prescribing
// a platform device name.
type InterfaceID string

// WireGuardConfig is the platform-neutral input for a WireGuard device.
// Identity and peer credentials are references; key material remains in SecretStore.
type WireGuardConfig struct {
	ConfigurationID ConfigurationID
	Generation      Generation
	InterfaceID     InterfaceID
	Addresses       []netip.Prefix
	Identity        SecretRef
	Peers           []WireGuardPeer
}

// WireGuardPeer describes one peer without carrying private credential material.
type WireGuardPeer struct {
	PublicKey       string
	Credential      SecretRef
	Endpoint        string
	AllowedPrefixes []netip.Prefix
	Keepalive       time.Duration
}

// RouteSet is the complete set of routes owned by one controller operation.
type RouteSet struct {
	Routes []Route
}

// Route is a platform-neutral route request.
type Route struct {
	Destination netip.Prefix
	Gateway     netip.Addr
	Metric      uint32
}

// DNSConfig is the complete DNS state owned by one controller operation.
type DNSConfig struct {
	Servers       []netip.Addr
	SearchDomains []string
	MatchDomains  []string
}

// PortHealth is a redacted health result from a platform port.
type PortHealth struct {
	Status HealthStatus
	Detail string
}

// The named health types prevent accidentally mixing results from different
// health dimensions.
type WireGuardHealth PortHealth
type RouteHealth PortHealth
type DNSHealth PortHealth

// NetworkEventKind identifies a platform-neutral network lifecycle signal.
type NetworkEventKind string

const (
	NetworkEventChanged NetworkEventKind = "network_changed"
	NetworkEventWake    NetworkEventKind = "wake"
)

// NetworkEvent is an observed network lifecycle signal.
type NetworkEvent struct {
	Kind       NetworkEventKind
	OccurredAt time.Time
}

// UpdatePackage identifies a locally available update without prescribing an
// installer or packaging format.
type UpdatePackage struct {
	Version  string
	Location string
	Digest   []byte
}

// DiagnosticsRequest selects non-sensitive diagnostic sections.
type DiagnosticsRequest struct {
	IncludeNetwork bool
	IncludeService bool
}

// DiagnosticEntry is one redacted, platform-neutral diagnostic observation.
type DiagnosticEntry struct {
	Component string
	Status    HealthStatus
	Detail    string
}

// DiagnosticsReport contains only redacted observations safe for controller
// consumers. Platform implementations are responsible for redaction at source.
type DiagnosticsReport struct {
	GeneratedAt time.Time
	Entries     []DiagnosticEntry
}

// Undo is an owner-scoped rollback action with at-most-once execution. Revert
// passes the immutable owner captured by NewUndo to the callback and caches
// every result, including errors. Copies share the same state. A failed or
// ambiguous result is deliberately not retried because doing so could repeat a
// side effect whose completion could not be established.
type Undo struct {
	state *undoState
}

type undoState struct {
	owner   OwnerID
	revert  func(context.Context, OwnerID) error
	mu      sync.Mutex
	started bool
	done    chan struct{}
	err     error
}

// NewUndo creates an at-most-once rollback action for exactly one owner. The
// callback must obey context cancellation, execute synchronously, and leave no
// goroutine continuing side effects after it returns. Its first result is cached.
func NewUndo(owner OwnerID, revert func(context.Context, OwnerID) error) (Undo, error) {
	if owner == "" {
		return Undo{}, errors.New("undo owner is required")
	}
	if revert == nil {
		return Undo{}, errors.New("undo action is required")
	}
	return Undo{state: &undoState{owner: owner, revert: revert}}, nil
}

// Owner returns the identity whose resources this rollback may change.
func (u Undo) Owner() OwnerID {
	if u.state == nil {
		return ""
	}
	return u.state.owner
}

// Revert invokes the rollback callback at most once with the owner captured by
// NewUndo and shares its cached result, including errors. There is deliberately
// no retry after an ambiguous failure. Callers waiting for an in-flight callback
// may stop waiting via their context without changing the cached execution.
func (u Undo) Revert(ctx context.Context) error {
	if u.state == nil {
		return errors.New("undo action is not initialized")
	}
	if ctx == nil {
		return errors.New("undo context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	u.state.mu.Lock()
	if u.state.started {
		done := u.state.done
		u.state.mu.Unlock()
		select {
		case <-done:
			u.state.mu.Lock()
			err := u.state.err
			u.state.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	u.state.started = true
	u.state.done = make(chan struct{})
	u.state.mu.Unlock()

	err := func() (result error) {
		defer func() {
			if recover() != nil {
				result = errors.New("undo action panicked")
			}
		}()
		return u.state.revert(ctx, u.state.owner)
	}()
	u.state.mu.Lock()
	u.state.err = err
	close(u.state.done)
	u.state.mu.Unlock()
	return err
}

// Platform port contract: every method must observe context cancellation. Apply
// and other mutations are synchronous and must not return while an implementation
// goroutine can still mutate host state. An error may include a valid Undo when
// the completed portion is known; ambiguous completion without Undo forces recovery.

// WireGuardDevice owns WireZTNA WireGuard device state for one local owner.
type WireGuardDevice interface {
	Apply(context.Context, OwnerID, WireGuardConfig) (Undo, error)
	Health(context.Context, OwnerID) (WireGuardHealth, error)
}

// RouteManager owns only the routes created for one local owner.
type RouteManager interface {
	Apply(context.Context, OwnerID, RouteSet) (Undo, error)
	Health(context.Context, OwnerID) (RouteHealth, error)
}

// DNSManager owns only the DNS state created for one local owner.
type DNSManager interface {
	Apply(context.Context, OwnerID, DNSConfig) (Undo, error)
	Health(context.Context, OwnerID) (DNSHealth, error)
}

// NetworkObserver emits signals until ctx is canceled, then closes its channel.
type NetworkObserver interface {
	Events(context.Context) (<-chan NetworkEvent, error)
}

// SecretStore is the sole persistent credential store. Mutations return an undo
// action scoped to the same owner; references have no meaning across owners.
type SecretStore interface {
	Put(context.Context, OwnerID, SecretRef, []byte) (Undo, error)
	Get(context.Context, OwnerID, SecretRef) ([]byte, error)
	Delete(context.Context, OwnerID, SecretRef) (Undo, error)
}

// UpdateInstaller applies a local package for one owner. Installation remains a
// port operation; this package contains no platform installer implementation.
type UpdateInstaller interface {
	Install(context.Context, OwnerID, UpdatePackage) (Undo, error)
}

// PlatformDiagnostics collects redacted observations without mutating the host.
type PlatformDiagnostics interface {
	Collect(context.Context, OwnerID, DiagnosticsRequest) (DiagnosticsReport, error)
}

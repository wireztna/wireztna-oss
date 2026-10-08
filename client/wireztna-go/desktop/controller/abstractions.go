package controller

import (
	"context"
	"errors"
	"time"
)

// Context contract for injected dependencies: every method must observe
// cancellation promptly. A mutating call is synchronous; when it returns, no
// goroutine owned by the implementation may continue its side effects. This
// lets the controller retain its lease until all mutation and rollback ends.

// Session is an opaque, non-secret lease used by a Planner. Implementations may
// keep credential material behind SecretRef values, but must not expose it from
// this interface or persist it in the applied journal.
type Session interface {
	Owner() OwnerID
	Generation() Generation
	ExpiresAt() time.Time
}

// SessionProvider obtains an owner-scoped session for a connected desired
// state. Disconnect plans do not require a session.
type SessionProvider interface {
	Acquire(context.Context, OwnerID, DesiredState) (Session, error)
}

// Plan is the complete platform-neutral mutation produced for one intent. A
// disconnect is represented by a plan whose Applied.Connected is false; the
// platform ports interpret its WG, route, and DNS configurations as complete
// replacement state for the owner.
type Plan struct {
	WireGuard WireGuardConfig
	Routes    RouteSet
	DNS       DNSConfig
	Applied   AppliedState
}

// Validate rejects plans that could apply a different owner revision than the
// accepted desired state.
func (p Plan) Validate(desired DesiredState) error {
	if p.Applied.ConfigurationID != desired.ConfigurationID ||
		p.Applied.Generation != desired.Generation ||
		p.Applied.GroupID != desired.GroupID ||
		p.Applied.ExitNodeID != desired.ExitNodeID ||
		p.Applied.Connected != desired.Connected {
		return errors.New("plan applied state does not match desired state")
	}
	if p.WireGuard.ConfigurationID != desired.ConfigurationID ||
		p.WireGuard.Generation != desired.Generation {
		return errors.New("plan WireGuard revision does not match desired state")
	}
	return nil
}

// Planner converts desired state and an optional session into a complete plan.
// Session is nil for disconnect. Planner implementations must not couple this
// package to cmd, service, or transport packages. Plan must return only after
// planning work has stopped; it must not launch platform side effects.
type Planner interface {
	Plan(context.Context, OwnerID, DesiredState, Session) (Plan, error)
}

// HealthObservation contains the independently observed platform dimensions.
// TruthGate adds the end-to-end observation and derives the externally visible
// state instead of allowing successful Apply calls to imply connectivity.
type HealthObservation struct {
	WireGuard WireGuardHealth
	Routes    RouteHealth
	DNS       DNSHealth
}

// Truth is the state and complete health result established after all platform
// mutations have completed.
type Truth struct {
	State  ConnectionState
	Health Health
}

// Validate enforces truthful terminal states for connected and disconnected
// plans.
func (t Truth) Validate(expectedConnected bool) error {
	if !t.State.valid() {
		return errors.New("truth gate returned an unknown connection state")
	}
	if t.Health.Healthy != t.Health.ConditionsHealthy() {
		return errors.New("truth gate returned contradictory aggregate health")
	}
	if expectedConnected {
		if t.State != ConnectionStateConnected || !t.Health.Healthy {
			return errors.New("truth gate did not establish a healthy connected state")
		}
		return nil
	}
	if t.State != ConnectionStateDisconnected || t.Health.Healthy {
		return errors.New("truth gate did not establish a disconnected state")
	}
	return nil
}

// TruthGate performs the end-to-end check and derives state from observations.
// Verify must finish all observations before returning and obey its context.
type TruthGate interface {
	Verify(context.Context, OwnerID, DesiredState, AppliedState, HealthObservation) (Truth, error)
}

// OperationIDGenerator supplies non-secret operation correlation IDs.
type OperationIDGenerator interface {
	NewOperationID() (OperationID, error)
}

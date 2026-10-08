// Package controller defines the platform-neutral desktop controller domain.
package controller

import (
	"errors"
	"time"
)

// CommandKind identifies an intent accepted by the desktop controller.
type CommandKind string

const (
	CommandConnect       CommandKind = "connect"
	CommandDisconnect    CommandKind = "disconnect"
	CommandSwitch        CommandKind = "switch"
	CommandRenew         CommandKind = "renew"
	CommandWake          CommandKind = "wake"
	CommandNetworkChange CommandKind = "network_change"
	// CommandReconcile is an explicit complete replacement. Unlike ordinary
	// connected intents, it may run while durable recovery evidence is present.
	CommandReconcile CommandKind = "reconcile"
)

// Command is an intent to reconcile the desktop connection.
type Command struct {
	Kind    CommandKind
	Desired DesiredState
}

// ConfigurationID identifies one non-secret controller configuration.
type ConfigurationID string

// Generation distinguishes revisions of the same logical configuration.
type Generation uint64

// GroupID identifies the selected access group without exposing credentials.
type GroupID string

// ExitNodeID identifies the selected exit node. Empty means split tunnel.
type ExitNodeID string

// DesiredState is the non-secret connection state requested by the caller.
type DesiredState struct {
	ConfigurationID ConfigurationID
	Generation      Generation
	GroupID         GroupID
	ExitNodeID      ExitNodeID
	Connected       bool
}

// AppliedState is the non-secret connection state successfully applied to the
// local system. ConfigurationID and Generation make stale or partially applied
// revisions distinguishable from accepted intent.
type AppliedState struct {
	ConfigurationID ConfigurationID
	Generation      Generation
	GroupID         GroupID
	ExitNodeID      ExitNodeID
	Connected       bool
	// ExpiresAt is the absolute control-plane lease deadline. It remains zero
	// for disconnected and pre-lifecycle recovered states.
	ExpiresAt time.Time
}

// ConnectionState is the stable, externally observable lifecycle state.
type ConnectionState string

const (
	ConnectionStateDisconnected   ConnectionState = "disconnected"
	ConnectionStateConnecting     ConnectionState = "connecting"
	ConnectionStateConnected      ConnectionState = "connected"
	ConnectionStateReconnecting   ConnectionState = "reconnecting"
	ConnectionStateDegraded       ConnectionState = "degraded"
	ConnectionStateAuthRequired   ConnectionState = "auth_required"
	ConnectionStateUpdateRequired ConnectionState = "update_required"
)

// HealthStatus is the result of one independently observed health dimension.
type HealthStatus string

const (
	HealthUnknown   HealthStatus = "unknown"
	HealthHealthy   HealthStatus = "healthy"
	HealthUnhealthy HealthStatus = "unhealthy"
)

// Health reports the aggregate result and every condition required for a
// truthful connected state. A failing field identifies the degraded dimension.
type Health struct {
	Healthy   bool
	WireGuard HealthStatus
	Routes    HealthStatus
	DNS       HealthStatus
	EndToEnd  HealthStatus
}

// ConditionsHealthy derives aggregate health from every required dimension.
func (h Health) ConditionsHealthy() bool {
	return h.WireGuard == HealthHealthy &&
		h.Routes == HealthHealthy &&
		h.DNS == HealthHealthy &&
		h.EndToEnd == HealthHealthy
}

// OperationID correlates an accepted command with its ordered events.
type OperationID string

// Operation describes an in-flight command without carrying credential data.
type Operation struct {
	ID        OperationID
	Command   Command
	StartedAt time.Time
}

// Sequence is a monotonically increasing event cursor.
type Sequence uint64

// EventKind identifies operation lifecycle and snapshot-change events.
type EventKind string

const (
	EventOperationStarted   EventKind = "operation_started"
	EventOperationProgress  EventKind = "operation_progress"
	EventOperationSucceeded EventKind = "operation_succeeded"
	EventOperationFailed    EventKind = "operation_failed"
	EventSnapshotChanged    EventKind = "snapshot_changed"
)

// Event is an ordered, owner-safe controller event. Progress is present only
// for operation_progress and Error only for operation_failed. Existing fields
// retain their original meanings for consumers that do not inspect payloads.
type Event struct {
	Sequence    Sequence
	Kind        EventKind
	OperationID OperationID
	State       ConnectionState
	OccurredAt  time.Time
	Progress    *Progress
	Error       *Error
}

// Validate checks the event's discriminated payload shape. Stream-level ordering
// and single-terminal guarantees require validation across a sequence of events.
func (e Event) Validate() error {
	if e.State != "" && !e.State.valid() {
		return errors.New("event contains an unknown connection state")
	}

	switch e.Kind {
	case EventOperationStarted, EventOperationSucceeded:
		if e.OperationID == "" {
			return errors.New("operation event requires an operation ID")
		}
		if e.Progress != nil || e.Error != nil {
			return errors.New("operation event kind does not accept a payload")
		}
	case EventOperationProgress:
		if e.OperationID == "" {
			return errors.New("operation progress requires an operation ID")
		}
		if e.Progress == nil {
			return errors.New("operation progress requires a progress payload")
		}
		if e.Error != nil {
			return errors.New("operation progress cannot contain an error payload")
		}
		if err := e.Progress.Validate(); err != nil {
			return err
		}
	case EventOperationFailed:
		if e.OperationID == "" {
			return errors.New("operation failure requires an operation ID")
		}
		if e.Error == nil {
			return errors.New("operation failure requires an error payload")
		}
		if e.Progress != nil {
			return errors.New("operation failure cannot contain a progress payload")
		}
		if err := e.Error.Validate(); err != nil {
			return err
		}
	case EventSnapshotChanged:
		if e.Progress != nil || e.Error != nil {
			return errors.New("snapshot event does not accept an operation payload")
		}
	default:
		return errors.New("event contains an unknown kind")
	}

	return nil
}

func (s ConnectionState) valid() bool {
	switch s {
	case ConnectionStateDisconnected,
		ConnectionStateConnecting,
		ConnectionStateConnected,
		ConnectionStateReconnecting,
		ConnectionStateDegraded,
		ConnectionStateAuthRequired,
		ConnectionStateUpdateRequired:
		return true
	default:
		return false
	}
}

// Snapshot is the current platform-neutral controller view. Desired and Applied
// are intentionally separate, and ActiveOperation is nil when no command runs.
type Snapshot struct {
	State           ConnectionState
	Desired         DesiredState
	Applied         AppliedState
	Health          Health
	ActiveOperation *Operation
	Sequence        Sequence
}

// Validate rejects snapshots whose lifecycle, aggregate health, and applied
// state contradict one another. Every snapshot published by Controller passes
// this method.
func (s Snapshot) Validate() error {
	if !s.State.valid() {
		return errors.New("snapshot contains an unknown connection state")
	}
	if s.Health.Healthy != s.Health.ConditionsHealthy() {
		return errors.New("aggregate health does not match required health dimensions")
	}

	switch s.State {
	case ConnectionStateConnected:
		if !s.Applied.Connected {
			return errors.New("connected state requires applied connection state")
		}
		if !s.Health.Healthy {
			return errors.New("connected state requires healthy network conditions")
		}
	case ConnectionStateDisconnected:
		if s.Applied.Connected {
			return errors.New("disconnected state cannot retain applied connected state")
		}
		if s.Health.Healthy {
			return errors.New("disconnected state cannot report connected health")
		}
	case ConnectionStateConnecting, ConnectionStateReconnecting, ConnectionStateDegraded:
		if s.Health.Healthy {
			return errors.New("transitional or degraded state cannot report healthy conditions")
		}
	}

	return nil
}

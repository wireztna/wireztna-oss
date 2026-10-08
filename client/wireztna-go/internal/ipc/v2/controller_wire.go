package v2

import (
	"errors"
	"fmt"
	"time"

	"github.com/wireztna/client/desktop/controller"
)

// DesiredState is the non-secret desired controller state on the wire.
type DesiredState struct {
	ConfigurationID string `json:"configuration_id"`
	Generation      uint64 `json:"generation"`
	GroupID         string `json:"group_id"`
	ExitNodeID      string `json:"exit_node_id,omitempty"`
	Connected       bool   `json:"connected"`
}

// AppliedState is the non-secret applied controller state on the wire.
type AppliedState struct {
	ConfigurationID string `json:"configuration_id"`
	Generation      uint64 `json:"generation"`
	GroupID         string `json:"group_id"`
	ExitNodeID      string `json:"exit_node_id,omitempty"`
	Connected       bool   `json:"connected"`
}

// Health is the controller health projection on the wire.
type Health struct {
	Healthy   bool                    `json:"healthy"`
	WireGuard controller.HealthStatus `json:"wireguard"`
	Routes    controller.HealthStatus `json:"routes"`
	DNS       controller.HealthStatus `json:"dns"`
	EndToEnd  controller.HealthStatus `json:"end_to_end"`
}

// Operation describes the active controller operation without secret material.
type Operation struct {
	ID        string            `json:"id"`
	Command   ControllerCommand `json:"command"`
	StartedAt time.Time         `json:"started_at"`
}

// ControllerCommand is an operation intent embedded in a snapshot.
type ControllerCommand struct {
	Kind    controller.CommandKind `json:"kind"`
	Desired DesiredState           `json:"desired"`
}

// Snapshot is the snake_case wire projection of controller.Snapshot.
type Snapshot struct {
	State           controller.ConnectionState `json:"state"`
	Desired         DesiredState               `json:"desired"`
	Applied         AppliedState               `json:"applied"`
	Health          Health                     `json:"health"`
	ActiveOperation *Operation                 `json:"active_operation,omitempty"`
	Catalog         *ConnectionCatalog         `json:"catalog,omitempty"`
	StreamID        string                     `json:"stream_id"`
	Epoch           uint64                     `json:"epoch"`
	Sequence        uint64                     `json:"sequence"`
}

func (s Snapshot) validateReadModel() error {
	_, err := s.controller()
	return err
}

// StreamIdentity identifies the event epoch represented by this snapshot.
func (s Snapshot) StreamIdentity() StreamIdentity {
	return StreamIdentity{StreamID: s.StreamID, Epoch: s.Epoch}
}

// Cursor returns the exact replay cursor represented by this snapshot.
func (s Snapshot) Cursor() StreamCursor {
	return StreamCursor{StreamIdentity: s.StreamIdentity(), Sequence: s.Sequence}
}

// Progress is the wire projection of controller.Progress.
type Progress struct {
	Stage controller.ProgressStage `json:"stage"`
}

// Event is the snake_case wire projection of controller.Event.
type Event struct {
	Sequence    uint64                     `json:"sequence"`
	Kind        controller.EventKind       `json:"kind"`
	OperationID string                     `json:"operation_id"`
	State       controller.ConnectionState `json:"state,omitempty"`
	OccurredAt  time.Time                  `json:"occurred_at"`
	Progress    *Progress                  `json:"progress,omitempty"`
	Error       *WireError                 `json:"error,omitempty"`
}

// SnapshotFromController validates and projects the controller's source of truth.
func SnapshotFromController(source controller.Snapshot, identity StreamIdentity) (Snapshot, error) {
	if err := identity.validate(); err != nil {
		return Snapshot{}, fmt.Errorf("invalid snapshot stream identity: %w", err)
	}
	if err := source.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("invalid controller snapshot: %w", err)
	}
	if !validConnectionState(source.State) {
		return Snapshot{}, errors.New("invalid controller snapshot: unknown state")
	}
	if !validHealth(source.Health) {
		return Snapshot{}, errors.New("invalid controller snapshot: unknown health status")
	}

	result := Snapshot{
		State:    source.State,
		Desired:  desiredFromController(source.Desired),
		Applied:  appliedFromController(source.Applied),
		Health:   healthFromController(source.Health),
		StreamID: identity.StreamID,
		Epoch:    identity.Epoch,
		Sequence: uint64(source.Sequence),
	}
	if source.ActiveOperation != nil {
		result.ActiveOperation = &Operation{
			ID: string(source.ActiveOperation.ID),
			Command: ControllerCommand{
				Kind:    source.ActiveOperation.Command.Kind,
				Desired: desiredFromController(source.ActiveOperation.Command.Desired),
			},
			StartedAt: source.ActiveOperation.StartedAt,
		}
	}
	return result, nil
}

// Controller converts and validates a wire snapshot for client consumers.
func (s Snapshot) Controller() (controller.Snapshot, error) {
	return s.controller()
}

func (s Snapshot) controller() (controller.Snapshot, error) {
	if err := s.StreamIdentity().validate(); err != nil {
		return controller.Snapshot{}, err
	}
	if s.Catalog != nil {
		if err := s.Catalog.validate(); err != nil {
			return controller.Snapshot{}, err
		}
	}
	result := controller.Snapshot{
		State:    s.State,
		Desired:  s.Desired.controller(),
		Applied:  s.Applied.controller(),
		Health:   s.Health.controller(),
		Sequence: controller.Sequence(s.Sequence),
	}
	if s.ActiveOperation != nil {
		result.ActiveOperation = &controller.Operation{
			ID: controller.OperationID(s.ActiveOperation.ID),
			Command: controller.Command{
				Kind:    s.ActiveOperation.Command.Kind,
				Desired: s.ActiveOperation.Command.Desired.controller(),
			},
			StartedAt: s.ActiveOperation.StartedAt,
		}
	}
	if !validConnectionState(result.State) || !validHealth(result.Health) {
		return controller.Snapshot{}, errors.New("snapshot contains an unknown state or health value")
	}
	if err := result.Validate(); err != nil {
		return controller.Snapshot{}, err
	}
	return result, nil
}

// EventFromController validates and projects one controller event.
func EventFromController(source controller.Event) (Event, error) {
	if err := source.Validate(); err != nil {
		return Event{}, fmt.Errorf("invalid controller event: %w", err)
	}
	result := Event{
		Sequence:    uint64(source.Sequence),
		Kind:        source.Kind,
		OperationID: string(source.OperationID),
		State:       source.State,
		OccurredAt:  source.OccurredAt,
	}
	if source.Progress != nil {
		result.Progress = &Progress{Stage: source.Progress.Stage}
	}
	if source.Error != nil {
		result.Error = wireErrorFromController(source.Error)
	}
	return result, nil
}

// Controller converts and validates a wire event for client consumers.
func (e Event) Controller() (controller.Event, error) {
	result := controller.Event{
		Sequence:    controller.Sequence(e.Sequence),
		Kind:        e.Kind,
		OperationID: controller.OperationID(e.OperationID),
		State:       e.State,
		OccurredAt:  e.OccurredAt,
	}
	if e.Progress != nil {
		result.Progress = &controller.Progress{Stage: e.Progress.Stage}
	}
	if e.Error != nil {
		converted, err := e.Error.controller()
		if err != nil {
			return controller.Event{}, err
		}
		result.Error = converted
	}
	if err := result.Validate(); err != nil {
		return controller.Event{}, err
	}
	return result, nil
}

func (e Event) validate() error {
	_, err := e.Controller()
	return err
}

func desiredFromController(source controller.DesiredState) DesiredState {
	return DesiredState{
		ConfigurationID: string(source.ConfigurationID),
		Generation:      uint64(source.Generation),
		GroupID:         string(source.GroupID),
		ExitNodeID:      string(source.ExitNodeID),
		Connected:       source.Connected,
	}
}

func (s DesiredState) controller() controller.DesiredState {
	return controller.DesiredState{
		ConfigurationID: controller.ConfigurationID(s.ConfigurationID),
		Generation:      controller.Generation(s.Generation),
		GroupID:         controller.GroupID(s.GroupID),
		ExitNodeID:      controller.ExitNodeID(s.ExitNodeID),
		Connected:       s.Connected,
	}
}

func appliedFromController(source controller.AppliedState) AppliedState {
	return AppliedState{
		ConfigurationID: string(source.ConfigurationID),
		Generation:      uint64(source.Generation),
		GroupID:         string(source.GroupID),
		ExitNodeID:      string(source.ExitNodeID),
		Connected:       source.Connected,
	}
}

func (s AppliedState) controller() controller.AppliedState {
	return controller.AppliedState{
		ConfigurationID: controller.ConfigurationID(s.ConfigurationID),
		Generation:      controller.Generation(s.Generation),
		GroupID:         controller.GroupID(s.GroupID),
		ExitNodeID:      controller.ExitNodeID(s.ExitNodeID),
		Connected:       s.Connected,
	}
}

func healthFromController(source controller.Health) Health {
	return Health{
		Healthy:   source.Healthy,
		WireGuard: source.WireGuard,
		Routes:    source.Routes,
		DNS:       source.DNS,
		EndToEnd:  source.EndToEnd,
	}
}

func (h Health) controller() controller.Health {
	return controller.Health{
		Healthy:   h.Healthy,
		WireGuard: h.WireGuard,
		Routes:    h.Routes,
		DNS:       h.DNS,
		EndToEnd:  h.EndToEnd,
	}
}

func validConnectionState(state controller.ConnectionState) bool {
	switch state {
	case controller.ConnectionStateDisconnected, controller.ConnectionStateConnecting,
		controller.ConnectionStateConnected, controller.ConnectionStateReconnecting,
		controller.ConnectionStateDegraded, controller.ConnectionStateAuthRequired,
		controller.ConnectionStateUpdateRequired:
		return true
	default:
		return false
	}
}

func validHealth(health controller.Health) bool {
	return validHealthStatus(health.WireGuard) && validHealthStatus(health.Routes) &&
		validHealthStatus(health.DNS) && validHealthStatus(health.EndToEnd)
}

func validHealthStatus(status controller.HealthStatus) bool {
	return status == controller.HealthUnknown || status == controller.HealthHealthy || status == controller.HealthUnhealthy
}

func wireErrorFromController(source *controller.Error) *WireError {
	if source == nil {
		return nil
	}
	return &WireError{Code: ErrorCode(source.Code), Detail: source.Detail}
}

func (e WireError) controller() (*controller.Error, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	if e.Code == ErrorCodeResyncRequired {
		return nil, errors.New("RESYNC_REQUIRED is an IPC stream error, not a controller error")
	}
	result := &controller.Error{Code: controller.ErrorCode(e.Code), Detail: e.Detail}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

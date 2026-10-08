package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/wireztna/client/desktop/controller"
	v2 "github.com/wireztna/client/internal/ipc/v2"
)

const (
	managedRequestTimeout   = 10 * time.Second
	managedOperationTimeout = 2 * time.Minute
	managedStreamLifetime   = 12 * time.Hour
)

var (
	ErrManagedAuthRequired   = errors.New("managed service requires authentication")
	ErrManagedUpdateRequired = errors.New("managed service requires a client update")
)

// ManagedSelection is the only connection input accepted by a managed TUI.
// GroupID is always required, including when an exit node is selected.
type ManagedSelection struct {
	GroupID    string
	ExitNodeID string
}

// ManagedProject is the non-secret project projection supplied by IPC v2.
type ManagedProject struct {
	ID              string
	Name            string
	Description     string
	CIDRs           []string
	Resources       []ManagedResource
	OnlineResources int
}

// ManagedResource is one non-secret project resource supplied by IPC v2.
type ManagedResource struct {
	ID           string
	Name         string
	Status       string
	ExposedCIDRs []string
}

// ManagedExitNode is one non-secret full-tunnel choice supplied by IPC v2.
type ManagedExitNode struct {
	ID       string
	Name     string
	Location string
	Status   string
}

// ManagedSnapshot is the TUI-specific read model. It intentionally omits
// tunnel/session telemetry that IPC v2 does not publish.
type ManagedSnapshot struct {
	State             controller.ConnectionState
	Desired           ManagedSelection
	DesiredConnected  bool
	Applied           ManagedSelection
	AppliedConnected  bool
	Health            controller.Health
	Projects          []ManagedProject
	ExitNodes         []ManagedExitNode
	HasOverlap        bool
	VPNMode           bool
	ActiveOperationID string
	StreamID          string
	Epoch             uint64
	Sequence          uint64
}

// ManagedEvent contains only the event fields rendered by the TUI.
type ManagedEvent struct {
	Kind        controller.EventKind
	OperationID string
	State       controller.ConnectionState
	Progress    controller.ProgressStage
	Err         error
	StreamID    string
	Epoch       uint64
	Sequence    uint64
}

// ManagedUpdate is emitted for service-owned changes, including resyncs.
type ManagedUpdate struct {
	Snapshot *ManagedSnapshot
	Event    *ManagedEvent
}

// ManagedOperationResult is returned only after a terminal operation event and
// a final snapshot. Command acceptance alone never produces this result.
type ManagedOperationResult struct {
	OperationID string
	Snapshot    ManagedSnapshot
}

// ManagedBackend is the injectable service-owned connection boundary used by
// the TUI. Implementations own subscription and transport lifetimes.
type ManagedBackend interface {
	Start(context.Context) (ManagedSnapshot, error)
	NextUpdate(context.Context) (ManagedUpdate, error)
	Connect(context.Context, ManagedSelection) (ManagedOperationResult, error)
	Switch(context.Context, ManagedSelection) (ManagedOperationResult, error)
	Disconnect(context.Context) (ManagedOperationResult, error)
	Close() error
}

type managedCommandClient interface {
	GetReadModel(context.Context, string, time.Time) (v2.Snapshot, v2.StreamCursor, error)
	ConnectSelection(context.Context, string, string, time.Time, v2.SelectionPayload) (string, error)
	SwitchSelection(context.Context, string, string, time.Time, v2.SelectionPayload) (string, error)
	Disconnect(context.Context, string, string, time.Time) (string, error)
	Close() error
}

type managedSubscription interface {
	Next(context.Context) (controller.Event, error)
	Close() error
}

type managedEventClient interface {
	Subscribe(context.Context, string, time.Time, v2.StreamCursor) (managedSubscription, error)
	Close() error
}

type v2ManagedEventClient struct {
	client *v2.Client
}

func (c v2ManagedEventClient) Subscribe(ctx context.Context, requestID string, deadline time.Time, cursor v2.StreamCursor) (managedSubscription, error) {
	return c.client.Subscribe(ctx, requestID, deadline, cursor)
}

func (c v2ManagedEventClient) Close() error { return c.client.Close() }

type managedV2Connector interface {
	Command(context.Context) (managedCommandClient, v2.ServerHello, error)
	Event(context.Context) (managedEventClient, v2.ServerHello, error)
}

type unixV2Connector struct {
	socketPath string
	dial       func(context.Context, string) (io.ReadWriteCloser, error)
}

func (c unixV2Connector) Command(ctx context.Context) (managedCommandClient, v2.ServerHello, error) {
	client, hello, err := c.connect(ctx, v2.ChannelCommand)
	return client, hello, err
}

func (c unixV2Connector) Event(ctx context.Context) (managedEventClient, v2.ServerHello, error) {
	client, hello, err := c.connect(ctx, v2.ChannelEvent)
	if err != nil {
		return nil, hello, err
	}
	return v2ManagedEventClient{client: client}, hello, nil
}

func (c unixV2Connector) connect(ctx context.Context, channel v2.Channel) (*v2.Client, v2.ServerHello, error) {
	connection, err := c.dial(ctx, c.socketPath)
	if err != nil {
		return nil, v2.ServerHello{}, fmt.Errorf("connect to managed service: %w", err)
	}
	capabilities := append(v2.DefaultCapabilities(), v2.CapabilityConnectionCatalog)
	client, err := v2.NewClient(connection, v2.ClientConfig{
		Capabilities:         capabilities,
		RequiredCapabilities: []v2.Capability{v2.CapabilityConnectionCatalog},
	})
	if err != nil {
		_ = connection.Close()
		return nil, v2.ServerHello{}, err
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, managedRequestTimeout)
	defer cancel()
	hello, err := client.Handshake(handshakeCtx, channel)
	if err != nil {
		_ = client.Close()
		return nil, hello, normalizeManagedError(err)
	}
	return client, hello, nil
}

// NewDarwinManagedBackend creates the only production macOS TUI backend. Its
// endpoint is fixed and it has no legacy or direct fallback.
func NewDarwinManagedBackend() ManagedBackend {
	return newV2ManagedBackend(unixV2Connector{
		socketPath: v2.DefaultDarwinSocketPath,
		dial: func(ctx context.Context, path string) (io.ReadWriteCloser, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", path)
		},
	})
}

type operationTerminal struct {
	err error
}

type v2ManagedBackend struct {
	connector managedV2Connector

	ctx    context.Context
	cancel context.CancelFunc

	commandMu           sync.Mutex
	recoveryMu          sync.Mutex
	mu                  sync.Mutex
	command             managedCommandClient
	events              managedEventClient
	sub                 managedSubscription
	stream              v2.StreamIdentity
	transportGeneration uint64
	started             bool
	closed              bool
	updates             chan ManagedUpdate

	terminalMu sync.Mutex
	waiters    map[string]chan operationTerminal
	terminals  map[string]operationTerminal
}

func newV2ManagedBackend(connector managedV2Connector) *v2ManagedBackend {
	return &v2ManagedBackend{
		connector: connector,
		updates:   make(chan ManagedUpdate, 64),
		waiters:   make(map[string]chan operationTerminal),
		terminals: make(map[string]operationTerminal),
	}
}

func (b *v2ManagedBackend) Start(parent context.Context) (ManagedSnapshot, error) {
	if parent == nil {
		return ManagedSnapshot{}, errors.New("managed backend context is required")
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ManagedSnapshot{}, errors.New("managed backend is closed")
	}
	if b.started {
		b.mu.Unlock()
		return b.readSnapshot(parent)
	}
	previousCancel := b.cancel
	b.ctx, b.cancel = context.WithCancel(parent)
	b.mu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}

	snapshot, err := b.establish(b.ctx)
	if err != nil {
		return ManagedSnapshot{}, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ManagedSnapshot{}, errors.New("managed backend is closed")
	}
	b.started = true
	b.mu.Unlock()
	go b.eventLoop()
	return snapshot, nil
}

func (b *v2ManagedBackend) establish(ctx context.Context) (ManagedSnapshot, error) {
	b.commandMu.Lock()
	defer b.commandMu.Unlock()

	commandClient, _, err := b.connector.Command(ctx)
	if err != nil {
		return ManagedSnapshot{}, err
	}
	cleanupCommand := true
	defer func() {
		if cleanupCommand {
			_ = commandClient.Close()
		}
	}()

	wireSnapshot, cursor, err := commandClient.GetReadModel(ctx, mustManagedID("snapshot"), time.Now().Add(managedRequestTimeout))
	if err != nil {
		return ManagedSnapshot{}, normalizeManagedError(err)
	}
	eventClient, eventHello, err := b.connector.Event(ctx)
	if err != nil {
		return ManagedSnapshot{}, err
	}
	cleanupEvents := true
	defer func() {
		if cleanupEvents {
			_ = eventClient.Close()
		}
	}()
	if eventHello.StreamIdentity() != cursor.StreamIdentity {
		return ManagedSnapshot{}, fmt.Errorf("managed event stream changed before subscription: %w", &v2.WireError{Code: v2.ErrorCodeResyncRequired})
	}
	subscription, err := eventClient.Subscribe(ctx, mustManagedID("events"), time.Now().Add(managedStreamLifetime), cursor)
	if err != nil {
		return ManagedSnapshot{}, normalizeManagedError(err)
	}

	b.mu.Lock()
	oldSub, oldEvents, oldCommand := b.sub, b.events, b.command
	b.transportGeneration++
	b.sub, b.events, b.command, b.stream = subscription, eventClient, commandClient, cursor.StreamIdentity
	b.mu.Unlock()
	if oldSub != nil {
		_ = oldSub.Close()
	} else if oldEvents != nil {
		_ = oldEvents.Close()
	}
	if oldCommand != nil {
		_ = oldCommand.Close()
	}
	cleanupCommand = false
	cleanupEvents = false
	return projectManagedSnapshot(wireSnapshot), nil
}

func (b *v2ManagedBackend) eventLoop() {
	for {
		b.mu.Lock()
		ctx, sub, stream, transportGeneration, closed := b.ctx, b.sub, b.stream, b.transportGeneration, b.closed
		b.mu.Unlock()
		if closed || ctx == nil || sub == nil {
			return
		}
		event, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.mu.Lock()
			staleTransport := transportGeneration != b.transportGeneration
			b.mu.Unlock()
			if staleTransport {
				continue
			}
			cause := fmt.Errorf("managed event stream interrupted before terminal result: %w", normalizeManagedError(err))
			snapshot, resyncErr := b.recoverConnections(ctx, transportGeneration, cause)
			if resyncErr != nil {
				b.publish(ManagedUpdate{Event: &ManagedEvent{Err: resyncErr}})
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				continue
			}
			b.publish(ManagedUpdate{Snapshot: &snapshot})
			continue
		}

		projected := projectManagedEvent(event, stream)
		if event.Kind == controller.EventOperationSucceeded || event.Kind == controller.EventOperationFailed {
			terminal := operationTerminal{}
			if event.Error != nil {
				terminal.err = normalizeManagedError(event.Error)
			}
			b.completeWaiter(string(event.OperationID), terminal)
		}
		b.publish(ManagedUpdate{Event: &projected})
		if event.Kind == controller.EventSnapshotChanged {
			snapshot, snapshotErr := b.readSnapshot(ctx)
			if snapshotErr != nil && shouldRecoverManagedCommand(snapshotErr) {
				snapshot, snapshotErr = b.recoverConnections(ctx, transportGeneration, snapshotErr)
			}
			if snapshotErr != nil {
				b.publish(ManagedUpdate{Event: &ManagedEvent{Err: snapshotErr, StreamID: stream.StreamID, Epoch: stream.Epoch}})
			} else {
				b.publish(ManagedUpdate{Snapshot: &snapshot})
			}
		}
	}
}

func (b *v2ManagedBackend) publish(update ManagedUpdate) {
	b.mu.Lock()
	ctx, closed := b.ctx, b.closed
	b.mu.Unlock()
	if closed || ctx == nil {
		return
	}
	select {
	case b.updates <- update:
	case <-ctx.Done():
	}
}

func (b *v2ManagedBackend) NextUpdate(ctx context.Context) (ManagedUpdate, error) {
	select {
	case update := <-b.updates:
		return update, nil
	case <-ctx.Done():
		return ManagedUpdate{}, ctx.Err()
	}
}

func (b *v2ManagedBackend) Connect(ctx context.Context, selection ManagedSelection) (ManagedOperationResult, error) {
	return b.mutate(ctx, "connect", selection, func(client managedCommandClient, requestID, key string, deadline time.Time) (string, error) {
		return client.ConnectSelection(ctx, requestID, key, deadline, v2.SelectionPayload{GroupID: selection.GroupID, ExitNodeID: selection.ExitNodeID})
	})
}

func (b *v2ManagedBackend) Switch(ctx context.Context, selection ManagedSelection) (ManagedOperationResult, error) {
	return b.mutate(ctx, "switch", selection, func(client managedCommandClient, requestID, key string, deadline time.Time) (string, error) {
		return client.SwitchSelection(ctx, requestID, key, deadline, v2.SelectionPayload{GroupID: selection.GroupID, ExitNodeID: selection.ExitNodeID})
	})
}

func (b *v2ManagedBackend) Disconnect(ctx context.Context) (ManagedOperationResult, error) {
	return b.mutate(ctx, "disconnect", ManagedSelection{}, func(client managedCommandClient, requestID, key string, deadline time.Time) (string, error) {
		return client.Disconnect(ctx, requestID, key, deadline)
	})
}

func (b *v2ManagedBackend) mutate(
	parent context.Context,
	kind string,
	selection ManagedSelection,
	accept func(managedCommandClient, string, string, time.Time) (string, error),
) (ManagedOperationResult, error) {
	if kind != "disconnect" && selection.GroupID == "" {
		return ManagedOperationResult{}, errors.New("a concrete project is required")
	}
	ctx, cancel := context.WithTimeout(parent, managedOperationTimeout)
	defer cancel()

	b.commandMu.Lock()
	b.mu.Lock()
	client, started, closed, transportGeneration := b.command, b.started, b.closed, b.transportGeneration
	b.mu.Unlock()
	if !started || closed || client == nil {
		b.commandMu.Unlock()
		return ManagedOperationResult{}, errors.New("managed service is not ready")
	}
	requestID, err := newManagedID(kind + "-request")
	if err != nil {
		b.commandMu.Unlock()
		return ManagedOperationResult{}, err
	}
	key, err := newManagedID(kind + "-intent")
	if err != nil {
		b.commandMu.Unlock()
		return ManagedOperationResult{}, err
	}
	operationID, err := accept(client, requestID, key, time.Now().Add(managedRequestTimeout))
	b.commandMu.Unlock()
	if err != nil {
		normalized := normalizeManagedError(err)
		if shouldRecoverManagedCommand(normalized) {
			snapshot, recoveryErr := b.recoverConnections(ctx, transportGeneration, normalized)
			if recoveryErr == nil {
				b.publish(ManagedUpdate{Snapshot: &snapshot})
			}
			return ManagedOperationResult{}, errors.Join(normalized, recoveryErr)
		}
		return ManagedOperationResult{}, normalized
	}

	waiter := b.operationWaiter(operationID)
	var terminal operationTerminal
	select {
	case terminal = <-waiter:
	case <-ctx.Done():
		b.removeWaiter(operationID, waiter)
		return ManagedOperationResult{OperationID: operationID}, fmt.Errorf("wait for managed %s operation %s: %w", kind, operationID, ctx.Err())
	}

	finalCtx, finalCancel := context.WithTimeout(context.WithoutCancel(parent), managedRequestTimeout)
	defer finalCancel()
	finalSnapshot, snapshotErr := b.readSnapshot(finalCtx)
	if snapshotErr != nil && shouldRecoverManagedCommand(snapshotErr) {
		b.mu.Lock()
		currentGeneration := b.transportGeneration
		b.mu.Unlock()
		finalSnapshot, snapshotErr = b.recoverConnections(finalCtx, currentGeneration, snapshotErr)
		if snapshotErr == nil {
			b.publish(ManagedUpdate{Snapshot: &finalSnapshot})
		}
	}
	result := ManagedOperationResult{OperationID: operationID, Snapshot: finalSnapshot}
	if terminal.err != nil || snapshotErr != nil {
		return result, errors.Join(terminal.err, snapshotErr)
	}
	return result, nil
}

func (b *v2ManagedBackend) readSnapshot(ctx context.Context) (ManagedSnapshot, error) {
	b.commandMu.Lock()
	defer b.commandMu.Unlock()
	b.mu.Lock()
	client := b.command
	b.mu.Unlock()
	if client == nil {
		return ManagedSnapshot{}, errors.New("managed command channel is unavailable")
	}
	wireSnapshot, _, err := client.GetReadModel(ctx, mustManagedID("snapshot"), time.Now().Add(managedRequestTimeout))
	if err != nil {
		return ManagedSnapshot{}, normalizeManagedError(err)
	}
	return projectManagedSnapshot(wireSnapshot), nil
}

func (b *v2ManagedBackend) recoverConnections(ctx context.Context, expectedGeneration uint64, cause error) (ManagedSnapshot, error) {
	b.recoveryMu.Lock()
	defer b.recoveryMu.Unlock()

	b.mu.Lock()
	currentGeneration, closed := b.transportGeneration, b.closed
	b.mu.Unlock()
	if closed {
		return ManagedSnapshot{}, errors.New("managed backend is closed")
	}
	if currentGeneration != expectedGeneration {
		return b.readSnapshot(ctx)
	}
	b.failWaiters(fmt.Errorf("managed transport replaced before terminal result: %w", cause))
	snapshot, err := b.establish(ctx)
	if err != nil {
		return ManagedSnapshot{}, errors.Join(cause, err)
	}
	return snapshot, nil
}

func shouldRecoverManagedCommand(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var wireErr *v2.WireError
	if !errors.As(err, &wireErr) {
		return !errors.Is(err, ErrManagedAuthRequired) && !errors.Is(err, ErrManagedUpdateRequired)
	}
	switch wireErr.Code {
	case v2.ErrorCodeReauthRequired, v2.ErrorCodeUnsupportedVersion,
		v2.ErrorCodeUnauthorized, v2.ErrorCodeInvalidArgument,
		v2.ErrorCodeConflict, v2.ErrorCodeDegraded:
		return false
	default:
		return true
	}
}

func (b *v2ManagedBackend) operationWaiter(operationID string) <-chan operationTerminal {
	b.terminalMu.Lock()
	defer b.terminalMu.Unlock()
	waiter := make(chan operationTerminal, 1)
	if terminal, ok := b.terminals[operationID]; ok {
		delete(b.terminals, operationID)
		waiter <- terminal
		return waiter
	}
	b.waiters[operationID] = waiter
	return waiter
}

func (b *v2ManagedBackend) completeWaiter(operationID string, terminal operationTerminal) {
	b.terminalMu.Lock()
	defer b.terminalMu.Unlock()
	if waiter, ok := b.waiters[operationID]; ok {
		delete(b.waiters, operationID)
		waiter <- terminal
		return
	}
	b.terminals[operationID] = terminal
	if len(b.terminals) > 128 {
		for staleID := range b.terminals {
			if staleID != operationID {
				delete(b.terminals, staleID)
				break
			}
		}
	}
}

func (b *v2ManagedBackend) removeWaiter(operationID string, target <-chan operationTerminal) {
	b.terminalMu.Lock()
	defer b.terminalMu.Unlock()
	if waiter, ok := b.waiters[operationID]; ok && waiter == target {
		delete(b.waiters, operationID)
	}
}

func (b *v2ManagedBackend) failWaiters(err error) {
	b.terminalMu.Lock()
	defer b.terminalMu.Unlock()
	for operationID, waiter := range b.waiters {
		delete(b.waiters, operationID)
		waiter <- operationTerminal{err: err}
	}
	clear(b.terminals)
}

func (b *v2ManagedBackend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	cancel, sub, events, command := b.cancel, b.sub, b.events, b.command
	b.sub, b.events, b.command = nil, nil, nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	b.failWaiters(errors.New("managed backend closed before terminal result"))
	if sub != nil {
		return errors.Join(sub.Close(), commandClose(command))
	}
	return errors.Join(commandClose(events), commandClose(command))
}

func commandClose(client interface{ Close() error }) error {
	if client == nil {
		return nil
	}
	return client.Close()
}

func projectManagedSnapshot(snapshot v2.Snapshot) ManagedSnapshot {
	result := ManagedSnapshot{
		State:            snapshot.State,
		Desired:          ManagedSelection{GroupID: snapshot.Desired.GroupID, ExitNodeID: snapshot.Desired.ExitNodeID},
		DesiredConnected: snapshot.Desired.Connected,
		Applied:          ManagedSelection{GroupID: snapshot.Applied.GroupID, ExitNodeID: snapshot.Applied.ExitNodeID},
		AppliedConnected: snapshot.Applied.Connected,
		Health: controller.Health{
			Healthy:   snapshot.Health.Healthy,
			WireGuard: snapshot.Health.WireGuard,
			Routes:    snapshot.Health.Routes,
			DNS:       snapshot.Health.DNS,
			EndToEnd:  snapshot.Health.EndToEnd,
		},
		Sequence: snapshot.Sequence,
		StreamID: snapshot.StreamID,
		Epoch:    snapshot.Epoch,
	}
	if snapshot.ActiveOperation != nil {
		result.ActiveOperationID = snapshot.ActiveOperation.ID
	}
	if snapshot.Catalog == nil {
		return result
	}
	result.HasOverlap = snapshot.Catalog.HasOverlap
	result.VPNMode = snapshot.Catalog.VPNMode
	for _, project := range snapshot.Catalog.Projects {
		mapped := ManagedProject{
			ID: project.GroupID, Name: project.Name, Description: project.Description,
			CIDRs: append([]string(nil), project.CIDRs...), OnlineResources: project.OnlineResources,
		}
		for _, resource := range project.Resources {
			mapped.Resources = append(mapped.Resources, ManagedResource{
				ID: resource.PublisherID, Name: resource.Name, Status: resource.Status,
				ExposedCIDRs: append([]string(nil), resource.ExposedCIDRs...),
			})
		}
		result.Projects = append(result.Projects, mapped)
	}
	for _, node := range snapshot.Catalog.ExitNodes {
		result.ExitNodes = append(result.ExitNodes, ManagedExitNode{
			ID: node.ExitNodeID, Name: node.Name, Location: node.Location, Status: node.Status,
		})
	}
	return result
}

func projectManagedEvent(event controller.Event, stream v2.StreamIdentity) ManagedEvent {
	result := ManagedEvent{
		Kind: event.Kind, OperationID: string(event.OperationID), State: event.State,
		StreamID: stream.StreamID, Epoch: stream.Epoch, Sequence: uint64(event.Sequence),
	}
	if event.Progress != nil {
		result.Progress = event.Progress.Stage
	}
	if event.Error != nil {
		result.Err = normalizeManagedError(event.Error)
	}
	return result
}

func normalizeManagedError(err error) error {
	if err == nil {
		return nil
	}
	var wireErr *v2.WireError
	if errors.As(err, &wireErr) {
		switch wireErr.Code {
		case v2.ErrorCodeReauthRequired:
			return fmt.Errorf("%w: %v", ErrManagedAuthRequired, err)
		case v2.ErrorCodeUnsupportedVersion:
			return fmt.Errorf("%w: %v", ErrManagedUpdateRequired, err)
		}
	}
	if structured, ok := controller.AsError(err); ok {
		switch structured.Code {
		case controller.ErrorCodeReauthRequired:
			return fmt.Errorf("%w: %v", ErrManagedAuthRequired, err)
		case controller.ErrorCodeUnsupportedVersion:
			return fmt.Errorf("%w: %v", ErrManagedUpdateRequired, err)
		}
	}
	return err
}

func mustManagedID(prefix string) string {
	id, err := newManagedID(prefix)
	if err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return id
}

func newManagedID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate managed %s ID: %w", prefix, err)
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}

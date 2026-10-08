package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wireztna/client/desktop/controller"
	v2 "github.com/wireztna/client/internal/ipc/v2"
)

type fakeManagedCommandClient struct {
	mu            sync.Mutex
	steps         *[]string
	snapshots     []v2.Snapshot
	snapshotCalls int
	connectCalls  int
	selection     v2.SelectionPayload
	operationID   string
	connectErr    error
}

func (f *fakeManagedCommandClient) GetReadModel(context.Context, string, time.Time) (v2.Snapshot, v2.StreamCursor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.steps = append(*f.steps, "snapshot")
	index := f.snapshotCalls
	if index >= len(f.snapshots) {
		index = len(f.snapshots) - 1
	}
	f.snapshotCalls++
	snapshot := f.snapshots[index]
	return snapshot, snapshot.Cursor(), nil
}

func (f *fakeManagedCommandClient) ConnectSelection(_ context.Context, _, _ string, _ time.Time, selection v2.SelectionPayload) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.steps = append(*f.steps, "connect")
	f.connectCalls++
	f.selection = selection
	return f.operationID, f.connectErr
}

func (f *fakeManagedCommandClient) SwitchSelection(context.Context, string, string, time.Time, v2.SelectionPayload) (string, error) {
	return "switch-operation", nil
}

func (f *fakeManagedCommandClient) Disconnect(context.Context, string, string, time.Time) (string, error) {
	return "disconnect-operation", nil
}

func (f *fakeManagedCommandClient) Close() error { return nil }

type fakeManagedSubscription struct {
	events chan controller.Event
	closed chan struct{}
	once   sync.Once
}

func (f *fakeManagedSubscription) Next(ctx context.Context) (controller.Event, error) {
	select {
	case event := <-f.events:
		return event, nil
	case <-f.closed:
		return controller.Event{}, errors.New("closed")
	case <-ctx.Done():
		return controller.Event{}, ctx.Err()
	}
}

func (f *fakeManagedSubscription) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

type fakeManagedEventClient struct {
	steps *[]string
	sub   managedSubscription
}

func (f *fakeManagedEventClient) Subscribe(context.Context, string, time.Time, v2.StreamCursor) (managedSubscription, error) {
	*f.steps = append(*f.steps, "subscribe")
	return f.sub, nil
}

func (f *fakeManagedEventClient) Close() error { return nil }

type fakeManagedConnector struct {
	mu       sync.Mutex
	steps    *[]string
	command  managedCommandClient
	commands []managedCommandClient
	events   managedEventClient
	eventSet []managedEventClient
	hello    v2.ServerHello
	calls    int
}

func (f *fakeManagedConnector) Command(context.Context) (managedCommandClient, v2.ServerHello, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.steps = append(*f.steps, "command-channel")
	client := f.command
	if len(f.commands) > 0 {
		index := f.calls
		if index >= len(f.commands) {
			index = len(f.commands) - 1
		}
		client = f.commands[index]
	}
	f.calls++
	return client, f.hello, nil
}

func (f *fakeManagedConnector) Event(context.Context) (managedEventClient, v2.ServerHello, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.steps = append(*f.steps, "event-channel")
	client := f.events
	if len(f.eventSet) > 0 {
		index := f.calls - 1
		if index >= len(f.eventSet) {
			index = len(f.eventSet) - 1
		}
		client = f.eventSet[index]
	}
	return client, f.hello, nil
}

func managedWireSnapshot(state controller.ConnectionState, sequence uint64) v2.Snapshot {
	connected := state == controller.ConnectionStateConnected
	return v2.Snapshot{
		State: state,
		Desired: v2.DesiredState{
			ConfigurationID: "configuration", Generation: 1, GroupID: "engineering", Connected: connected,
		},
		Applied: v2.AppliedState{
			ConfigurationID: "configuration", Generation: 1, GroupID: "engineering", Connected: connected,
		},
		Health: v2.Health{
			Healthy:   connected,
			WireGuard: map[bool]controller.HealthStatus{true: controller.HealthHealthy, false: controller.HealthUnknown}[connected],
			Routes:    map[bool]controller.HealthStatus{true: controller.HealthHealthy, false: controller.HealthUnknown}[connected],
			DNS:       map[bool]controller.HealthStatus{true: controller.HealthHealthy, false: controller.HealthUnknown}[connected],
			EndToEnd:  map[bool]controller.HealthStatus{true: controller.HealthHealthy, false: controller.HealthUnknown}[connected],
		},
		Catalog: &v2.ConnectionCatalog{
			ConfigurationID: "configuration",
			Projects:        []v2.CatalogProject{{GroupID: "engineering", Name: "Engineering", OnlineResources: 1}},
		},
		StreamID: "stream", Epoch: 1, Sequence: sequence,
	}
}

func TestV2ManagedBackendSubscribesBeforeMutationAndWaitsForTerminal(t *testing.T) {
	steps := []string{}
	subscription := &fakeManagedSubscription{events: make(chan controller.Event, 2), closed: make(chan struct{})}
	command := &fakeManagedCommandClient{
		steps:       &steps,
		snapshots:   []v2.Snapshot{managedWireSnapshot(controller.ConnectionStateDisconnected, 0), managedWireSnapshot(controller.ConnectionStateConnected, 2)},
		operationID: "operation-1",
	}
	hello := v2.ServerHello{StreamID: "stream", Epoch: 1}
	backend := newV2ManagedBackend(&fakeManagedConnector{
		steps: &steps, command: command,
		events: &fakeManagedEventClient{steps: &steps, sub: subscription}, hello: hello,
	})
	defer backend.Close()

	if _, err := backend.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	resultChannel := make(chan ManagedOperationResult, 1)
	errorChannel := make(chan error, 1)
	go func() {
		result, err := backend.Connect(context.Background(), ManagedSelection{GroupID: "engineering"})
		resultChannel <- result
		errorChannel <- err
	}()

	deadline := time.Now().Add(time.Second)
	for {
		command.mu.Lock()
		accepted := command.connectCalls == 1
		command.mu.Unlock()
		if accepted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connect was not accepted")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-resultChannel:
		t.Fatal("Connect returned after acceptance without a terminal event")
	case <-time.After(25 * time.Millisecond):
	}

	subscription.events <- controller.Event{
		Sequence: 1, Kind: controller.EventOperationSucceeded,
		OperationID: controller.OperationID("another-operation"), State: controller.ConnectionStateConnected,
	}
	select {
	case <-resultChannel:
		t.Fatal("Connect returned for a terminal event with another operation ID")
	case <-time.After(25 * time.Millisecond):
	}

	subscription.events <- controller.Event{
		Sequence: 2, Kind: controller.EventOperationSucceeded,
		OperationID: controller.OperationID("operation-1"), State: controller.ConnectionStateConnected,
	}
	result := <-resultChannel
	if err := <-errorChannel; err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if result.OperationID != "operation-1" || result.Snapshot.State != controller.ConnectionStateConnected {
		t.Fatalf("Connect() result = %#v", result)
	}
	command.mu.Lock()
	defer command.mu.Unlock()
	if command.snapshotCalls != 2 {
		t.Fatalf("snapshot calls = %d, want initial and final", command.snapshotCalls)
	}
	wantSteps := []string{"command-channel", "snapshot", "event-channel", "subscribe", "connect", "snapshot"}
	if len(steps) != len(wantSteps) {
		t.Fatalf("steps = %#v, want %#v", steps, wantSteps)
	}
	for index := range wantSteps {
		if steps[index] != wantSteps[index] {
			t.Fatalf("steps = %#v, want %#v", steps, wantSteps)
		}
	}
	if command.selection.GroupID != "engineering" {
		t.Fatalf("selection = %#v", command.selection)
	}
}

func TestV2ManagedBackendRejectsSelectionWithoutConcreteGroup(t *testing.T) {
	backend := newV2ManagedBackend(&fakeManagedConnector{})
	_, err := backend.Connect(context.Background(), ManagedSelection{ExitNodeID: "exit-1"})
	if err == nil {
		t.Fatal("Connect accepted an exit node without GroupID")
	}
}

func TestDarwinManagedBackendUsesOnlyFixedV2Socket(t *testing.T) {
	backend := NewDarwinManagedBackend()
	implementation, ok := backend.(*v2ManagedBackend)
	if !ok {
		t.Fatalf("backend type = %T", backend)
	}
	connector, ok := implementation.connector.(unixV2Connector)
	if !ok {
		t.Fatalf("connector type = %T", implementation.connector)
	}
	if connector.socketPath != v2.DefaultDarwinSocketPath {
		t.Fatalf("socket = %q, want %q", connector.socketPath, v2.DefaultDarwinSocketPath)
	}
}

func TestV2ManagedBackendRecoversBothChannelsAfterCommandFailure(t *testing.T) {
	steps := []string{}
	firstSubscription := &fakeManagedSubscription{events: make(chan controller.Event), closed: make(chan struct{})}
	secondSubscription := &fakeManagedSubscription{events: make(chan controller.Event), closed: make(chan struct{})}
	firstCommand := &fakeManagedCommandClient{
		steps: &steps, snapshots: []v2.Snapshot{managedWireSnapshot(controller.ConnectionStateDisconnected, 0)},
		connectErr: errors.New("broken command connection"),
	}
	secondCommand := &fakeManagedCommandClient{
		steps: &steps, snapshots: []v2.Snapshot{managedWireSnapshot(controller.ConnectionStateDisconnected, 0)},
	}
	connector := &fakeManagedConnector{
		steps:    &steps,
		commands: []managedCommandClient{firstCommand, secondCommand},
		eventSet: []managedEventClient{
			&fakeManagedEventClient{steps: &steps, sub: firstSubscription},
			&fakeManagedEventClient{steps: &steps, sub: secondSubscription},
		},
		hello: v2.ServerHello{StreamID: "stream", Epoch: 1},
	}
	backend := newV2ManagedBackend(connector)
	defer backend.Close()
	if _, err := backend.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if _, err := backend.Connect(context.Background(), ManagedSelection{GroupID: "engineering"}); err == nil || !strings.Contains(err.Error(), "broken command connection") {
		t.Fatalf("Connect() error = %v", err)
	}
	connector.mu.Lock()
	commandChannelCalls := connector.calls
	connector.mu.Unlock()
	if commandChannelCalls != 2 {
		t.Fatalf("command channels = %d, want initial plus recovered", commandChannelCalls)
	}
	backend.mu.Lock()
	activeCommand := backend.command
	generation := backend.transportGeneration
	backend.mu.Unlock()
	if activeCommand != secondCommand || generation != 2 {
		t.Fatalf("recovered transport = command %T generation %d", activeCommand, generation)
	}
}

func TestV2ManagedBackendReturnsOperationFailureAfterFinalSnapshot(t *testing.T) {
	steps := []string{}
	subscription := &fakeManagedSubscription{events: make(chan controller.Event, 1), closed: make(chan struct{})}
	command := &fakeManagedCommandClient{
		steps: &steps,
		snapshots: []v2.Snapshot{
			managedWireSnapshot(controller.ConnectionStateDisconnected, 0),
			managedWireSnapshot(controller.ConnectionStateDisconnected, 2),
		},
		operationID: "failed-operation",
	}
	backend := newV2ManagedBackend(&fakeManagedConnector{
		steps: &steps, command: command,
		events: &fakeManagedEventClient{steps: &steps, sub: subscription},
		hello:  v2.ServerHello{StreamID: "stream", Epoch: 1},
	})
	defer backend.Close()
	if _, err := backend.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	resultChannel := make(chan ManagedOperationResult, 1)
	errorChannel := make(chan error, 1)
	go func() {
		result, err := backend.Connect(context.Background(), ManagedSelection{GroupID: "engineering"})
		resultChannel <- result
		errorChannel <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		command.mu.Lock()
		accepted := command.connectCalls == 1
		command.mu.Unlock()
		if accepted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connect was not accepted")
		}
		time.Sleep(time.Millisecond)
	}
	subscription.events <- controller.Event{
		Sequence: 2, Kind: controller.EventOperationFailed,
		OperationID: controller.OperationID("failed-operation"), State: controller.ConnectionStateDisconnected,
		Error: &controller.Error{Code: controller.ErrorCodeDegraded, Detail: "route verification failed"},
	}
	result := <-resultChannel
	err := <-errorChannel
	if err == nil || !strings.Contains(err.Error(), "route verification failed") {
		t.Fatalf("Connect() error = %v", err)
	}
	if result.OperationID != "failed-operation" || result.Snapshot.State != controller.ConnectionStateDisconnected {
		t.Fatalf("failed operation result = %#v", result)
	}
	command.mu.Lock()
	defer command.mu.Unlock()
	if command.snapshotCalls != 2 {
		t.Fatalf("snapshot calls = %d, want final snapshot after failure", command.snapshotCalls)
	}
}

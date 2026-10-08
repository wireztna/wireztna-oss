package v2

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/wireztna/client/desktop/controller"
)

type catalogTestBackend struct {
	mu       sync.Mutex
	snapshot controller.Snapshot
	commands []controller.Command
}

func (b *catalogTestBackend) Execute(_ context.Context, command controller.Command) (controller.OperationID, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.commands = append(b.commands, command)
	return controller.OperationID("operation-" + string(rune('0'+len(b.commands)))), nil
}

func (b *catalogTestBackend) Snapshot(context.Context) (controller.Snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshot, nil
}

func (*catalogTestBackend) Subscribe(context.Context, controller.Sequence) (<-chan controller.Event, error) {
	events := make(chan controller.Event)
	close(events)
	return events, nil
}

func (b *catalogTestBackend) recordedCommands() []controller.Command {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]controller.Command(nil), b.commands...)
}

type catalogTestProvider struct {
	mu      sync.Mutex
	catalog ConnectionCatalog
	calls   int
}

func (p *catalogTestProvider) ConnectionCatalog(context.Context) (ConnectionCatalog, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.catalog, nil
}

func (p *catalogTestProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestServerFreshSnapshotFirstConnectIsCatalogValidatedStampedAndIdempotent(t *testing.T) {
	backend := &catalogTestBackend{snapshot: controller.Snapshot{
		State: controller.ConnectionStateDisconnected,
		Health: controller.Health{
			WireGuard: controller.HealthUnknown,
			Routes:    controller.HealthUnknown,
			DNS:       controller.HealthUnknown,
			EndToEnd:  controller.HealthUnknown,
		},
	}}
	provider := &catalogTestProvider{catalog: ConnectionCatalog{
		ConfigurationID: "wg-public-sha256:stable",
		Projects: []CatalogProject{{
			GroupID: "group-a", Name: "Project A", CIDRs: []string{"10.10.0.0/16"},
			Resources: []CatalogResource{{PublisherID: "exit-a", Name: "Exit A", Status: "online", ExposedCIDRs: []string{"10.10.1.0/24"}}},
		}},
		ExitNodes: []CatalogExitNode{{ExitNodeID: "exit-a", Name: "Exit A", Status: "online"}},
		VPNMode:   true,
	}}
	owner := NewExpectedUIDOwner(501)
	authorizer := NewAuthorizer(NewCommandRegistry(), map[CommandClass]AuthorizationPolicy{
		CommandClassOwnerControl: ExactIdentityPolicy(owner),
		CommandClassRead:         ExactIdentityPolicy(owner),
	})
	idempotency, err := NewIdempotencyStore(time.Minute, 16)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(backend, authorizer, idempotency, ServerConfig{
		ServiceVersion:        "test",
		ExpectedOwnerIdentity: owner,
		StreamIdentity:        StreamIdentity{StreamID: "stream-a", Epoch: 1},
		CatalogProvider:       provider,
	})
	if err != nil {
		t.Fatal(err)
	}

	serverConn, clientConn := net.Pipe()
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(context.Background(), serverConn, newUIDPeerAuthentication(501))
	}()
	client, err := NewClient(clientConn, ClientConfig{
		Capabilities:         append(DefaultCapabilities(), CapabilityConnectionCatalog),
		RequiredCapabilities: []Capability{CapabilityConnectionCatalog},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = client.Close()
		if err := <-serveResult; err != nil && !errors.Is(err, io.EOF) {
			t.Errorf("Server.Serve() error = %v", err)
		}
	}()
	hello, err := client.Handshake(context.Background(), ChannelCommand)
	if err != nil {
		t.Fatal(err)
	}
	if !containsCapability(hello.Capabilities, CapabilityConnectionCatalog) {
		t.Fatalf("negotiated capabilities = %#v", hello.Capabilities)
	}

	readModel, _, err := client.GetReadModel(context.Background(), "snapshot-1", time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if readModel.State != controller.ConnectionStateDisconnected || readModel.Applied != (AppliedState{}) {
		t.Fatalf("fresh applied truth changed: state=%q applied=%#v", readModel.State, readModel.Applied)
	}
	if readModel.Desired.ConfigurationID != provider.catalog.ConfigurationID || readModel.Desired.Generation != 1 || readModel.Desired.GroupID != "group-a" || readModel.Desired.Connected {
		t.Fatalf("fresh desired candidate = %#v", readModel.Desired)
	}
	if readModel.Catalog == nil || len(readModel.Catalog.Projects) != 1 || len(readModel.Catalog.Projects[0].Resources) != 1 || len(readModel.Catalog.ExitNodes) != 1 {
		t.Fatalf("catalog read model = %#v", readModel.Catalog)
	}

	selection := SelectionPayload{GroupID: "group-a", ExitNodeID: "exit-a"}
	operationID, err := client.ConnectSelection(context.Background(), "connect-1", "connect-key", time.Now().Add(time.Second), selection)
	if err != nil {
		t.Fatal(err)
	}
	replayedID, err := client.ConnectSelection(context.Background(), "connect-2", "connect-key", time.Now().Add(time.Second), selection)
	if err != nil {
		t.Fatal(err)
	}
	if replayedID != operationID {
		t.Fatalf("idempotent replay operation = %q, want %q", replayedID, operationID)
	}
	commands := backend.recordedCommands()
	if len(commands) != 1 {
		t.Fatalf("executed commands = %d, want 1", len(commands))
	}
	if commands[0].Kind != controller.CommandConnect || commands[0].Desired.ConfigurationID != controller.ConfigurationID(provider.catalog.ConfigurationID) || commands[0].Desired.Generation != 1 || commands[0].Desired.GroupID != "group-a" || commands[0].Desired.ExitNodeID != "exit-a" || !commands[0].Desired.Connected {
		t.Fatalf("stamped first connect = %#v", commands[0])
	}
	if provider.callCount() != 2 {
		t.Fatalf("catalog calls after replay = %d, want snapshot + first execution only", provider.callCount())
	}

	_, err = client.SwitchSelection(context.Background(), "switch-invalid-group", "switch-invalid-group-key", time.Now().Add(time.Second), SelectionPayload{GroupID: "group-missing"})
	var wireErr *WireError
	if !errors.As(err, &wireErr) || wireErr.Code != ErrorCodeInvalidArgument {
		t.Fatalf("invalid catalog group error = %v", err)
	}
	_, err = client.SwitchSelection(context.Background(), "switch-invalid-exit", "switch-invalid-exit-key", time.Now().Add(time.Second), SelectionPayload{GroupID: "group-a", ExitNodeID: "exit-missing"})
	wireErr = nil
	if !errors.As(err, &wireErr) || wireErr.Code != ErrorCodeInvalidArgument {
		t.Fatalf("invalid catalog exit node error = %v", err)
	}
	provider.mu.Lock()
	provider.catalog.Projects = append(provider.catalog.Projects, CatalogProject{
		GroupID: "group-b", Name: "Project B",
		Resources: []CatalogResource{{PublisherID: "publisher-b", Name: "Resource B", Status: "online"}},
	})
	provider.mu.Unlock()
	_, err = client.SwitchSelection(context.Background(), "switch-inaccessible-exit", "switch-inaccessible-exit-key", time.Now().Add(time.Second), SelectionPayload{GroupID: "group-b", ExitNodeID: "exit-a"})
	wireErr = nil
	if !errors.As(err, &wireErr) || wireErr.Code != ErrorCodeInvalidArgument {
		t.Fatalf("inaccessible group/exit pair error = %v", err)
	}
	if len(backend.recordedCommands()) != 1 {
		t.Fatal("invalid group/exit pair executed an operation")
	}
	_, err = client.SwitchSelection(context.Background(), "switch-1", "switch-key", time.Now().Add(time.Second), SelectionPayload{GroupID: "group-a"})
	if err != nil {
		t.Fatal(err)
	}
	commands = backend.recordedCommands()
	if len(commands) != 2 || commands[1].Desired.Generation != 2 || commands[1].Kind != controller.CommandSwitch {
		t.Fatalf("authoritative switch command = %#v", commands)
	}
	_, err = client.SwitchSelection(context.Background(), "switch-conflict", "switch-key", time.Now().Add(time.Second), selection)
	wireErr = nil
	if !errors.As(err, &wireErr) || wireErr.Code != ErrorCodeConflict {
		t.Fatalf("changed selection under same key error = %v", err)
	}
	if len(backend.recordedCommands()) != 2 {
		t.Fatal("conflicting idempotency replay executed another operation")
	}
}

func TestCatalogServerLegacyDesiredPayloadKeepsShapeButIgnoresClientRevision(t *testing.T) {
	backend := &catalogTestBackend{snapshot: controller.Snapshot{
		State: controller.ConnectionStateDisconnected,
		Health: controller.Health{
			WireGuard: controller.HealthUnknown,
			Routes:    controller.HealthUnknown,
			DNS:       controller.HealthUnknown,
			EndToEnd:  controller.HealthUnknown,
		},
	}}
	provider := &catalogTestProvider{catalog: ConnectionCatalog{
		ConfigurationID: "wg-public-sha256:service-owned",
		Projects:        []CatalogProject{{GroupID: "group-a"}},
	}}
	owner := NewExpectedUIDOwner(501)
	idempotency, err := NewIdempotencyStore(time.Minute, 4)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(backend, NewAuthorizer(NewCommandRegistry(), nil), idempotency, ServerConfig{
		ServiceVersion:        "test",
		ExpectedOwnerIdentity: owner,
		StreamIdentity:        StreamIdentity{StreamID: "stream-legacy", Epoch: 1},
		CatalogProvider:       provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalPayload(DesiredPayload{
		ConfigurationID: "client-invented",
		Generation:      999,
		GroupID:         "group-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Command: CommandConnect, Payload: payload}
	canonical, err := server.canonicalPayload(request, DefaultCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical, err := marshalPayload(SelectionPayload{GroupID: "group-a"})
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(wantCanonical) {
		t.Fatalf("canonical payload = %s, want %s", canonical, wantCanonical)
	}
	command, err := server.controllerCommand(context.Background(), request, DefaultCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	if command.Desired.ConfigurationID != "wg-public-sha256:service-owned" || command.Desired.Generation != 1 {
		t.Fatalf("legacy payload controlled stamped revision: %#v", command.Desired)
	}
}

func TestPristineSnapshotWithoutCatalogCapabilityRemainsLocal(t *testing.T) {
	backend := &catalogTestBackend{snapshot: controller.Snapshot{
		State: controller.ConnectionStateDisconnected,
		Health: controller.Health{
			WireGuard: controller.HealthUnknown,
			Routes:    controller.HealthUnknown,
			DNS:       controller.HealthUnknown,
			EndToEnd:  controller.HealthUnknown,
		},
	}}
	provider := &catalogTestProvider{catalog: ConnectionCatalog{ConfigurationID: "wg-public-sha256:stable"}}
	owner := NewExpectedUIDOwner(501)
	idempotency, err := NewIdempotencyStore(time.Minute, 4)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(backend, NewAuthorizer(NewCommandRegistry(), nil), idempotency, ServerConfig{
		ServiceVersion:        "test",
		ExpectedOwnerIdentity: owner,
		StreamIdentity:        StreamIdentity{StreamID: "stream-local", Epoch: 1},
		CatalogProvider:       provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := server.snapshot(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Catalog != nil || snapshot.Desired != (DesiredState{}) || snapshot.Applied != (AppliedState{}) {
		t.Fatalf("non-negotiated snapshot was enriched: %#v", snapshot)
	}
	if provider.callCount() != 0 {
		t.Fatalf("non-negotiated snapshot fetched remote catalog %d times", provider.callCount())
	}
}

func TestCatalogReadModelRejectsContradictoryControllerState(t *testing.T) {
	snapshot := Snapshot{
		State:    controller.ConnectionStateConnected,
		Health:   Health{WireGuard: controller.HealthUnknown, Routes: controller.HealthUnknown, DNS: controller.HealthUnknown, EndToEnd: controller.HealthUnknown},
		Catalog:  &ConnectionCatalog{ConfigurationID: "wg-public-sha256:stable"},
		StreamID: "stream-invalid",
		Epoch:    1,
	}
	if err := snapshot.validateReadModel(); err == nil {
		t.Fatal("catalog read model accepted connected state with zero applied truth")
	}
}

func TestPristineDisconnectOverIPCDoesNotFetchCatalogAndIsIdempotent(t *testing.T) {
	backend := &catalogTestBackend{snapshot: controller.Snapshot{
		State: controller.ConnectionStateDisconnected,
		Health: controller.Health{
			WireGuard: controller.HealthUnknown,
			Routes:    controller.HealthUnknown,
			DNS:       controller.HealthUnknown,
			EndToEnd:  controller.HealthUnknown,
		},
	}}
	provider := &catalogTestProvider{catalog: ConnectionCatalog{ConfigurationID: "must-not-be-read"}}
	owner := NewExpectedUIDOwner(501)
	authorizer := NewAuthorizer(NewCommandRegistry(), map[CommandClass]AuthorizationPolicy{
		CommandClassOwnerControl: ExactIdentityPolicy(owner),
		CommandClassRead:         ExactIdentityPolicy(owner),
	})
	idempotency, err := NewIdempotencyStore(time.Minute, 4)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(backend, authorizer, idempotency, ServerConfig{
		ServiceVersion:        "test",
		ExpectedOwnerIdentity: owner,
		StreamIdentity:        StreamIdentity{StreamID: "stream-disconnect", Epoch: 1},
		CatalogProvider:       provider,
	})
	if err != nil {
		t.Fatal(err)
	}

	serverConn, clientConn := net.Pipe()
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(context.Background(), serverConn, newUIDPeerAuthentication(501))
	}()
	client, err := NewClient(clientConn, ClientConfig{
		Capabilities:         append(DefaultCapabilities(), CapabilityConnectionCatalog),
		RequiredCapabilities: []Capability{CapabilityConnectionCatalog},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = client.Close()
		if err := <-serveResult; err != nil && !errors.Is(err, io.EOF) {
			t.Errorf("Server.Serve() error = %v", err)
		}
	}()
	if _, err := client.Handshake(context.Background(), ChannelCommand); err != nil {
		t.Fatal(err)
	}

	operationID, err := client.Disconnect(context.Background(), "disconnect-1", "disconnect-key", time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replayedID, err := client.Disconnect(context.Background(), "disconnect-2", "disconnect-key", time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if operationID == "" || replayedID != operationID {
		t.Fatalf("disconnect operation IDs = %q, %q", operationID, replayedID)
	}
	commands := backend.recordedCommands()
	if len(commands) != 1 || commands[0].Kind != controller.CommandDisconnect || commands[0].Desired != (controller.DesiredState{}) {
		t.Fatalf("pristine disconnect commands = %#v", commands)
	}
	if provider.callCount() != 0 {
		t.Fatalf("pristine disconnect fetched catalog %d times", provider.callCount())
	}
}

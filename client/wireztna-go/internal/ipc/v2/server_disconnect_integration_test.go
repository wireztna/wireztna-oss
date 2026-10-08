package v2

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wireztna/client/desktop/controller"
)

type disconnectIntentStore struct {
	mu     sync.Mutex
	intent controller.DurableIntent
}

func (s *disconnectIntentStore) Load(ctx context.Context, _ controller.OwnerID) (controller.DurableIntent, error) {
	if err := ctx.Err(); err != nil {
		return controller.DurableIntent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.intent, nil
}

func (s *disconnectIntentStore) CompareAndSwap(ctx context.Context, expected uint64, next controller.DurableIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.intent.Revision != expected {
		return errors.New("intent CAS mismatch")
	}
	s.intent = next
	return nil
}

func (s *disconnectIntentStore) Replace(ctx context.Context, next controller.DurableIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.intent = next
	s.mu.Unlock()
	return nil
}

type disconnectSessions struct{}

func (disconnectSessions) Acquire(context.Context, controller.OwnerID, controller.DesiredState) (controller.Session, error) {
	panic("session provider must not run for pristine disconnect")
}

type disconnectPlanner struct{}

func (disconnectPlanner) Plan(context.Context, controller.OwnerID, controller.DesiredState, controller.Session) (controller.Plan, error) {
	panic("planner must not run for pristine disconnect")
}

type disconnectTruthGate struct{}

func (disconnectTruthGate) Verify(context.Context, controller.OwnerID, controller.DesiredState, controller.AppliedState, controller.HealthObservation) (controller.Truth, error) {
	panic("truth gate must not run for pristine disconnect")
}

type disconnectWireGuard struct{}

func (disconnectWireGuard) Apply(context.Context, controller.OwnerID, controller.WireGuardConfig) (controller.Undo, error) {
	panic("WireGuard must not mutate for pristine disconnect")
}
func (disconnectWireGuard) Health(context.Context, controller.OwnerID) (controller.WireGuardHealth, error) {
	panic("WireGuard health must not run for pristine disconnect")
}

type disconnectRoutes struct{}

func (disconnectRoutes) Apply(context.Context, controller.OwnerID, controller.RouteSet) (controller.Undo, error) {
	panic("routes must not mutate for pristine disconnect")
}
func (disconnectRoutes) Health(context.Context, controller.OwnerID) (controller.RouteHealth, error) {
	panic("route health must not run for pristine disconnect")
}

type disconnectDNS struct{}

func (disconnectDNS) Apply(context.Context, controller.OwnerID, controller.DNSConfig) (controller.Undo, error) {
	panic("DNS must not mutate for pristine disconnect")
}
func (disconnectDNS) Health(context.Context, controller.OwnerID) (controller.DNSHealth, error) {
	panic("DNS health must not run for pristine disconnect")
}

func TestDurablePristineDisconnectEndToEndOverIPC(t *testing.T) {
	ownerID := controller.OwnerID("uid:501")
	journal, err := controller.NewFileAppliedJournalStore(filepath.Join(t.TempDir(), "applied.json"), 16)
	if err != nil {
		t.Fatal(err)
	}
	controllerContext, cancelController := context.WithCancel(context.Background())
	defer cancelController()
	backend, err := controller.NewController(controllerContext, controller.ControllerConfig{
		Owner: ownerID, Sessions: disconnectSessions{}, Planner: disconnectPlanner{}, TruthGate: disconnectTruthGate{},
		WireGuard: disconnectWireGuard{}, Routes: disconnectRoutes{}, DNS: disconnectDNS{}, Journal: journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := backend.Close(closeContext); err != nil {
			t.Errorf("Controller.Close() error = %v", err)
		}
	}()
	durable, err := controller.NewDurableController(context.Background(), backend, &disconnectIntentStore{}, ownerID, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	owner := NewExpectedUIDOwner(501)
	authorizer := NewAuthorizer(NewCommandRegistry(), map[CommandClass]AuthorizationPolicy{
		CommandClassOwnerControl: ExactIdentityPolicy(owner),
		CommandClassRead:         ExactIdentityPolicy(owner),
	})
	idempotency, err := NewIdempotencyStore(time.Minute, 8)
	if err != nil {
		t.Fatal(err)
	}
	provider := &catalogTestProvider{catalog: ConnectionCatalog{ConfigurationID: "must-not-be-read"}}
	server, err := NewServer(durable, authorizer, idempotency, ServerConfig{
		ServiceVersion:        "test",
		ExpectedOwnerIdentity: owner,
		StreamIdentity:        StreamIdentity{StreamID: "stream-e2e-disconnect", Epoch: 1},
		CatalogProvider:       provider,
	})
	if err != nil {
		t.Fatal(err)
	}

	commandServer, commandPeer := netPipe(t)
	eventServer, eventPeer := netPipe(t)
	commandResult := servePipe(server, commandServer)
	eventResult := servePipe(server, eventServer)
	commandClient, err := NewClient(commandPeer, ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	eventClient, err := NewClient(eventPeer, ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = commandClient.Close()
		_ = eventClient.Close()
		for _, result := range []<-chan error{commandResult, eventResult} {
			if err := <-result; err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Server.Serve() error = %v", err)
			}
		}
	}()
	if _, err := commandClient.Handshake(context.Background(), ChannelCommand); err != nil {
		t.Fatal(err)
	}
	if _, err := eventClient.Handshake(context.Background(), ChannelEvent); err != nil {
		t.Fatal(err)
	}
	_, cursor, err := commandClient.GetSnapshot(context.Background(), "snapshot-pristine", time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := eventClient.Subscribe(context.Background(), "subscribe-pristine", time.Now().Add(250*time.Millisecond), cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	operationID, err := commandClient.Disconnect(context.Background(), "disconnect-pristine-1", "disconnect-pristine-key", time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var observed []controller.Event
	for len(observed) < 3 {
		nextContext, cancel := context.WithTimeout(context.Background(), time.Second)
		event, nextErr := subscription.Next(nextContext)
		cancel()
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		observed = append(observed, event)
	}
	if observed[0].Kind != controller.EventOperationStarted || observed[0].OperationID != controller.OperationID(operationID) ||
		observed[1].Kind != controller.EventSnapshotChanged || observed[1].OperationID != "" ||
		observed[2].Kind != controller.EventOperationSucceeded || observed[2].OperationID != controller.OperationID(operationID) ||
		observed[0].Sequence >= observed[1].Sequence || observed[1].Sequence >= observed[2].Sequence {
		t.Fatalf("pristine disconnect events = %#v", observed)
	}

	replayedID, err := commandClient.Disconnect(context.Background(), "disconnect-pristine-2", "disconnect-pristine-key", time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if replayedID != operationID {
		t.Fatalf("replayed operation ID = %q, want %q", replayedID, operationID)
	}
	silenceContext, cancelSilence := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, silenceErr := subscription.Next(silenceContext)
	cancelSilence()
	if silenceErr == nil {
		t.Fatal("idempotent replay emitted an extra event")
	}
	if !errors.Is(silenceErr, context.DeadlineExceeded) {
		var networkError net.Error
		if !errors.As(silenceErr, &networkError) || !networkError.Timeout() {
			t.Fatalf("idempotent replay returned an unexpected error: %v", silenceErr)
		}
	}
	if provider.callCount() != 0 {
		t.Fatalf("pristine disconnect fetched catalog %d times", provider.callCount())
	}
	snapshot, err := durable.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != controller.ConnectionStateDisconnected || snapshot.Desired != (controller.DesiredState{}) ||
		snapshot.Applied != (controller.AppliedState{}) || snapshot.ActiveOperation != nil {
		t.Fatalf("terminal pristine snapshot = %#v", snapshot)
	}
}

func netPipe(t *testing.T) (io.ReadWriteCloser, io.ReadWriteCloser) {
	t.Helper()
	server, client := net.Pipe()
	return server, client
}

func servePipe(server *Server, connection io.ReadWriteCloser) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- server.Serve(context.Background(), connection, newUIDPeerAuthentication(501))
	}()
	return result
}

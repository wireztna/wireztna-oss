package controller

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type memoryIntentStore struct {
	mu           sync.Mutex
	intent       DurableIntent
	casCalls     int
	replaceCalls int
}

func (s *memoryIntentStore) Load(ctx context.Context, _ OwnerID) (DurableIntent, error) {
	if err := ctx.Err(); err != nil {
		return DurableIntent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.intent, nil
}

func (s *memoryIntentStore) CompareAndSwap(ctx context.Context, expected uint64, next DurableIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.casCalls++
	if s.intent.Revision != expected {
		return errors.New("CAS mismatch")
	}
	s.intent = next
	return nil
}

func (s *memoryIntentStore) Replace(ctx context.Context, next DurableIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.replaceCalls++
	s.intent = next
	s.mu.Unlock()
	return nil
}

type intentBackendStub struct {
	mu            sync.Mutex
	snapshot      Snapshot
	calls         []Command
	execute       func(context.Context, Command) (OperationID, error)
	subscribe     func(context.Context, Sequence) (<-chan Event, error)
	subscriptions int
}

func (b *intentBackendStub) Execute(ctx context.Context, command Command) (OperationID, error) {
	b.mu.Lock()
	b.calls = append(b.calls, command)
	callback := b.execute
	b.mu.Unlock()
	if callback != nil {
		return callback(ctx, command)
	}
	return "operation", nil
}

func (b *intentBackendStub) Snapshot(context.Context) (Snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneSnapshot(b.snapshot), nil
}

func (b *intentBackendStub) Subscribe(ctx context.Context, after Sequence) (<-chan Event, error) {
	b.mu.Lock()
	b.subscriptions++
	callback := b.subscribe
	b.mu.Unlock()
	if callback != nil {
		return callback(ctx, after)
	}
	return make(chan Event), nil
}

func waitForIntent(t *testing.T, durable *DurableController, predicate func(DurableIntent) bool) DurableIntent {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		intent, present, invalid := durable.Intent()
		if present && !invalid && predicate(intent) {
			return intent
		}
		if time.Now().After(deadline) {
			t.Fatalf("durable intent did not reach expected state: %#v present=%v invalid=%v", intent, present, invalid)
		}
		time.Sleep(time.Millisecond)
	}
}

func canonicalTerminal(kind EventKind, operationID OperationID) Event {
	event := Event{Kind: kind, OperationID: operationID, State: ConnectionStateDisconnected}
	if kind == EventOperationFailed {
		event.Error = &Error{Code: ErrorCodeServiceUnavailable, Detail: "injected terminal failure"}
	}
	return event
}

func TestDurableControllerCompensatesRejectedConnectWithInternalContext(t *testing.T) {
	owner := OwnerID("uid:501")
	fixedNow := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store := &memoryIntentStore{intent: DurableIntent{
		SchemaVersion: durableIntentSchemaVersion, Owner: owner, Revision: 1,
		Desired: DesiredState{}, UpdatedAt: fixedNow,
	}}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	backend := &intentBackendStub{snapshot: Snapshot{State: ConnectionStateDisconnected, Health: unknownHealth()}}
	backend.execute = func(ctx context.Context, _ Command) (OperationID, error) {
		cancelRequest()
		return "", ctx.Err()
	}
	durable, err := NewDurableController(context.Background(), backend, store, owner, func() time.Time { return fixedNow })
	if err != nil {
		t.Fatal(err)
	}
	durable.markReady()
	_, err = durable.Execute(requestContext, Command{Kind: CommandConnect, Desired: DesiredState{
		ConfigurationID: "cfg", Generation: 2, GroupID: "group", Connected: true,
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v", err)
	}
	intent, present, invalid := durable.Intent()
	if !present || invalid || intent.Desired.Connected || intent.Revision < 3 {
		t.Fatalf("rejected connect left unsafe intent: %#v present=%v invalid=%v", intent, present, invalid)
	}
}

func TestDurableControllerNeutralizesTerminalFailureFromDisconnected(t *testing.T) {
	for _, kind := range []CommandKind{CommandConnect, CommandSwitch} {
		t.Run(string(kind), func(t *testing.T) {
			owner := OwnerID("uid:501")
			fixedNow := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
			store := &memoryIntentStore{intent: DurableIntent{
				SchemaVersion: durableIntentSchemaVersion, Owner: owner, Revision: 1,
				Desired: DesiredState{}, UpdatedAt: fixedNow,
			}}
			lifetime, cancelLifetime := context.WithCancel(context.Background())
			defer cancelLifetime()
			events := make(chan Event, 1)
			backend := &intentBackendStub{snapshot: Snapshot{
				Sequence: 41, State: ConnectionStateDisconnected, Health: unknownHealth(),
			}}
			backend.subscribe = func(context.Context, Sequence) (<-chan Event, error) { return events, nil }
			backend.execute = func(context.Context, Command) (OperationID, error) {
				backend.mu.Lock()
				subscribed := backend.subscriptions
				backend.mu.Unlock()
				if subscribed != 1 {
					return "", errors.New("execute ran before terminal subscription")
				}
				events <- canonicalTerminal(EventOperationFailed, "explicit-operation")
				return "explicit-operation", nil
			}
			durable, err := NewDurableController(lifetime, backend, store, owner, func() time.Time { return fixedNow })
			if err != nil {
				t.Fatal(err)
			}
			durable.markReady()
			requestContext, cancelRequest := context.WithCancel(context.Background())
			desired := DesiredState{ConfigurationID: "cfg", Generation: 2, GroupID: "group", ExitNodeID: "exit", Connected: true}
			operationID, err := durable.Execute(requestContext, Command{Kind: kind, Desired: desired})
			cancelRequest()
			if err != nil || operationID != "explicit-operation" {
				t.Fatalf("Execute() = %q, %v", operationID, err)
			}
			intent := waitForIntent(t, durable, func(intent DurableIntent) bool { return intent.Revision == 3 })
			wantDesired := desired
			wantDesired.Connected = false
			if intent.Desired != wantDesired {
				t.Fatalf("neutralized desired = %#v, want %#v", intent.Desired, wantDesired)
			}
			store.mu.Lock()
			casCalls, replaceCalls := store.casCalls, store.replaceCalls
			store.mu.Unlock()
			if casCalls != 2 || replaceCalls != 0 {
				t.Fatalf("store calls: CAS=%d Replace=%d, want CAS=2 Replace=0", casCalls, replaceCalls)
			}
		})
	}
}

func TestFailedExplicitConnectNeutralizationPreventsAutomaticRetry(t *testing.T) {
	owner := OwnerID("uid:501")
	store := &memoryIntentStore{intent: DurableIntent{
		SchemaVersion: durableIntentSchemaVersion, Owner: owner, Revision: 1,
		Desired: DesiredState{}, UpdatedAt: time.Now().UTC(),
	}}
	events := make(chan Event, 1)
	backend := &intentBackendStub{snapshot: Snapshot{State: ConnectionStateDisconnected, Health: unknownHealth()}}
	backend.subscribe = func(context.Context, Sequence) (<-chan Event, error) { return events, nil }
	backend.execute = func(context.Context, Command) (OperationID, error) { return "failed-connect", nil }
	durable, err := NewDurableController(context.Background(), backend, store, owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	durable.markReady()
	desired := DesiredState{ConfigurationID: "cfg", Generation: 2, GroupID: "group", Connected: true}
	if _, err := durable.Execute(context.Background(), Command{Kind: CommandConnect, Desired: desired}); err != nil {
		t.Fatal(err)
	}
	events <- canonicalTerminal(EventOperationFailed, "failed-connect")
	waitForIntent(t, durable, func(intent DurableIntent) bool { return intent.Revision == 3 && !intent.Desired.Connected })
	if err := durable.executeAutomatic(context.Background(), CommandReconcile, true); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.calls) != 1 {
		t.Fatalf("backend calls = %#v; stale connected intent retried", backend.calls)
	}
}

func TestLateTerminalFailureDoesNotOverwriteLaterGeneration(t *testing.T) {
	owner := OwnerID("uid:501")
	store := &memoryIntentStore{intent: DurableIntent{
		SchemaVersion: durableIntentSchemaVersion, Owner: owner, Revision: 1,
		Desired: DesiredState{}, UpdatedAt: time.Now().UTC(),
	}}
	events := make(chan Event, 1)
	backend := &intentBackendStub{snapshot: Snapshot{State: ConnectionStateDisconnected, Health: unknownHealth()}}
	backend.subscribe = func(context.Context, Sequence) (<-chan Event, error) { return events, nil }
	executions := 0
	backend.execute = func(context.Context, Command) (OperationID, error) {
		executions++
		return OperationID("operation-" + string(rune('0'+executions))), nil
	}
	durable, err := NewDurableController(context.Background(), backend, store, owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	durable.markReady()
	first := DesiredState{ConfigurationID: "cfg-a", Generation: 2, GroupID: "group-a", Connected: true}
	firstID, err := durable.Execute(context.Background(), Command{Kind: CommandConnect, Desired: first})
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.snapshot = Snapshot{
		State:   ConnectionStateConnected,
		Applied: AppliedState{ConfigurationID: "cfg-a", Generation: 2, GroupID: "group-a", Connected: true},
		Health:  unknownHealth(),
	}
	backend.mu.Unlock()
	later := DesiredState{ConfigurationID: "cfg-b", Generation: 3, GroupID: "group-b", ExitNodeID: "exit-b", Connected: true}
	if _, err := durable.Execute(context.Background(), Command{Kind: CommandSwitch, Desired: later}); err != nil {
		t.Fatal(err)
	}
	events <- canonicalTerminal(EventOperationFailed, firstID)
	time.Sleep(20 * time.Millisecond)
	intent, present, invalid := durable.Intent()
	if !present || invalid || intent.Revision != 3 || intent.Desired != later {
		t.Fatalf("late failure overwrote later intent: %#v present=%v invalid=%v", intent, present, invalid)
	}
}

func TestSuccessfulExplicitConnectDoesNotNeutralizeIntent(t *testing.T) {
	owner := OwnerID("uid:501")
	store := &memoryIntentStore{intent: DurableIntent{
		SchemaVersion: durableIntentSchemaVersion, Owner: owner, Revision: 1,
		Desired: DesiredState{}, UpdatedAt: time.Now().UTC(),
	}}
	events := make(chan Event, 1)
	watchDone := make(chan struct{})
	backend := &intentBackendStub{snapshot: Snapshot{State: ConnectionStateDisconnected, Health: unknownHealth()}}
	backend.subscribe = func(ctx context.Context, _ Sequence) (<-chan Event, error) {
		go func() {
			<-ctx.Done()
			close(watchDone)
		}()
		return events, nil
	}
	backend.execute = func(context.Context, Command) (OperationID, error) { return "successful-connect", nil }
	durable, err := NewDurableController(context.Background(), backend, store, owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	durable.markReady()
	desired := DesiredState{ConfigurationID: "cfg", Generation: 2, GroupID: "group", Connected: true}
	if _, err := durable.Execute(context.Background(), Command{Kind: CommandConnect, Desired: desired}); err != nil {
		t.Fatal(err)
	}
	events <- canonicalTerminal(EventOperationSucceeded, "successful-connect")
	select {
	case <-watchDone:
	case <-time.After(time.Second):
		t.Fatal("success did not stop terminal watcher")
	}
	intent, present, invalid := durable.Intent()
	if !present || invalid || intent.Revision != 2 || intent.Desired != desired {
		t.Fatalf("success neutralized intent: %#v present=%v invalid=%v", intent, present, invalid)
	}
}

func TestSwitchFromConnectedRollsBackIntentOnTerminalFailure(t *testing.T) {
	owner := OwnerID("uid:501")
	current := DesiredState{ConfigurationID: "cfg-a", Generation: 1, GroupID: "group", Connected: true}
	store := &memoryIntentStore{intent: DurableIntent{
		SchemaVersion: durableIntentSchemaVersion, Owner: owner, Revision: 1,
		Desired: current, UpdatedAt: time.Now().UTC(),
	}}
	events := make(chan Event, 1)
	backend := &intentBackendStub{snapshot: Snapshot{
		Sequence: 17, State: ConnectionStateConnected,
		Applied: AppliedState{ConfigurationID: "cfg-a", Generation: 1, GroupID: "group", Connected: true},
		Health:  unknownHealth(),
	}}
	backend.subscribe = func(context.Context, Sequence) (<-chan Event, error) { return events, nil }
	backend.execute = func(context.Context, Command) (OperationID, error) { return "connected-switch", nil }
	durable, err := NewDurableController(context.Background(), backend, store, owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	durable.markReady()
	desired := DesiredState{ConfigurationID: "cfg-b", Generation: 2, GroupID: "group", ExitNodeID: "exit", Connected: true}
	if _, err := durable.Execute(context.Background(), Command{Kind: CommandSwitch, Desired: desired}); err != nil {
		t.Fatal(err)
	}
	events <- canonicalTerminal(EventOperationFailed, "connected-switch")
	intent := waitForIntent(t, durable, func(intent DurableIntent) bool { return intent.Revision == 3 })
	backend.mu.Lock()
	subscriptions := backend.subscriptions
	backend.mu.Unlock()
	if intent.Desired != current || subscriptions != 1 {
		t.Fatalf("failed switch intent=%#v subscriptions=%d, want prior applied intent", intent, subscriptions)
	}
	store.mu.Lock()
	casCalls, replaceCalls := store.casCalls, store.replaceCalls
	store.mu.Unlock()
	if casCalls != 2 || replaceCalls != 0 {
		t.Fatalf("store calls: CAS=%d Replace=%d, want CAS=2 Replace=0", casCalls, replaceCalls)
	}
}

func TestDurableDisconnectPreventsLaterAutomaticReconnect(t *testing.T) {
	owner := OwnerID("uid:501")
	store := &memoryIntentStore{intent: DurableIntent{
		SchemaVersion: durableIntentSchemaVersion, Owner: owner, Revision: 1,
		Desired:   DesiredState{ConfigurationID: "cfg", Generation: 1, GroupID: "group", Connected: true},
		UpdatedAt: time.Now().UTC(),
	}}
	backend := &intentBackendStub{snapshot: Snapshot{
		State:   ConnectionStateDegraded,
		Desired: DesiredState{ConfigurationID: "cfg", Generation: 1, GroupID: "group", Connected: true},
		Applied: AppliedState{ConfigurationID: "cfg", Generation: 1, GroupID: "group", Connected: true},
		Health:  unknownHealth(),
	}}
	durable, err := NewDurableController(context.Background(), backend, store, owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := durable.Execute(context.Background(), Command{Kind: CommandDisconnect}); err != nil {
		t.Fatal(err)
	}
	if err := durable.executeAutomatic(context.Background(), CommandReconcile, false); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.calls) != 1 || backend.calls[0].Kind != CommandDisconnect {
		t.Fatalf("automatic reconnect escaped disconnect barrier: %#v", backend.calls)
	}
}

func TestDisconnectRollbackNeverReplaysConnectedUndo(t *testing.T) {
	called := false
	undo, err := NewUndo("uid:501", func(context.Context, OwnerID) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	controller := &Controller{}
	result := controller.rollbackOperation(false, []appliedUndo{{step: JournalStepWireGuard, undo: undo}}, errors.New("cleanup failed"), false, false)
	if called {
		t.Fatal("disconnect rollback replayed a connected undo")
	}
	if structured, ok := AsError(result); !ok || structured.Code != ErrorCodeDegraded {
		t.Fatalf("rollback result = %v", result)
	}
}

type disconnectedPlanner struct{}

func (disconnectedPlanner) Plan(_ context.Context, _ OwnerID, desired DesiredState, _ Session) (Plan, error) {
	return Plan{
		WireGuard: WireGuardConfig{ConfigurationID: desired.ConfigurationID, Generation: desired.Generation},
		Applied:   AppliedState{ConfigurationID: desired.ConfigurationID, Generation: desired.Generation, GroupID: desired.GroupID, ExitNodeID: desired.ExitNodeID},
	}, nil
}

type unusedSessions struct{}

func (unusedSessions) Acquire(context.Context, OwnerID, DesiredState) (Session, error) {
	return nil, errors.New("unexpected session acquisition")
}

type healthyPort struct{ owner OwnerID }

func (p healthyPort) Apply(_ context.Context, owner OwnerID, _ WireGuardConfig) (Undo, error) {
	return NewUndo(owner, func(context.Context, OwnerID) error { return nil })
}
func (p healthyPort) Health(context.Context, OwnerID) (WireGuardHealth, error) {
	return WireGuardHealth{Status: HealthHealthy}, nil
}

type healthyRoutes struct{}

func (healthyRoutes) Apply(_ context.Context, owner OwnerID, _ RouteSet) (Undo, error) {
	return NewUndo(owner, func(context.Context, OwnerID) error { return nil })
}
func (healthyRoutes) Health(context.Context, OwnerID) (RouteHealth, error) {
	return RouteHealth{Status: HealthHealthy}, nil
}

type healthyDNS struct{}

func (healthyDNS) Apply(_ context.Context, owner OwnerID, _ DNSConfig) (Undo, error) {
	return NewUndo(owner, func(context.Context, OwnerID) error { return nil })
}
func (healthyDNS) Health(context.Context, OwnerID) (DNSHealth, error) {
	return DNSHealth{Status: HealthHealthy}, nil
}

type disconnectedTruth struct{}

func (disconnectedTruth) Verify(context.Context, OwnerID, DesiredState, AppliedState, HealthObservation) (Truth, error) {
	return Truth{State: ConnectionStateDisconnected, Health: Health{
		WireGuard: HealthHealthy, Routes: HealthHealthy, DNS: HealthHealthy, EndToEnd: HealthUnhealthy,
	}}, nil
}

type quietNetworkObserver struct{}

func (quietNetworkObserver) Events(ctx context.Context) (<-chan NetworkEvent, error) {
	result := make(chan NetworkEvent)
	go func() {
		<-ctx.Done()
		close(result)
	}()
	return result, nil
}

func TestLifecycleRepairsMissingIntentBeforeOpeningReadyGate(t *testing.T) {
	owner := OwnerID("uid:501")
	journal, err := NewFileAppliedJournalStore(filepath.Join(t.TempDir(), "applied.json"), 16)
	if err != nil {
		t.Fatal(err)
	}
	controllerContext, cancelController := context.WithCancel(context.Background())
	defer cancelController()
	backend, err := NewController(controllerContext, ControllerConfig{
		Owner: owner, Sessions: unusedSessions{}, Planner: disconnectedPlanner{}, TruthGate: disconnectedTruth{},
		WireGuard: healthyPort{owner: owner}, Routes: healthyRoutes{}, DNS: healthyDNS{}, Journal: journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	intentStore := &memoryIntentStore{}
	durable, err := NewDurableController(context.Background(), backend, intentStore, owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(LifecycleConfig{
		Controller: backend, Durable: durable, Observer: quietNetworkObserver{}, Poll: time.Hour, Retry: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycleContext, cancelLifecycle := context.WithCancel(context.Background())
	if err := lifecycle.Start(lifecycleContext); err != nil {
		t.Fatal(err)
	}
	select {
	case <-durable.ready:
	case <-time.After(time.Second):
		t.Fatal("startup did not open readiness after fail-closed intent repair")
	}
	intent, present, invalid := durable.Intent()
	if !present || invalid || intent.Desired.Connected {
		t.Fatalf("startup intent = %#v present=%v invalid=%v", intent, present, invalid)
	}
	cancelLifecycle()
	closeContext, cancelClose := context.WithTimeout(context.Background(), time.Second)
	defer cancelClose()
	if err := lifecycle.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(closeContext); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteAutomaticPanicDoesNotPoisonIntentLocks(t *testing.T) {
	owner := OwnerID("uid:501")
	store := &memoryIntentStore{intent: DurableIntent{
		SchemaVersion: durableIntentSchemaVersion, Owner: owner, Revision: 1,
		Desired:   DesiredState{ConfigurationID: "cfg", Generation: 1, GroupID: "group", Connected: true},
		UpdatedAt: time.Now().UTC(),
	}}
	calls := 0
	backend := &intentBackendStub{snapshot: Snapshot{State: ConnectionStateDisconnected, Health: unknownHealth()}}
	backend.execute = func(context.Context, Command) (OperationID, error) {
		calls++
		if calls == 1 {
			panic("injected backend panic")
		}
		return "disconnect", nil
	}
	durable, err := NewDurableController(context.Background(), backend, store, owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := durable.executeAutomatic(context.Background(), CommandReconcile, true); err == nil {
		t.Fatal("automatic panic was not converted to an error")
	}

	intentDone := make(chan struct{})
	go func() {
		_, _, _ = durable.Intent()
		close(intentDone)
	}()
	select {
	case <-intentDone:
	case <-time.After(time.Second):
		t.Fatal("intent mutex remained locked after backend panic")
	}

	disconnectDone := make(chan error, 1)
	go func() {
		_, executeErr := durable.Execute(context.Background(), Command{Kind: CommandDisconnect})
		disconnectDone <- executeErr
	}()
	select {
	case err := <-disconnectDone:
		if err != nil {
			t.Fatalf("disconnect after panic: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("execute mutex remained locked after backend panic")
	}
}

func TestLifecycleCloseRetriesPanicAndErrorBeforeSignallingAbsence(t *testing.T) {
	secondAttempt := make(chan struct{})
	allowSecondFailure := make(chan struct{})
	attempts := 0
	lifecycle := &Lifecycle{
		retry:     5 * time.Millisecond,
		closeDone: make(chan struct{}),
		cleanup: func(context.Context) error {
			attempts++
			switch attempts {
			case 1:
				panic("injected cleanup panic")
			case 2:
				close(secondAttempt)
				<-allowSecondFailure
				return errors.New("injected transient cleanup failure")
			default:
				return nil
			}
		},
	}
	go lifecycle.completeClose(false, nil, nil)

	select {
	case <-secondAttempt:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not retry after panic")
	}
	select {
	case <-lifecycle.closeDone:
		t.Fatal("close completed before network absence was established")
	default:
	}
	close(allowSecondFailure)
	select {
	case <-lifecycle.closeDone:
		if attempts < 3 {
			t.Fatalf("cleanup attempts = %d, want at least 3", attempts)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not complete after successful absence check")
	}
}

type rejectingPristinePlanner struct{}

func (rejectingPristinePlanner) Plan(context.Context, OwnerID, DesiredState, Session) (Plan, error) {
	return Plan{}, errors.New("planner must not run for a pristine disconnect")
}

func TestDurablePristineDisconnectIsIdempotentBeforeReadiness(t *testing.T) {
	owner := OwnerID("uid:501")
	journal, err := NewFileAppliedJournalStore(filepath.Join(t.TempDir(), "applied.json"), 16)
	if err != nil {
		t.Fatal(err)
	}
	controllerContext, cancelController := context.WithCancel(context.Background())
	defer cancelController()
	backend, err := NewController(controllerContext, ControllerConfig{
		Owner: owner, Sessions: unusedSessions{}, Planner: rejectingPristinePlanner{}, TruthGate: disconnectedTruth{},
		WireGuard: healthyPort{owner: owner}, Routes: healthyRoutes{}, DNS: healthyDNS{}, Journal: journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := backend.Close(closeContext); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	durable, err := NewDurableController(context.Background(), backend, &memoryIntentStore{}, owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately do not mark the durable controller ready: explicit cleanup
	// must remain available before startup reconciliation and authentication.
	for attempt := 0; attempt < 2; attempt++ {
		snapshot, err := backend.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		events, err := backend.Subscribe(context.Background(), snapshot.Sequence)
		if err != nil {
			t.Fatal(err)
		}
		operationID, err := durable.Execute(context.Background(), Command{Kind: CommandDisconnect})
		if err != nil {
			t.Fatalf("Disconnect attempt %d error = %v", attempt+1, err)
		}
		waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
		var observed []Event
		for len(observed) < 3 {
			select {
			case <-waitContext.Done():
				cancel()
				t.Fatalf("Disconnect attempt %d events timed out: %#v", attempt+1, observed)
			case event := <-events:
				observed = append(observed, event)
			}
		}
		cancel()
		if observed[0].Kind != EventOperationStarted || observed[0].OperationID != operationID ||
			observed[1].Kind != EventSnapshotChanged || observed[1].OperationID != "" ||
			observed[2].Kind != EventOperationSucceeded || observed[2].OperationID != operationID ||
			observed[0].Sequence >= observed[1].Sequence || observed[1].Sequence >= observed[2].Sequence {
			t.Fatalf("Disconnect attempt %d event sequence = %#v", attempt+1, observed)
		}
		select {
		case extra := <-events:
			t.Fatalf("Disconnect attempt %d emitted extra event: %#v", attempt+1, extra)
		default:
		}
	}

	intent, present, invalid := durable.Intent()
	if !present || invalid || intent.Desired != (DesiredState{}) {
		t.Fatalf("pristine disconnect intent = %#v present=%v invalid=%v", intent, present, invalid)
	}
	snapshot, err := backend.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != ConnectionStateDisconnected || snapshot.Desired != (DesiredState{}) ||
		snapshot.Applied != (AppliedState{}) || snapshot.ActiveOperation != nil {
		t.Fatalf("pristine disconnect snapshot = %#v", snapshot)
	}
}

func newPristineGuardController(t *testing.T) *Controller {
	t.Helper()
	owner := OwnerID("uid:501")
	journal, err := NewFileAppliedJournalStore(filepath.Join(t.TempDir(), "applied.json"), 16)
	if err != nil {
		t.Fatal(err)
	}
	controllerContext, cancelController := context.WithCancel(context.Background())
	backend, err := NewController(controllerContext, ControllerConfig{
		Owner: owner, Sessions: unusedSessions{}, Planner: rejectingPristinePlanner{}, TruthGate: disconnectedTruth{},
		WireGuard: healthyPort{owner: owner}, Routes: healthyRoutes{}, DNS: healthyDNS{}, Journal: journal,
	})
	if err != nil {
		cancelController()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeContext, cancelClose := context.WithTimeout(context.Background(), time.Second)
		defer cancelClose()
		if err := backend.Close(closeContext); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		cancelController()
	})
	return backend
}

func TestPristineDisconnectFastPathRejectsContradictoryEvidence(t *testing.T) {
	t.Run("degraded zero state", func(t *testing.T) {
		backend := newPristineGuardController(t)
		backend.mu.Lock()
		backend.snapshot.State = ConnectionStateDegraded
		backend.mu.Unlock()
		_, err := backend.Execute(context.Background(), Command{Kind: CommandDisconnect})
		structured, ok := AsError(err)
		if !ok || structured.Code != ErrorCodeInvalidArgument {
			t.Fatalf("degraded empty disconnect error = %v", err)
		}
	})

	t.Run("recovery journal", func(t *testing.T) {
		backend := newPristineGuardController(t)
		backend.journalMu.Lock()
		backend.journalState = AppliedJournal{
			SchemaVersion: appliedJournalSchemaVersion,
			Owner:         backend.owner,
			Revision:      1,
			Status:        JournalStatusRecoveryRequired,
		}
		backend.journalMu.Unlock()
		_, err := backend.Execute(context.Background(), Command{Kind: CommandDisconnect})
		structured, ok := AsError(err)
		if !ok || structured.Code != ErrorCodeInvalidArgument {
			t.Fatalf("recovery empty disconnect error = %v", err)
		}
	})

	t.Run("residual applied revision", func(t *testing.T) {
		backend := newPristineGuardController(t)
		backend.mu.Lock()
		backend.snapshot.Applied = AppliedState{ConfigurationID: "cfg", Generation: 1}
		backend.mu.Unlock()
		snapshot, err := backend.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		events, err := backend.Subscribe(context.Background(), snapshot.Sequence)
		if err != nil {
			t.Fatal(err)
		}
		operationID, err := backend.Execute(context.Background(), Command{Kind: CommandDisconnect})
		if err != nil {
			t.Fatal(err)
		}
		waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
		err = waitForOperation(waitContext, events, operationID)
		cancel()
		structured, ok := AsError(err)
		if !ok || structured.Code != ErrorCodeServiceUnavailable {
			t.Fatalf("residual applied disconnect terminal error = %v", err)
		}
	})
}

func TestPristineDisconnectSupersedesQueuedOperationBeforeSucceeding(t *testing.T) {
	backend := newPristineGuardController(t)
	queued := &operationRequest{operation: Operation{
		ID: "queued-connect",
		Command: Command{Kind: CommandConnect, Desired: DesiredState{
			ConfigurationID: "cfg", Generation: 1, GroupID: "group", Connected: true,
		}},
		StartedAt: time.Now(),
	}}
	backend.mu.Lock()
	backend.queue = append(backend.queue, queued)
	backend.mu.Unlock()

	operationID, err := backend.Execute(context.Background(), Command{Kind: CommandDisconnect})
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if !queued.terminal || len(backend.queue) != 0 || backend.active != nil {
		t.Fatalf("queued operation was not superseded: terminal=%v queue=%d active=%v", queued.terminal, len(backend.queue), backend.active)
	}
	if operationID == "" || backend.snapshot.State != ConnectionStateDisconnected || backend.snapshot.Applied != (AppliedState{}) {
		t.Fatalf("pristine disconnect result = operation %q snapshot %#v", operationID, backend.snapshot)
	}
}

type trackingCleanupWireGuard struct {
	mu      sync.Mutex
	reverts int
}

func (p *trackingCleanupWireGuard) Apply(_ context.Context, owner OwnerID, _ WireGuardConfig) (Undo, error) {
	return NewUndo(owner, func(context.Context, OwnerID) error {
		p.mu.Lock()
		p.reverts++
		p.mu.Unlock()
		return nil
	})
}

func (*trackingCleanupWireGuard) Health(context.Context, OwnerID) (WireGuardHealth, error) {
	return WireGuardHealth{Status: HealthHealthy}, nil
}

type failOnceCleanupRoutes struct {
	mu       sync.Mutex
	attempts int
	entered  chan int
	release  [2]chan struct{}
}

func newFailOnceCleanupRoutes() *failOnceCleanupRoutes {
	return &failOnceCleanupRoutes{
		entered: make(chan int, 2),
		release: [2]chan struct{}{make(chan struct{}), make(chan struct{})},
	}
}

func (p *failOnceCleanupRoutes) Apply(_ context.Context, owner OwnerID, _ RouteSet) (Undo, error) {
	p.mu.Lock()
	p.attempts++
	attempt := p.attempts
	p.mu.Unlock()
	if attempt > len(p.release) {
		return Undo{}, errors.New("unexpected extra cleanup attempt")
	}
	p.entered <- attempt
	<-p.release[attempt-1]
	if attempt == 1 {
		return Undo{}, errors.New("injected cleanup failure")
	}
	return NewUndo(owner, func(context.Context, OwnerID) error { return nil })
}

func (*failOnceCleanupRoutes) Health(context.Context, OwnerID) (RouteHealth, error) {
	return RouteHealth{Status: HealthHealthy}, nil
}

func TestLifecycleRetriesDisconnectedRecoveryUntilAbsence(t *testing.T) {
	owner := OwnerID("uid:501")
	desired := DesiredState{ConfigurationID: "cfg", Generation: 7, GroupID: "group"}
	journal, err := NewFileAppliedJournalStore(filepath.Join(t.TempDir(), "applied.json"), 16)
	if err != nil {
		t.Fatal(err)
	}
	wireGuard := &trackingCleanupWireGuard{}
	routes := newFailOnceCleanupRoutes()
	controllerContext, cancelController := context.WithCancel(context.Background())
	defer cancelController()
	backend, err := NewController(controllerContext, ControllerConfig{
		Owner: owner, Sessions: unusedSessions{}, Planner: disconnectedPlanner{}, TruthGate: disconnectedTruth{},
		WireGuard: wireGuard, Routes: routes, DNS: healthyDNS{}, Journal: journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryIntentStore{intent: DurableIntent{
		SchemaVersion: durableIntentSchemaVersion,
		Owner:         owner,
		Revision:      1,
		Desired:       desired,
		UpdatedAt:     time.Now().UTC(),
	}}
	durable, err := NewDurableController(context.Background(), backend, store, owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewLifecycle(LifecycleConfig{
		Controller: backend,
		Durable:    durable,
		Observer:   quietNetworkObserver{},
		Poll:       time.Hour,
		Retry:      5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-durable.ready:
	case <-time.After(time.Second):
		t.Fatal("lifecycle did not become ready from a clean disconnected state")
	}

	backend.mu.Lock()
	backend.snapshot = Snapshot{
		State:   ConnectionStateDegraded,
		Desired: desired,
		Applied: AppliedState{
			ConfigurationID: desired.ConfigurationID,
			Generation:      desired.Generation,
			GroupID:         desired.GroupID,
			Connected:       true,
		},
		Health: unknownHealth(),
	}
	backend.mu.Unlock()
	durable.signalChanged()

	for attempt := 1; attempt <= 2; attempt++ {
		select {
		case got := <-routes.entered:
			if got != attempt {
				t.Fatalf("cleanup attempt = %d, want %d", got, attempt)
			}
		case <-time.After(time.Second):
			t.Fatalf("cleanup attempt %d was not issued", attempt)
		}
		snapshot, snapshotErr := backend.Snapshot(context.Background())
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		if snapshot.ActiveOperation == nil || snapshot.ActiveOperation.Command.Kind != CommandDisconnect || snapshot.Desired.Connected {
			t.Fatalf("cleanup attempt %d snapshot = %#v", attempt, snapshot)
		}
		close(routes.release[attempt-1])
	}

	deadline := time.Now().Add(time.Second)
	for {
		snapshot, snapshotErr := backend.Snapshot(context.Background())
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		if snapshot.ActiveOperation == nil && snapshot.State == ConnectionStateDisconnected && !snapshot.Applied.Connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cleanup did not establish absence: %#v", snapshot)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case attempt := <-routes.entered:
		t.Fatalf("lifecycle issued cleanup attempt %d after absence", attempt)
	case <-time.After(25 * time.Millisecond):
	}
	wireGuard.mu.Lock()
	reverts := wireGuard.reverts
	wireGuard.mu.Unlock()
	if reverts != 0 {
		t.Fatalf("disconnect cleanup replayed %d connected undo tokens", reverts)
	}

	closeContext, cancelClose := context.WithTimeout(context.Background(), time.Second)
	defer cancelClose()
	if err := lifecycle.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	routes.mu.Lock()
	defer routes.mu.Unlock()
	if routes.attempts != 2 {
		t.Fatalf("cleanup attempts = %d, want exactly 2", routes.attempts)
	}
}

func TestAutomaticDisconnectDoesNotSupersedeQueuedExplicitDisconnect(t *testing.T) {
	owner := OwnerID("uid:501")
	desired := DesiredState{ConfigurationID: "cfg", Generation: 9, GroupID: "group"}
	backend := newPristineGuardController(t)
	queued := &operationRequest{operation: Operation{
		ID:        "explicit-disconnect",
		Command:   Command{Kind: CommandDisconnect, Desired: desired},
		StartedAt: time.Now(),
	}}
	backend.mu.Lock()
	backend.queue = append(backend.queue, queued)
	backend.mu.Unlock()

	store := &memoryIntentStore{intent: DurableIntent{
		SchemaVersion: durableIntentSchemaVersion,
		Owner:         owner,
		Revision:      1,
		Desired:       desired,
		UpdatedAt:     time.Now().UTC(),
	}}
	durable, err := NewDurableController(context.Background(), backend, store, owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := durable.executeAutomatic(context.Background(), CommandDisconnect, false); err != nil {
		t.Fatal(err)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.queue) != 1 || backend.queue[0] != queued || queued.terminal {
		t.Fatalf("automatic cleanup superseded queued explicit disconnect: queue=%#v terminal=%v", backend.queue, queued.terminal)
	}
}
func TestClosedExplicitTerminalStreamDoesNotNeutralizeIntent(t *testing.T) {
	owner := OwnerID("uid:501")
	store := &memoryIntentStore{intent: DurableIntent{
		SchemaVersion: durableIntentSchemaVersion, Owner: owner, Revision: 1,
		Desired: DesiredState{}, UpdatedAt: time.Now().UTC(),
	}}
	events := make(chan Event)
	watchDone := make(chan struct{})
	backend := &intentBackendStub{snapshot: Snapshot{State: ConnectionStateDisconnected, Health: unknownHealth()}}
	backend.subscribe = func(ctx context.Context, _ Sequence) (<-chan Event, error) {
		go func() {
			<-ctx.Done()
			close(watchDone)
		}()
		return events, nil
	}
	backend.execute = func(context.Context, Command) (OperationID, error) { return "stream-closed-connect", nil }
	durable, err := NewDurableController(context.Background(), backend, store, owner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	durable.markReady()
	desired := DesiredState{ConfigurationID: "cfg", Generation: 2, GroupID: "group", Connected: true}
	if _, err := durable.Execute(context.Background(), Command{Kind: CommandConnect, Desired: desired}); err != nil {
		t.Fatal(err)
	}
	close(events)
	select {
	case <-watchDone:
	case <-time.After(time.Second):
		t.Fatal("closed stream did not stop terminal watcher")
	}
	intent, present, invalid := durable.Intent()
	if !present || invalid || intent.Revision != 2 || intent.Desired != desired {
		t.Fatalf("closed stream neutralized intent: %#v present=%v invalid=%v", intent, present, invalid)
	}
}

package controller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUndoPassesCapturedOwnerAtMostOnceAcrossCopies(t *testing.T) {
	const owner OwnerID = "local-owner"
	var calls atomic.Int32
	receivedOwner := make(chan OwnerID, 1)
	undo, err := NewUndo(owner, func(_ context.Context, callbackOwner OwnerID) error {
		calls.Add(1)
		receivedOwner <- callbackOwner
		return nil
	})
	if err != nil {
		t.Fatalf("NewUndo() error = %v", err)
	}
	if got := undo.Owner(); got != owner {
		t.Fatalf("Undo.Owner() = %q, want %q", got, owner)
	}

	const workers = 32
	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		copyOfUndo := undo
		go func() {
			defer group.Done()
			if revertErr := copyOfUndo.Revert(context.Background()); revertErr != nil {
				t.Errorf("Undo.Revert() error = %v", revertErr)
			}
		}()
	}
	group.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("rollback call count = %d, want 1", got)
	}
	if got := <-receivedOwner; got != owner {
		t.Fatalf("rollback callback owner = %q, want exactly %q", got, owner)
	}
}

func TestUndoCachesFailureWithoutRepeatingSideEffect(t *testing.T) {
	ambiguous := errors.New("cleanup completed but verification failed")
	calls := 0
	undo, err := NewUndo("owner", func(context.Context, OwnerID) error {
		calls++
		return ambiguous
	})
	if err != nil {
		t.Fatalf("NewUndo() error = %v", err)
	}

	for attempt := 0; attempt < 3; attempt++ {
		if got := undo.Revert(context.Background()); !errors.Is(got, ambiguous) {
			t.Fatalf("Revert() attempt %d error = %v, want %v", attempt, got, ambiguous)
		}
	}
	if calls != 1 {
		t.Fatalf("rollback call count = %d, want 1", calls)
	}
}

func TestUndoWaitHonorsContextCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	undo, err := NewUndo("owner", func(context.Context, OwnerID) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("NewUndo() error = %v", err)
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- undo.Revert(context.Background()) }()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if got := undo.Revert(ctx); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("waiting Revert() error = %v, want deadline exceeded", got)
	}
	close(release)
	if got := <-firstDone; got != nil {
		t.Fatalf("initial Revert() error = %v", got)
	}
}

func TestNewUndoRejectsUnscopedOrMissingAction(t *testing.T) {
	tests := []struct {
		name   string
		owner  OwnerID
		revert func(context.Context, OwnerID) error
	}{
		{name: "missing owner", revert: func(context.Context, OwnerID) error { return nil }},
		{name: "missing action", owner: "owner"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewUndo(test.owner, test.revert); err == nil {
				t.Fatal("NewUndo() error = nil")
			}
		})
	}

	var zero Undo
	if err := zero.Revert(context.Background()); err == nil {
		t.Fatal("zero Undo.Revert() error = nil")
	}
}

// Compile-time assertions freeze context-aware, platform-neutral port shapes.
var (
	_ WireGuardDevice     = wireGuardPortStub{}
	_ RouteManager        = routePortStub{}
	_ DNSManager          = dnsPortStub{}
	_ NetworkObserver     = networkPortStub{}
	_ SecretStore         = secretStorePortStub{}
	_ UpdateInstaller     = updateInstallerPortStub{}
	_ PlatformDiagnostics = diagnosticsPortStub{}
)

type wireGuardPortStub struct{}

func (wireGuardPortStub) Apply(context.Context, OwnerID, WireGuardConfig) (Undo, error) {
	return Undo{}, nil
}
func (wireGuardPortStub) Health(context.Context, OwnerID) (WireGuardHealth, error) {
	return WireGuardHealth{}, nil
}

type routePortStub struct{}

func (routePortStub) Apply(context.Context, OwnerID, RouteSet) (Undo, error) {
	return Undo{}, nil
}
func (routePortStub) Health(context.Context, OwnerID) (RouteHealth, error) {
	return RouteHealth{}, nil
}

type dnsPortStub struct{}

func (dnsPortStub) Apply(context.Context, OwnerID, DNSConfig) (Undo, error) {
	return Undo{}, nil
}
func (dnsPortStub) Health(context.Context, OwnerID) (DNSHealth, error) {
	return DNSHealth{}, nil
}

type networkPortStub struct{}

func (networkPortStub) Events(context.Context) (<-chan NetworkEvent, error) {
	return nil, nil
}

type secretStorePortStub struct{}

func (secretStorePortStub) Put(context.Context, OwnerID, SecretRef, []byte) (Undo, error) {
	return Undo{}, nil
}
func (secretStorePortStub) Get(context.Context, OwnerID, SecretRef) ([]byte, error) {
	return nil, nil
}
func (secretStorePortStub) Delete(context.Context, OwnerID, SecretRef) (Undo, error) {
	return Undo{}, nil
}

type updateInstallerPortStub struct{}

func (updateInstallerPortStub) Install(context.Context, OwnerID, UpdatePackage) (Undo, error) {
	return Undo{}, nil
}

type diagnosticsPortStub struct{}

func (diagnosticsPortStub) Collect(context.Context, OwnerID, DiagnosticsRequest) (DiagnosticsReport, error) {
	return DiagnosticsReport{}, nil
}

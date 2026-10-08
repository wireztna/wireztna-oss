package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/ipc"
)

type fakeServiceDNS struct{ err error }

func (f fakeServiceDNS) Configure(string, []string) error { return f.err }
func (f fakeServiceDNS) Cleanup() error                   { return f.err }

func TestServiceDisconnectCancelsBeforeWaitingForOperationGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	oldDNS := newServiceDNSManager
	newServiceDNSManager = func() serviceDNSManager { return fakeServiceDNS{} }
	defer func() { newServiceDNSManager = oldDNS }()

	s := newServiceState()
	ctx, finish := s.beginOperation()
	defer finish()
	s.operationMu.Lock()
	result := make(chan ipc.Response, 1)
	go func() { result <- s.handleDisconnect() }()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("disconnect waited for operationMu before cancelling in-flight work")
	}
	s.operationMu.Unlock()
	select {
	case resp := <-result:
		if !resp.Success || resp.Data.Status != ipc.StatusDisconnected {
			t.Fatalf("disconnect response = %+v", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect deadlocked after operation gate was released")
	}
}

func TestLifecycleGenerationRejectsStaleCommitAfterDisconnect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	generation, err := config.BeginLifecycle(false)
	if err != nil {
		t.Fatalf("begin connect lifecycle: %v", err)
	}
	if err := config.SaveStateForLifecycle(generation, &config.RuntimeState{SessionID: "active"}); err != nil {
		t.Fatalf("save active state: %v", err)
	}
	if _, err := config.BeginLifecycle(true); err != nil {
		t.Fatalf("begin disconnect lifecycle: %v", err)
	}
	err = config.SaveStateForLifecycle(generation, &config.RuntimeState{SessionID: "stale"})
	if !errors.Is(err, config.ErrStaleLifecycle) {
		t.Fatalf("stale SaveStateForLifecycle() error = %v", err)
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if !state.UserDisconnected || state.SessionID != "active" {
		t.Fatalf("stale commit mutated state: %+v", state)
	}
}

func TestServiceDisconnectSurfacesDNSCleanupFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	want := errors.New("cleanup failed")
	oldDNS := newServiceDNSManager
	newServiceDNSManager = func() serviceDNSManager { return fakeServiceDNS{err: want} }
	defer func() { newServiceDNSManager = oldDNS }()

	resp := newServiceState().handleDisconnect()
	if resp.Success || !strings.Contains(resp.Error, want.Error()) {
		t.Fatalf("disconnect response = %+v, want cleanup failure", resp)
	}
}

func TestServiceConnectIntentFailureLeavesTerminalError(t *testing.T) {
	want := errors.New("state unavailable")
	oldBegin := beginServiceLifecycle
	beginServiceLifecycle = func(bool) (uint64, error) { return 0, want }
	defer func() { beginServiceLifecycle = oldBegin }()

	s := newServiceState()
	resp := s.handleConnect("group")
	if resp.Success || resp.Data == nil || resp.Data.Status != ipc.StatusError || !strings.Contains(resp.Error, want.Error()) {
		t.Fatalf("connect response = %+v, want terminal error", resp)
	}
}

func TestServiceSwitchCleanupFailureLeavesTerminalError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	want := errors.New("DNS cleanup failed")
	oldDNS := newServiceDNSManager
	newServiceDNSManager = func() serviceDNSManager { return fakeServiceDNS{err: want} }
	defer func() { newServiceDNSManager = oldDNS }()

	s := newServiceState()
	s.status = ipc.StatusConnected
	resp := s.handleSwitch("next-group")
	if resp.Success || resp.Data == nil || resp.Data.Status != ipc.StatusError || !strings.Contains(resp.Error, want.Error()) {
		t.Fatalf("switch response = %+v, want terminal cleanup error", resp)
	}
}

func TestServiceAlreadyConnectedRejectsStaleDurableIntent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	generation, err := config.BeginLifecycle(false)
	if err != nil {
		t.Fatalf("begin connected lifecycle: %v", err)
	}
	if err := config.SaveStateForLifecycle(generation, &config.RuntimeState{SessionID: "active"}); err != nil {
		t.Fatalf("save connected lifecycle: %v", err)
	}
	if _, err := config.BeginLifecycle(true); err != nil {
		t.Fatalf("persist disconnect intent: %v", err)
	}

	s := newServiceState()
	s.status = ipc.StatusConnected
	s.sessionID = "active"
	s.lifecycleGeneration = generation
	resp := s.handleConnect("")
	if resp.Success || !strings.Contains(resp.Error, "stale durable lifecycle intent") {
		t.Fatalf("connect response = %+v, want stale durable intent failure", resp)
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if !state.UserDisconnected {
		t.Fatal("already-connected fast path cleared disconnect intent")
	}
}

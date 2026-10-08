package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/ipc"
)

type fakeConnectIPC struct {
	statuses    []*ipc.State
	statusErrs  []error
	statusCalls int
	connects    int
}

func (f *fakeConnectIPC) SendStatus() (*ipc.State, error) {
	i := f.statusCalls
	f.statusCalls++
	var state *ipc.State
	var err error
	if i < len(f.statuses) {
		state = f.statuses[i]
	}
	if i < len(f.statusErrs) {
		err = f.statusErrs[i]
	}
	return state, err
}
func (f *fakeConnectIPC) SendConnect(string) error  { f.connects++; return nil }
func (f *fakeConnectIPC) SendExitNode(string) error { f.connects++; return nil }
func (f *fakeConnectIPC) SendDisconnect() error     { return nil }

func connectTestCommand(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().String("exit-node", "", "")
	cmd.Flags().Bool("no-daemon", true, "")
	return cmd
}

func TestRunConnectViaIPCRequiresConnectedPostcondition(t *testing.T) {
	fake := &fakeConnectIPC{statuses: []*ipc.State{
		{Status: ipc.StatusDisconnected},
		{Status: ipc.StatusConnecting},
	}}
	oldFactory, oldDelay := newConnectIPCClient, connectIPCStatusDelay
	newConnectIPCClient = func() connectIPCClient { return fake }
	connectIPCStatusDelay = 0
	defer func() { newConnectIPCClient, connectIPCStatusDelay = oldFactory, oldDelay }()

	err := runConnectViaIPC(connectTestCommand(t))
	if err == nil || !strings.Contains(err.Error(), ipc.StatusConnecting) {
		t.Fatalf("runConnectViaIPC() error = %v, want non-connected postcondition failure", err)
	}
	if fake.connects != 1 {
		t.Fatalf("connect calls = %d, want 1", fake.connects)
	}
}

func TestRunConnectViaIPCPropagatesStatusFailure(t *testing.T) {
	want := errors.New("IPC unavailable")
	fake := &fakeConnectIPC{statusErrs: []error{want}}
	oldFactory := newConnectIPCClient
	newConnectIPCClient = func() connectIPCClient { return fake }
	defer func() { newConnectIPCClient = oldFactory }()

	err := runConnectViaIPC(connectTestCommand(t))
	if !errors.Is(err, want) {
		t.Fatalf("runConnectViaIPC() error = %v, want %v", err, want)
	}
	if fake.connects != 0 {
		t.Fatalf("connect calls = %d, want 0", fake.connects)
	}
}

func TestWaitForTunnelContextCancelsBeforeHealthMutation(t *testing.T) {
	oldPing := pingTunnelHost
	called := false
	pingTunnelHost = func(string) bool { called = true; return true }
	defer func() { pingTunnelHost = oldPing }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := waitForTunnelContext(ctx, "10.200.0.1", time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForTunnelContext() error = %v, want context canceled", err)
	}
	if called {
		t.Fatal("health probe ran after cancellation")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation was not prompt")
	}
}

func TestCommitDirectConnectionRejectsCancellationAfterSave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	generation, err := config.BeginLifecycle(false)
	if err != nil {
		t.Fatalf("begin lifecycle: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	oldSave := saveDirectState
	saveDirectState = func(gotGeneration uint64, state *config.RuntimeState) error {
		if gotGeneration != generation {
			t.Fatalf("save generation = %d, want %d", gotGeneration, generation)
		}
		if err := config.SaveStateForLifecycle(gotGeneration, state); err != nil {
			return err
		}
		cancel()
		return nil
	}
	defer func() { saveDirectState = oldSave }()

	err = commitDirectConnection(ctx, generation, &config.RuntimeState{SessionID: "cancelled"})
	if !errors.Is(err, config.ErrStaleLifecycle) {
		t.Fatalf("commitDirectConnection() error = %v, want stale lifecycle", err)
	}
}

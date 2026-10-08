package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/tunnel"
)

type fakeClient struct {
	mu          sync.Mutex
	renew       func(context.Context) (*api.SessionResponse, error)
	renewCount  int
	inFlight    int
	maxInFlight int
}

func (f *fakeClient) RenewSessionContext(ctx context.Context, _ string, _ ...string) (*api.SessionResponse, error) {
	f.mu.Lock()
	f.renewCount++
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inFlight--; f.mu.Unlock() }()
	return f.renew(ctx)
}
func (f *fakeClient) GetDNSZonesContext(context.Context) ([]string, error) { return nil, nil }

type fakeTunnel struct {
	mu        sync.Mutex
	events    *[]string
	updates   int
	downCalls int
	upCalls   int
}

func (f *fakeTunnel) Down() { f.mu.Lock(); f.downCalls++; f.mu.Unlock() }
func (f *fakeTunnel) Up() error {
	f.mu.Lock()
	f.upCalls++
	f.mu.Unlock()
	return nil
}
func (f *fakeTunnel) UpdatePSK(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	if f.events != nil {
		*f.events = append(*f.events, "apply")
	}
	return nil
}

func expiredState(t *testing.T) uint64 {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	generation, err := config.BeginLifecycle(false)
	if err != nil {
		t.Fatalf("begin lifecycle: %v", err)
	}
	err = config.SaveStateForLifecycle(generation, &config.RuntimeState{
		SessionID: "old", ExpiresAt: time.Now().Add(-time.Minute), PresharedKey: "old",
	})
	if err != nil {
		t.Fatalf("save expired state: %v", err)
	}
	return generation
}

func renewedSession() *api.SessionResponse {
	return &api.SessionResponse{SessionID: "new", PresharedKey: "new-psk", ExpiresAt: api.FlexTime{Time: time.Now().Add(time.Hour)}}
}

func TestRenewalAppliesBeforePersisting(t *testing.T) {
	generation := expiredState(t)
	events := []string{}
	client := &fakeClient{renew: func(context.Context) (*api.SessionResponse, error) { return renewedSession(), nil }}
	tun := &fakeTunnel{events: &events}
	oldSave := saveState
	saveState = func(uint64, *config.RuntimeState) error { events = append(events, "save"); return nil }
	defer func() { saveState = oldSave }()

	d := New(Config{Client: client, Tunnel: tun, RenewBefore: time.Minute, LifecycleGeneration: generation})
	if err := d.checkRenewal(context.Background()); err != nil {
		t.Fatalf("checkRenewal: %v", err)
	}
	if len(events) != 2 || events[0] != "apply" || events[1] != "save" {
		t.Fatalf("events = %v, want [apply save]", events)
	}
}

func TestRenewalDoesNotDoubleRenewAfterSaveFailure(t *testing.T) {
	generation := expiredState(t)
	client := &fakeClient{renew: func(context.Context) (*api.SessionResponse, error) { return renewedSession(), nil }}
	tun := &fakeTunnel{}
	oldSave := saveState
	saveState = func(uint64, *config.RuntimeState) error { return errors.New("disk full") }
	defer func() { saveState = oldSave }()

	d := New(Config{Client: client, Tunnel: tun, RenewBefore: time.Minute, LifecycleGeneration: generation})
	if err := d.checkRenewal(context.Background()); err == nil {
		t.Fatal("save failure reported success")
	}
	if err := d.checkRenewal(context.Background()); err != nil {
		t.Fatalf("second check: %v", err)
	}
	if client.renewCount != 1 || tun.updates != 1 {
		t.Fatalf("renewals=%d updates=%d, want one applied remote mutation", client.renewCount, tun.updates)
	}
}

func TestSharedOperationGateAllowsOnlyOneRenewal(t *testing.T) {
	generation := expiredState(t)
	client := &fakeClient{renew: func(context.Context) (*api.SessionResponse, error) {
		time.Sleep(30 * time.Millisecond)
		return renewedSession(), nil
	}}
	gate := &sync.Mutex{}
	d1 := New(Config{Client: client, Tunnel: &fakeTunnel{}, RenewBefore: time.Minute, LifecycleGeneration: generation, OperationMu: gate})
	d2 := New(Config{Client: client, Tunnel: &fakeTunnel{}, RenewBefore: time.Minute, LifecycleGeneration: generation, OperationMu: gate})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = d1.checkRenewal(context.Background()) }()
	go func() { defer wg.Done(); _ = d2.checkRenewal(context.Background()) }()
	wg.Wait()
	if client.maxInFlight != 1 || client.renewCount != 1 {
		t.Fatalf("max in-flight=%d renewals=%d, want 1/1", client.maxInFlight, client.renewCount)
	}
}

func TestStaleRenewalResultCannotApplyOrPersist(t *testing.T) {
	generation := expiredState(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	client := &fakeClient{renew: func(context.Context) (*api.SessionResponse, error) {
		close(entered)
		<-release
		return renewedSession(), nil
	}}
	tun := &fakeTunnel{}
	d := New(Config{Client: client, Tunnel: tun, RenewBefore: time.Minute, LifecycleGeneration: generation})
	result := make(chan error, 1)
	go func() { result <- d.checkRenewal(context.Background()) }()
	<-entered
	if _, err := config.BeginLifecycle(true); err != nil {
		t.Fatalf("disconnect intent: %v", err)
	}
	close(release)
	err := <-result
	if !errors.Is(err, config.ErrStaleLifecycle) {
		t.Fatalf("checkRenewal() error = %v, want stale lifecycle", err)
	}
	if tun.updates != 0 {
		t.Fatalf("stale result applied %d PSK updates", tun.updates)
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.SessionID != "old" || !state.UserDisconnected {
		t.Fatalf("stale result mutated state: %+v", state)
	}
}

type fakeDNSManager struct{}

func (fakeDNSManager) Configure(string, []string) error { return nil }
func (fakeDNSManager) Cleanup() error                   { return nil }

func TestReconnectRollsBackWhenLifecycleStalesAfterSave(t *testing.T) {
	generation := expiredState(t)
	oldTunnel := &fakeTunnel{}
	replacement := &fakeTunnel{}
	client := &fakeClient{renew: func(context.Context) (*api.SessionResponse, error) { return renewedSession(), nil }}

	oldNewTunnel, oldDNS, oldStatus, oldSave := newTunnel, newDNSManager, getTunnelStatus, saveState
	newTunnel = func(tunnel.Config) (lifecycleTunnel, error) { return replacement, nil }
	newDNSManager = func() dnsManager { return fakeDNSManager{} }
	getTunnelStatus = func(string) (*tunnel.StatusInfo, error) {
		return &tunnel.StatusInfo{LastHandshake: time.Now()}, nil
	}
	saveState = func(generation uint64, state *config.RuntimeState) error {
		if err := oldSave(generation, state); err != nil {
			return err
		}
		_, err := config.BeginLifecycle(true)
		return err
	}
	defer func() {
		newTunnel, newDNSManager, getTunnelStatus, saveState = oldNewTunnel, oldDNS, oldStatus, oldSave
	}()

	published := 0
	d := New(Config{
		Client: client, Tunnel: oldTunnel, TunnelConfig: &tunnel.Config{InterfaceName: "wg-test"},
		LifecycleGeneration: generation,
		OnTunnelReplaced:    func(*tunnel.Tunnel) { published++ },
	})
	err := d.reconnect(context.Background())
	if !errors.Is(err, config.ErrStaleLifecycle) {
		t.Fatalf("reconnect error = %v, want stale lifecycle", err)
	}
	if replacement.downCalls != 1 {
		t.Fatalf("replacement Down() calls = %d, want 1", replacement.downCalls)
	}
	if published != 0 {
		t.Fatalf("published replacements = %d, want 0", published)
	}
	if d.config.Tunnel != oldTunnel {
		t.Fatal("stale replacement became daemon-owned tunnel")
	}
}

package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
)

type fakeTunnelPSKUpdater struct {
	psk       string
	err       error
	downCalls int
}

func (f *fakeTunnelPSKUpdater) UpdatePSK(psk string) error {
	f.psk = psk
	return f.err
}

func (f *fakeTunnelPSKUpdater) Down() { f.downCalls++ }

func TestConnectResultRetainsActiveTunnel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	updater := &fakeTunnelPSKUpdater{}
	m := Model{state: StateConnecting, cfg: &config.ClientConfig{}}

	updatedModel, _ := m.Update(connectResultMsg{
		session: &api.SessionResponse{SessionID: "session-123", ExpiresAt: api.FlexTime{Time: time.Now().Add(time.Hour)}},
		tunnel:  updater,
	})
	updated := updatedModel.(Model)

	if updated.tunnel != updater {
		t.Fatal("connected model did not retain active tunnel updater")
	}
}

func TestRenewResultPersistsOnlyAfterPSKUpdate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	if err := config.SaveState(&config.RuntimeState{SessionID: "old-session", PresharedKey: "old-psk"}); err != nil {
		t.Fatalf("save initial state: %v", err)
	}

	updater := &fakeTunnelPSKUpdater{}
	m := Model{
		state:              StateConnected,
		sessionID:          "old-session",
		tunnel:             updater,
		cfg:                &config.ClientConfig{},
		sessionCoordinator: &sessionCoordinator{},
	}
	newExpiry := time.Now().Add(8 * time.Hour)
	updatedModel, cmd := m.Update(renewResultMsg{
		session: &api.SessionResponse{
			SessionID:    "new-session",
			PresharedKey: "new-psk",
			ExpiresAt:    api.FlexTime{Time: newExpiry},
			TTLSeconds:   28800,
		},
	})
	updated := updatedModel.(Model)

	if cmd != nil {
		t.Fatal("successful PSK update unexpectedly scheduled reconnect")
	}
	if updater.psk != "new-psk" {
		t.Fatalf("UpdatePSK() received %q, want new-psk", updater.psk)
	}
	if updated.sessionID != "new-session" {
		t.Fatalf("model session = %q, want new-session", updated.sessionID)
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatalf("load renewed state: %v", err)
	}
	if state.SessionID != "new-session" || state.PresharedKey != "new-psk" {
		t.Fatalf("persisted state = session %q PSK %q, want renewed values", state.SessionID, state.PresharedKey)
	}
}

func TestRenewResultReconnectsWithoutPersistingUnappliedPSK(t *testing.T) {
	for _, test := range []struct {
		name    string
		updater tunnelPSKUpdater
	}{
		{name: "update failure", updater: &fakeTunnelPSKUpdater{err: errors.New("wgctrl failed")}},
		{name: "missing active tunnel"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("SUDO_USER", "")
			if err := config.SaveState(&config.RuntimeState{SessionID: "old-session", PresharedKey: "old-psk"}); err != nil {
				t.Fatalf("save initial state: %v", err)
			}

			m := Model{
				state:              StateConnected,
				sessionID:          "old-session",
				tunnel:             test.updater,
				cfg:                &config.ClientConfig{},
				sessionCoordinator: &sessionCoordinator{},
			}
			updatedModel, cmd := m.Update(renewResultMsg{
				session: &api.SessionResponse{
					SessionID:    "unapplied-session",
					PresharedKey: "unapplied-psk",
					ExpiresAt:    api.FlexTime{Time: time.Now().Add(8 * time.Hour)},
				},
			})
			updated := updatedModel.(Model)

			if updated.state != StateReconnecting {
				t.Fatalf("state = %s, want %s", updated.state, StateReconnecting)
			}
			if cmd == nil {
				t.Fatal("failed PSK update did not schedule reconnect")
			}
			if updated.sessionID != "old-session" {
				t.Fatalf("model adopted unapplied session %q", updated.sessionID)
			}
			state, err := config.LoadState()
			if err != nil {
				t.Fatalf("load state after failed update: %v", err)
			}
			if state.SessionID != "old-session" || state.PresharedKey != "old-psk" {
				t.Fatalf("persisted unapplied state: session %q PSK %q", state.SessionID, state.PresharedKey)
			}
		})
	}
}

func TestStaleRenewResultDoesNotMutateTunnelOrState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	if err := config.SaveState(&config.RuntimeState{SessionID: "old", PresharedKey: "old"}); err != nil {
		t.Fatalf("save initial state: %v", err)
	}
	updater := &fakeTunnelPSKUpdater{}
	m := Model{
		state: StateConnected, sessionID: "old", tunnel: updater,
		cfg: &config.ClientConfig{}, sessionCoordinator: &sessionCoordinator{},
		connectionGeneration: 2,
	}
	updatedModel, _ := m.Update(renewResultMsg{
		generation: 1,
		session:    &api.SessionResponse{SessionID: "stale", PresharedKey: "stale-psk"},
	})
	updated := updatedModel.(Model)
	if updater.psk != "" || updated.sessionID != "old" {
		t.Fatalf("stale renewal mutated updater=%q session=%q", updater.psk, updated.sessionID)
	}
}

func TestDurablyStaleRenewResultDoesNotApplyPSK(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	generation, err := config.BeginLifecycle(false)
	if err != nil {
		t.Fatalf("begin lifecycle: %v", err)
	}
	if err := config.SaveStateForLifecycle(generation, &config.RuntimeState{SessionID: "old", PresharedKey: "old"}); err != nil {
		t.Fatalf("save initial state: %v", err)
	}
	if _, err := config.BeginLifecycle(true); err != nil {
		t.Fatalf("invalidate lifecycle: %v", err)
	}

	updater := &fakeTunnelPSKUpdater{}
	m := Model{
		state: StateConnected, sessionID: "old", tunnel: updater,
		cfg: &config.ClientConfig{}, sessionCoordinator: &sessionCoordinator{},
		lifecycleGeneration: generation,
	}
	updatedModel, _ := m.Update(renewResultMsg{session: &api.SessionResponse{
		SessionID: "new", PresharedKey: "new-psk", ExpiresAt: api.FlexTime{Time: time.Now().Add(time.Hour)},
	}})
	updated := updatedModel.(Model)
	if updater.psk != "" {
		t.Fatalf("stale PSK was applied: %q", updater.psk)
	}
	if updated.sessionID != "old" || updated.lastRenewError == "" {
		t.Fatalf("stale result was not rejected: session=%q error=%q", updated.sessionID, updated.lastRenewError)
	}
}

func TestRenewSaveFailureIsVisibleAndDoesNotDoubleRenew(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	generation, err := config.BeginLifecycle(false)
	if err != nil {
		t.Fatalf("begin lifecycle: %v", err)
	}
	if err := config.SaveStateForLifecycle(generation, &config.RuntimeState{SessionID: "old", PresharedKey: "old"}); err != nil {
		t.Fatalf("save initial state: %v", err)
	}
	oldSave := saveTUIState
	saveTUIState = func(uint64, *config.RuntimeState) error { return errors.New("disk full") }
	defer func() { saveTUIState = oldSave }()

	updater := &fakeTunnelPSKUpdater{}
	m := Model{
		state: StateConnected, sessionID: "old", tunnel: updater,
		cfg: &config.ClientConfig{}, sessionCoordinator: &sessionCoordinator{},
		lifecycleGeneration: generation,
	}
	newExpiry := time.Now().Add(time.Hour)
	updatedModel, _ := m.Update(renewResultMsg{session: &api.SessionResponse{
		SessionID: "new", PresharedKey: "new-psk", ExpiresAt: api.FlexTime{Time: newExpiry},
	}})
	updated := updatedModel.(Model)
	if updater.psk != "new-psk" {
		t.Fatalf("PSK was not applied before persist: %q", updater.psk)
	}
	if updated.lastRenewError == "" {
		t.Fatal("state save failure was hidden")
	}
	if updated.sessionID != "old" {
		t.Fatalf("uncommitted session published as %q", updated.sessionID)
	}
	if !updated.expiresAt.Equal(newExpiry) {
		t.Fatal("applied expiry not retained to suppress double renew")
	}
}

func TestDurablyStaleConnectResultTearsDownWithoutPublishing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	generation, err := config.BeginLifecycle(false)
	if err != nil {
		t.Fatalf("begin lifecycle: %v", err)
	}
	if _, err := config.BeginLifecycle(true); err != nil {
		t.Fatalf("invalidate lifecycle: %v", err)
	}

	oldCleanup := cleanupStaleTUIDNS
	cleanupCalls := 0
	cleanupStaleTUIDNS = func() error {
		cleanupCalls++
		return nil
	}
	defer func() { cleanupStaleTUIDNS = oldCleanup }()

	updater := &fakeTunnelPSKUpdater{}
	m := Model{
		state:     StateConnecting,
		sessionID: "existing-session",
		tunnel:    updater,
		cfg:       &config.ClientConfig{},
	}
	updatedModel, cmd := m.Update(connectResultMsg{
		session: &api.SessionResponse{
			SessionID: "stale-session",
			ExpiresAt: api.FlexTime{Time: time.Now().Add(time.Hour)},
		},
		tunnel:              updater,
		lifecycleGeneration: generation,
	})
	updated := updatedModel.(Model)

	if cmd != nil {
		t.Fatal("stale connect result scheduled follow-up work")
	}
	if updater.downCalls != 1 {
		t.Fatalf("Down() calls = %d, want 1", updater.downCalls)
	}
	if cleanupCalls != 1 {
		t.Fatalf("stale DNS cleanup calls = %d, want 1", cleanupCalls)
	}
	if updated.state != StateDisconnected {
		t.Fatalf("state = %s, want %s", updated.state, StateDisconnected)
	}
	if updated.tunnel != nil {
		t.Fatal("stale tunnel was retained")
	}
	if updated.sessionID == "stale-session" {
		t.Fatal("stale session was published")
	}
}

func TestRenewDoesNotPublishWhenLifecycleStalesAfterSave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	generation, err := config.BeginLifecycle(false)
	if err != nil {
		t.Fatalf("begin lifecycle: %v", err)
	}
	if err := config.SaveStateForLifecycle(generation, &config.RuntimeState{SessionID: "old", PresharedKey: "old"}); err != nil {
		t.Fatalf("save initial state: %v", err)
	}

	oldSave := saveTUIState
	saveTUIState = func(generation uint64, state *config.RuntimeState) error {
		if err := oldSave(generation, state); err != nil {
			return err
		}
		_, err := config.BeginLifecycle(true)
		return err
	}
	defer func() { saveTUIState = oldSave }()

	updater := &fakeTunnelPSKUpdater{}
	m := Model{
		state: StateConnected, sessionID: "old", tunnel: updater,
		cfg: &config.ClientConfig{}, sessionCoordinator: &sessionCoordinator{},
		lifecycleGeneration: generation,
	}
	updatedModel, _ := m.Update(renewResultMsg{session: &api.SessionResponse{
		SessionID: "stale", PresharedKey: "stale-psk", ExpiresAt: api.FlexTime{Time: time.Now().Add(time.Hour)},
	}})
	updated := updatedModel.(Model)
	if updater.psk != "stale-psk" {
		t.Fatalf("PSK was not applied before save: %q", updater.psk)
	}
	if updated.sessionID != "old" {
		t.Fatalf("stale session published as %q", updated.sessionID)
	}
	if updated.lastRenewError == "" {
		t.Fatal("post-save stale lifecycle was not surfaced")
	}
}

func TestDirectLifecycleRejectsNewerConnectedGenerationBeforeApply(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	generation, err := config.BeginLifecycle(false)
	if err != nil {
		t.Fatalf("begin lifecycle: %v", err)
	}
	coordinator := &sessionCoordinator{}
	coordinator.generation.Store(7)
	m := Model{
		connectionGeneration: 7,
		lifecycleGeneration:  generation,
		sessionCoordinator:   coordinator,
	}
	if !m.lifecycleCurrent(7) {
		t.Fatal("current direct lifecycle was rejected")
	}
	if _, err := config.BeginLifecycle(false); err != nil {
		t.Fatalf("supersede with connected lifecycle: %v", err)
	}
	if m.lifecycleCurrent(7) {
		t.Fatal("older direct lifecycle remained current after connected supersession")
	}
}

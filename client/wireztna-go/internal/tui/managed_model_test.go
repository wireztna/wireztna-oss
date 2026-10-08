package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wireztna/client/desktop/controller"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
)

type fakeManagedBackend struct {
	started         int
	connects        []ManagedSelection
	switches        []ManagedSelection
	disconnects     int
	closes          int
	operationResult ManagedOperationResult
	operationErr    error
}

func (f *fakeManagedBackend) Start(context.Context) (ManagedSnapshot, error) {
	f.started++
	return f.operationResult.Snapshot, f.operationErr
}
func (f *fakeManagedBackend) NextUpdate(ctx context.Context) (ManagedUpdate, error) {
	<-ctx.Done()
	return ManagedUpdate{}, ctx.Err()
}
func (f *fakeManagedBackend) Connect(_ context.Context, selection ManagedSelection) (ManagedOperationResult, error) {
	f.connects = append(f.connects, selection)
	return f.operationResult, f.operationErr
}
func (f *fakeManagedBackend) Switch(_ context.Context, selection ManagedSelection) (ManagedOperationResult, error) {
	f.switches = append(f.switches, selection)
	return f.operationResult, f.operationErr
}
func (f *fakeManagedBackend) Disconnect(context.Context) (ManagedOperationResult, error) {
	f.disconnects++
	return f.operationResult, f.operationErr
}
func (f *fakeManagedBackend) Close() error { f.closes++; return nil }

func managedModelSnapshot(state controller.ConnectionState, exitNode string) ManagedSnapshot {
	health := controller.Health{
		Healthy: true, WireGuard: controller.HealthHealthy, Routes: controller.HealthHealthy,
		DNS: controller.HealthHealthy, EndToEnd: controller.HealthHealthy,
	}
	return ManagedSnapshot{
		State:   state,
		Desired: ManagedSelection{GroupID: "group-1", ExitNodeID: exitNode}, DesiredConnected: true,
		Applied: ManagedSelection{GroupID: "group-1", ExitNodeID: exitNode}, AppliedConnected: state == controller.ConnectionStateConnected,
		Health: health,
		Projects: []ManagedProject{{
			ID: "group-1", Name: "Engineering", OnlineResources: 1, CIDRs: []string{"10.10.0.0/16"},
			Resources: []ManagedResource{{ID: "exit-1", Name: "Madrid exit", Status: "online", ExposedCIDRs: []string{"10.10.0.0/16"}}},
		}},
		ExitNodes: []ManagedExitNode{{ID: "exit-1", Name: "Madrid", Location: "ES", Status: "online"}},
		VPNMode:   true, StreamID: "stream-1", Epoch: 1, Sequence: 4,
	}
}

func newManagedTestModel(backend ManagedBackend) Model {
	return NewManagedModel(api.NewClient("https://control.example"), &config.ClientConfig{}, backend)
}

func TestManagedSnapshotMapsOnlyCatalogAndControllerState(t *testing.T) {
	model := newManagedTestModel(&fakeManagedBackend{})
	model.managedReady = true
	model.applyManagedSnapshot(managedModelSnapshot(controller.ConnectionStateConnected, "exit-1"))

	if model.state != StateConnected || model.groupID != "group-1" || model.groupName != "Engineering" {
		t.Fatalf("managed state mapping = state %s group %q/%q", model.state, model.groupID, model.groupName)
	}
	if model.exitNodeID != "exit-1" || model.selectedExitNode != 1 {
		t.Fatalf("exit mapping = %q index %d", model.exitNodeID, model.selectedExitNode)
	}
	if model.overlayIP != "" || model.endpoint != "" || model.sessionID != "" || model.rxBytes != 0 || model.txBytes != 0 {
		t.Fatal("managed snapshot manufactured unavailable tunnel/session telemetry")
	}
	view := model.renderGroupsTab(100)
	if strings.Contains(view, "All groups") {
		t.Fatal("managed project view exposed All groups")
	}
	if !strings.Contains(view, "Engineering") || !strings.Contains(view, "Madrid") {
		t.Fatalf("managed catalog missing from view: %q", view)
	}
}

func TestManagedExitAndSplitSelectionsPreserveConcreteGroup(t *testing.T) {
	backend := &fakeManagedBackend{operationResult: ManagedOperationResult{Snapshot: managedModelSnapshot(controller.ConnectionStateConnected, "exit-1")}}
	model := newManagedTestModel(backend)
	model.managedReady = true
	model.applyManagedSnapshot(managedModelSnapshot(controller.ConnectionStateConnected, ""))
	model.tab = 1
	model.cursorInExit = true
	model.cursor = 0

	updatedModel, _ := model.selectManagedCursor()
	updated := updatedModel.(Model)
	if updated.groupID != "group-1" || updated.exitNodeID != "exit-1" {
		t.Fatalf("exit selection lost group: group=%q exit=%q", updated.groupID, updated.exitNodeID)
	}
	message := updated.managedMutationCmd(managedSwitch, ManagedSelection{GroupID: updated.groupID, ExitNodeID: updated.exitNodeID})()
	if message == nil || len(backend.switches) != 1 {
		t.Fatal("managed exit switch was not delivered")
	}
	if backend.switches[0] != (ManagedSelection{GroupID: "group-1", ExitNodeID: "exit-1"}) {
		t.Fatalf("exit switch selection = %#v", backend.switches[0])
	}

	updated.exitNodeID = ""
	updated.selectedExitNode = 0
	_ = updated.managedMutationCmd(managedSwitch, ManagedSelection{GroupID: updated.groupID})()
	if len(backend.switches) != 2 || backend.switches[1].GroupID != "group-1" || backend.switches[1].ExitNodeID != "" {
		t.Fatalf("split switch selection = %#v", backend.switches)
	}
}

func TestManagedAllGroupsIsRejected(t *testing.T) {
	model := newManagedTestModel(&fakeManagedBackend{})
	model.managedReady = true
	model.applyManagedSnapshot(managedModelSnapshot(controller.ConnectionStateDisconnected, ""))
	model.tab = 1

	updatedModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	updated := updatedModel.(Model)
	if !strings.Contains(updated.errorMsg, "All groups is unavailable") {
		t.Fatalf("All groups error = %q", updated.errorMsg)
	}
}

func TestManagedQuitClosesBackendWithoutDisconnect(t *testing.T) {
	backend := &fakeManagedBackend{}
	model := newManagedTestModel(backend)
	model.managedReady = true
	model.state = StateConnected

	_, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if command == nil {
		t.Fatal("quit did not return tea.Quit")
	}
	if backend.closes != 1 {
		t.Fatalf("Close calls = %d, want 1", backend.closes)
	}
	if backend.disconnects != 0 {
		t.Fatalf("quit sent %d disconnect operations", backend.disconnects)
	}
}

func TestManagedConnectRequiresReadyServiceAndConcreteGroup(t *testing.T) {
	backend := &fakeManagedBackend{}
	model := newManagedTestModel(backend)
	model.state = StateDisconnected
	updatedModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	updated := updatedModel.(Model)
	if len(backend.connects) != 0 || !strings.Contains(updated.errorMsg, "no direct fallback") {
		t.Fatalf("unavailable service fallback behavior: calls=%d error=%q", len(backend.connects), updated.errorMsg)
	}

	updated.managedReady = true
	updated.groups = []api.GroupInfo{{ID: "group-1", Name: "Engineering", OnlinePublishers: 1}}
	updated.groupID = ""
	updatedModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	updated = updatedModel.(Model)
	if len(backend.connects) != 0 || !strings.Contains(updated.errorMsg, "concrete project") {
		t.Fatalf("empty group behavior: calls=%d error=%q", len(backend.connects), updated.errorMsg)
	}
}

func TestManagedSnapshotAcceptsLowerSequenceFromNewStream(t *testing.T) {
	model := newManagedTestModel(&fakeManagedBackend{})
	oldSnapshot := managedModelSnapshot(controller.ConnectionStateConnected, "")
	oldSnapshot.StreamID = "old-stream"
	oldSnapshot.Sequence = 99
	model.applyManagedSnapshot(oldSnapshot)

	newSnapshot := managedModelSnapshot(controller.ConnectionStateDisconnected, "")
	newSnapshot.StreamID = "new-stream"
	newSnapshot.Sequence = 0
	newSnapshot.DesiredConnected = false
	newSnapshot.AppliedConnected = false
	model.applyManagedSnapshot(newSnapshot)

	if model.managedStreamID != "new-stream" || model.managedSequence != 0 {
		t.Fatalf("stream cursor = %q/%d, want new-stream/0", model.managedStreamID, model.managedSequence)
	}
	if model.state != StateDisconnected {
		t.Fatalf("state = %s, new stream snapshot was discarded", model.state)
	}
}

func TestManagedOperationFailureKeepsFinalAuthoritativeState(t *testing.T) {
	model := newManagedTestModel(&fakeManagedBackend{})
	model.managedReady = true
	finalSnapshot := managedModelSnapshot(controller.ConnectionStateConnected, "")
	updatedModel, _ := model.Update(managedOperationMsg{
		result: ManagedOperationResult{OperationID: "failed-switch", Snapshot: finalSnapshot},
		err:    errors.New("switch failed"),
	})
	updated := updatedModel.(Model)
	if updated.state != StateConnected {
		t.Fatalf("state = %s, want final snapshot state connected", updated.state)
	}
	if !strings.Contains(updated.errorMsg, "switch failed") {
		t.Fatalf("operation error = %q", updated.errorMsg)
	}
}

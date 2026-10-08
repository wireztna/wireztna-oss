package controller

import (
	"reflect"
	"strings"
	"testing"
)

func TestConnectionStateContract(t *testing.T) {
	states := []ConnectionState{
		ConnectionStateDisconnected,
		ConnectionStateConnecting,
		ConnectionStateConnected,
		ConnectionStateReconnecting,
		ConnectionStateDegraded,
		ConnectionStateAuthRequired,
		ConnectionStateUpdateRequired,
	}
	want := []string{
		"disconnected",
		"connecting",
		"connected",
		"reconnecting",
		"degraded",
		"auth_required",
		"update_required",
	}

	if len(states) != len(want) {
		t.Fatalf("state count = %d, want %d", len(states), len(want))
	}
	for i := range states {
		if got := string(states[i]); got != want[i] {
			t.Errorf("state %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestCommandKindsCoverControllerIntents(t *testing.T) {
	commands := map[CommandKind]bool{
		CommandConnect:       true,
		CommandDisconnect:    true,
		CommandSwitch:        true,
		CommandRenew:         true,
		CommandWake:          true,
		CommandNetworkChange: true,
	}

	for _, kind := range []CommandKind{
		"connect",
		"disconnect",
		"switch",
		"renew",
		"wake",
		"network_change",
	} {
		if !commands[kind] {
			t.Errorf("controller intent %q is not represented", kind)
		}
	}
	if commands[CommandKind("quit")] {
		t.Error("normal controller commands must not include service termination")
	}
}

func TestSnapshotKeepsDesiredAndAppliedStateDistinct(t *testing.T) {
	snapshot := Snapshot{
		State: ConnectionStateConnecting,
		Desired: DesiredState{
			ConfigurationID: "configuration",
			Generation:      18,
			GroupID:         "production",
			ExitNodeID:      "madrid",
			Connected:       true,
		},
		Applied: AppliedState{
			ConfigurationID: "configuration",
			Generation:      17,
			GroupID:         "engineering",
			Connected:       true,
		},
		Health: Health{
			Healthy:   false,
			WireGuard: HealthHealthy,
			Routes:    HealthHealthy,
			DNS:       HealthUnhealthy,
			EndToEnd:  HealthUnknown,
		},
		Sequence: 14,
	}

	if snapshot.Desired.Generation == snapshot.Applied.Generation {
		t.Fatal("desired generation must remain distinguishable from applied generation")
	}
	if snapshot.Desired.GroupID == snapshot.Applied.GroupID {
		t.Fatal("desired group must remain distinguishable from applied group")
	}
	if snapshot.Health.Healthy {
		t.Fatal("aggregate health must be able to report an unhealthy snapshot")
	}
	if snapshot.Health.DNS != HealthUnhealthy {
		t.Fatal("health must identify the failed DNS dimension")
	}
	if snapshot.State == ConnectionStateConnected {
		t.Fatal("partially healthy state fixture must not report connected")
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Snapshot.Validate() unexpected error: %v", err)
	}
}

func TestSnapshotValidationRejectsContradictoryConnectedState(t *testing.T) {
	snapshot := Snapshot{
		State:   ConnectionStateConnected,
		Applied: AppliedState{Connected: true},
		Health: Health{
			Healthy:   true,
			WireGuard: HealthHealthy,
			Routes:    HealthHealthy,
			DNS:       HealthUnhealthy,
			EndToEnd:  HealthHealthy,
		},
	}

	if err := snapshot.Validate(); err == nil {
		t.Fatal("Snapshot.Validate() error = nil, want contradictory health error")
	}

	snapshot.Health.DNS = HealthHealthy
	snapshot.Applied.Connected = false
	if err := snapshot.Validate(); err == nil {
		t.Fatal("Snapshot.Validate() error = nil, want unapplied connected-state error")
	}

	snapshot.Applied.Connected = true
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Snapshot.Validate() unexpected error: %v", err)
	}
}

func TestEventContractSupportsOrderedSingleTerminalResult(t *testing.T) {
	const operationID OperationID = "operation-17"
	events := []Event{
		{Sequence: 21, Kind: EventOperationStarted, OperationID: operationID},
		{
			Sequence:    22,
			Kind:        EventOperationProgress,
			OperationID: operationID,
			Progress:    &Progress{Stage: ProgressStageConfiguringWireGuard},
		},
		{Sequence: 23, Kind: EventOperationSucceeded, OperationID: operationID},
	}

	terminalCount := 0
	for i, event := range events {
		if err := event.Validate(); err != nil {
			t.Fatalf("event %d Validate() error = %v", i, err)
		}
		if event.OperationID != operationID {
			t.Fatalf("event %d operation ID = %q, want %q", i, event.OperationID, operationID)
		}
		if i > 0 && event.Sequence <= events[i-1].Sequence {
			t.Fatalf("event sequence is not monotonic at index %d", i)
		}
		if event.Kind == EventOperationSucceeded || event.Kind == EventOperationFailed {
			terminalCount++
		}
	}
	if terminalCount != 1 {
		t.Fatalf("terminal event count = %d, want 1", terminalCount)
	}
}

func TestPublicDomainFieldsExcludeCredentialNames(t *testing.T) {
	forbidden := []string{"privatekey", "jwt", "psk", "token", "secret"}
	types := []reflect.Type{
		reflect.TypeOf(Command{}),
		reflect.TypeOf(DesiredState{}),
		reflect.TypeOf(AppliedState{}),
		reflect.TypeOf(Health{}),
		reflect.TypeOf(Operation{}),
		reflect.TypeOf(Event{}),
		reflect.TypeOf(Snapshot{}),
	}

	seen := make(map[reflect.Type]bool)
	var inspect func(reflect.Type)
	inspect = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true

		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.ToLower(field.Name)
			for _, term := range forbidden {
				if strings.Contains(name, term) {
					t.Errorf("%s.%s exposes forbidden credential field name %q", typ.Name(), field.Name, term)
				}
			}
			inspect(field.Type)
		}
	}

	for _, typ := range types {
		inspect(typ)
	}
}

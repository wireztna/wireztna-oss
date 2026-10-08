package controller

import (
	"errors"
	"reflect"
	"testing"
)

func TestStableErrorCodeContract(t *testing.T) {
	codes := []ErrorCode{
		ErrorCodeUnauthorized,
		ErrorCodeUnsupportedVersion,
		ErrorCodeInvalidArgument,
		ErrorCodeDeadlineExceeded,
		ErrorCodeConflict,
		ErrorCodeServiceUnavailable,
		ErrorCodeDegraded,
		ErrorCodeReauthRequired,
	}
	want := []ErrorCode{
		"UNAUTHORIZED",
		"UNSUPPORTED_VERSION",
		"INVALID_ARGUMENT",
		"DEADLINE_EXCEEDED",
		"CONFLICT",
		"SERVICE_UNAVAILABLE",
		"DEGRADED",
		"REAUTH_REQUIRED",
	}
	if !reflect.DeepEqual(codes, want) {
		t.Fatalf("error codes = %#v, want %#v", codes, want)
	}
	for _, code := range codes {
		if err := (Error{Code: code}).Validate(); err != nil {
			t.Errorf("Error{%q}.Validate() error = %v", code, err)
		}
	}
	if err := (Error{Code: "UNKNOWN"}).Validate(); err == nil {
		t.Fatal("unknown Error.Validate() error = nil")
	}
}

func TestStructuredErrorImplementsErrorAndCanBeUnwrapped(t *testing.T) {
	structured := &Error{Code: ErrorCodeConflict, Detail: "operation already active"}
	var err error = structured
	if got := err.Error(); got != "CONFLICT: operation already active" {
		t.Fatalf("Error() = %q", got)
	}
	got, ok := AsError(errors.Join(errors.New("outer"), err))
	if !ok || got != structured {
		t.Fatalf("AsError() = (%v, %v), want original structured error", got, ok)
	}
}

func TestProgressStageContract(t *testing.T) {
	stages := []ProgressStage{
		ProgressStageAuthenticating,
		ProgressStageRequestingAccess,
		ProgressStageConfiguringWireGuard,
		ProgressStageConfiguringRoutes,
		ProgressStageConfiguringDNS,
		ProgressStageVerifyingConnection,
		ProgressStageConnected,
	}
	want := []ProgressStage{
		"authenticating",
		"requesting_access",
		"configuring_wireguard",
		"configuring_routes",
		"configuring_dns",
		"verifying_connection",
		"connected",
	}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("progress stages = %#v, want %#v", stages, want)
	}
	for _, stage := range stages {
		if err := (Progress{Stage: stage}).Validate(); err != nil {
			t.Errorf("Progress{%q}.Validate() error = %v", stage, err)
		}
	}
	if err := (Progress{Stage: "unknown"}).Validate(); err == nil {
		t.Fatal("unknown Progress.Validate() error = nil")
	}
}

func TestEventValidateEnforcesStructuredPayloadShape(t *testing.T) {
	validProgress := &Progress{Stage: ProgressStageConfiguringDNS}
	validError := &Error{Code: ErrorCodeDegraded, Detail: "DNS health check failed"}

	tests := []struct {
		name    string
		event   Event
		wantErr bool
	}{
		{name: "started", event: Event{Kind: EventOperationStarted, OperationID: "op"}},
		{name: "progress", event: Event{Kind: EventOperationProgress, OperationID: "op", Progress: validProgress}},
		{name: "failed", event: Event{Kind: EventOperationFailed, OperationID: "op", Error: validError}},
		{name: "succeeded", event: Event{Kind: EventOperationSucceeded, OperationID: "op"}},
		{name: "snapshot", event: Event{Kind: EventSnapshotChanged, State: ConnectionStateDisconnected}},
		{name: "missing operation ID", event: Event{Kind: EventOperationStarted}, wantErr: true},
		{name: "missing progress", event: Event{Kind: EventOperationProgress, OperationID: "op"}, wantErr: true},
		{name: "progress with error", event: Event{Kind: EventOperationProgress, OperationID: "op", Progress: validProgress, Error: validError}, wantErr: true},
		{name: "unknown progress stage", event: Event{Kind: EventOperationProgress, OperationID: "op", Progress: &Progress{Stage: "unknown"}}, wantErr: true},
		{name: "missing failure error", event: Event{Kind: EventOperationFailed, OperationID: "op"}, wantErr: true},
		{name: "unknown failure code", event: Event{Kind: EventOperationFailed, OperationID: "op", Error: &Error{Code: "UNKNOWN"}}, wantErr: true},
		{name: "payload on success", event: Event{Kind: EventOperationSucceeded, OperationID: "op", Progress: validProgress}, wantErr: true},
		{name: "payload on snapshot", event: Event{Kind: EventSnapshotChanged, Error: validError}, wantErr: true},
		{name: "unknown event kind", event: Event{Kind: "unknown"}, wantErr: true},
		{name: "unknown state", event: Event{Kind: EventSnapshotChanged, State: "unknown"}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.event.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Event.Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

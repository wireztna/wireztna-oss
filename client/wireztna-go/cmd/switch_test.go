package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type fakeGroupSwitchLifecycle struct {
	groupID string
	err     error
}

func (f *fakeGroupSwitchLifecycle) SendSwitch(groupID string) error {
	f.groupID = groupID
	return f.err
}

func TestRunSwitchFailsBeforeSessionWorkWithoutLifecycle(t *testing.T) {
	originalResolver := resolveGroupSwitchLifecycle
	defer func() { resolveGroupSwitchLifecycle = originalResolver }()

	resolveGroupSwitchLifecycle = func() (groupSwitchLifecycle, error) {
		return nil, errors.New("no safe lifecycle")
	}

	oldAPIURL := viper.GetString("api_url")
	viper.Set("api_url", "https://control.example")
	defer viper.Set("api_url", oldAPIURL)

	err := runSwitch(&cobra.Command{}, []string{"engineering"})
	if err == nil || !strings.Contains(err.Error(), "no safe lifecycle") {
		t.Fatalf("runSwitch() error = %v, want lifecycle preflight failure", err)
	}
}

func TestSwitchGroupDelegatesAndPropagatesDataplaneFailure(t *testing.T) {
	failure := errors.New("dataplane transition failed")
	lifecycle := &fakeGroupSwitchLifecycle{err: failure}

	err := switchGroup(lifecycle, "group-123")
	if !errors.Is(err, failure) {
		t.Fatalf("switchGroup() error = %v, want %v", err, failure)
	}
	if lifecycle.groupID != "group-123" {
		t.Fatalf("SendSwitch() group = %q, want group-123", lifecycle.groupID)
	}
}

func TestSwitchGroupSucceedsOnlyAfterLifecycleSuccess(t *testing.T) {
	lifecycle := &fakeGroupSwitchLifecycle{}

	if err := switchGroup(lifecycle, "group-456"); err != nil {
		t.Fatalf("switchGroup() error = %v", err)
	}
	if lifecycle.groupID != "group-456" {
		t.Fatalf("SendSwitch() group = %q, want group-456", lifecycle.groupID)
	}
}

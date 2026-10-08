package cmd

import (
	"errors"
	"testing"
)

type fakeDisconnectDNS struct{ err error }

func (f fakeDisconnectDNS) Cleanup() error { return f.err }

type fakeDisconnectIPC struct{ err error }

func (f fakeDisconnectIPC) SendDisconnect() error { return f.err }

func TestRunDisconnectPreservesAllDirectCleanupErrors(t *testing.T) {
	stateErr := errors.New("state write failed")
	tunnelErr := errors.New("tunnel teardown failed")
	dnsErr := errors.New("DNS cleanup failed")
	oldPersist, oldAvailable := persistDisconnectIntent, disconnectServiceAvailable
	oldDown, oldDNS := downDirectTunnel, newDisconnectDNSManager
	persistDisconnectIntent = func() error { return stateErr }
	disconnectServiceAvailable = func() bool { return false }
	downDirectTunnel = func(string, string) error { return tunnelErr }
	newDisconnectDNSManager = func() disconnectDNSManager { return fakeDisconnectDNS{err: dnsErr} }
	defer func() {
		persistDisconnectIntent, disconnectServiceAvailable = oldPersist, oldAvailable
		downDirectTunnel, newDisconnectDNSManager = oldDown, oldDNS
	}()

	err := runDisconnect(nil, nil)
	for _, want := range []error{stateErr, tunnelErr, dnsErr} {
		if !errors.Is(err, want) {
			t.Fatalf("runDisconnect() error = %v, missing %v", err, want)
		}
	}
}

func TestRunDisconnectPreservesIntentAndServiceErrors(t *testing.T) {
	stateErr := errors.New("state write failed")
	serviceErr := errors.New("service teardown failed")
	oldPersist, oldAvailable, oldFactory := persistDisconnectIntent, disconnectServiceAvailable, newDisconnectIPCClient
	persistDisconnectIntent = func() error { return stateErr }
	disconnectServiceAvailable = func() bool { return true }
	newDisconnectIPCClient = func() disconnectIPCClient { return fakeDisconnectIPC{err: serviceErr} }
	defer func() {
		persistDisconnectIntent, disconnectServiceAvailable, newDisconnectIPCClient = oldPersist, oldAvailable, oldFactory
	}()

	err := runDisconnect(nil, nil)
	if !errors.Is(err, stateErr) || !errors.Is(err, serviceErr) {
		t.Fatalf("runDisconnect() error = %v, want both failures", err)
	}
}

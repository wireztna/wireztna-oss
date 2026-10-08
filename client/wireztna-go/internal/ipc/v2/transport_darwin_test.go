//go:build darwin

package v2

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestServeDarwinTransportAuthenticatesPeerAndCleansOwnSocket(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	config := darwinTestConfig(path)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	authenticated := make(chan PeerIdentity, 1)
	go func() {
		result <- serveDarwinTransport(ctx, config, getpeereidUID, func(handlerContext context.Context, _ io.ReadWriteCloser, authentication peerAuthentication) error {
			authenticated <- authentication.authenticatedPeerIdentity()
			<-handlerContext.Done()
			return handlerContext.Err()
		})
	}()

	connection := dialDarwinTestSocket(t, path)
	defer connection.Close()

	select {
	case identity := <-authenticated:
		if identity.Kind() != IdentityKindUID || identity.ID() != strconv.Itoa(os.Geteuid()) {
			t.Fatalf("peer identity = %s:%s, want uid:%d", identity.Kind(), identity.ID(), os.Geteuid())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for authenticated connection")
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != darwinSocketMode {
		t.Fatalf("socket mode = %v, want socket %04o", info.Mode(), darwinSocketMode)
	}

	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve result = %v, want context canceled", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains after shutdown: %v", err)
	}
}

func TestServeDarwinTransportRejectsMismatchedUIDBeforeHandler(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	config := darwinTestConfig(path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	handled := make(chan struct{}, 1)
	go func() {
		result <- serveDarwinTransport(ctx, config, func(*net.UnixConn) (uint64, error) {
			return config.OwnerUID + 1, nil
		}, func(context.Context, io.ReadWriteCloser, peerAuthentication) error {
			handled <- struct{}{}
			return nil
		})
	}()

	connection := dialDarwinTestSocket(t, path)
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	var payload [1]byte
	if _, err := connection.Read(payload[:]); err == nil {
		t.Fatal("mismatched peer connection was not closed")
	}
	select {
	case <-handled:
		t.Fatal("handler ran for a mismatched peer UID")
	default:
	}

	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve result = %v, want context canceled", err)
	}
}

func TestServeDarwinTransportRejectsStaleSymlink(t *testing.T) {
	parent := darwinTempDir(t)
	target := filepath.Join(parent, "target")
	if err := os.WriteFile(target, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	path := filepath.Join(parent, "desktop.sock")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("create stale symlink: %v", err)
	}

	err := serveDarwinTransport(context.Background(), darwinTestConfig(path), getpeereidUID, func(context.Context, io.ReadWriteCloser, peerAuthentication) error {
		return nil
	})
	if err == nil {
		t.Fatal("served over a symlink path")
	}
	info, lstatErr := os.Lstat(path)
	if lstatErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was removed or replaced: info=%v err=%v", info, lstatErr)
	}
}

func TestServeDarwinTransportReplacesVerifiedStaleSocket(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := os.Chmod(path, darwinSocketMode); err != nil {
		t.Fatalf("chmod stale socket: %v", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serveDarwinTransport(ctx, darwinTestConfig(path), getpeereidUID, func(handlerContext context.Context, _ io.ReadWriteCloser, _ peerAuthentication) error {
			<-handlerContext.Done()
			return handlerContext.Err()
		})
	}()
	connection := dialDarwinTestSocket(t, path)
	_ = connection.Close()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve result = %v, want context canceled", err)
	}
}

func TestDarwinHandshakeTimeoutClosesSilentPeer(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	config := darwinTestConfig(path)
	config.HandshakeTimeout = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	handlerDone := make(chan struct{}, 1)
	go func() {
		result <- serveDarwinTransport(ctx, config, getpeereidUID, func(_ context.Context, connection io.ReadWriteCloser, _ peerAuthentication) error {
			defer func() { handlerDone <- struct{}{} }()
			var payload [1]byte
			_, err := connection.Read(payload[:])
			return err
		})
	}()
	connection := dialDarwinTestSocket(t, path)
	defer connection.Close()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handshake timeout did not close the silent peer")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve result = %v, want context canceled", err)
	}
}

func darwinTestConfig(path string) DarwinTransportConfig {
	return DarwinTransportConfig{
		SocketPath:       path,
		OwnerUID:         uint64(os.Geteuid()),
		MaxConnections:   4,
		HandshakeTimeout: time.Second,
		IdleTimeout:      time.Second,
	}
}

func dialDarwinTestSocket(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if err == nil {
			return connection
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial Darwin test socket: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func darwinTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "wz-ipc-")
	if err != nil {
		t.Fatalf("create short Darwin temp directory: %v", err)
	}
	if err := os.Chmod(directory, darwinParentMode); err != nil {
		t.Fatalf("set short Darwin temp directory mode: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func TestServeDarwinTransportDoesNotRemoveReplacementPath(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serveDarwinTransport(ctx, darwinTestConfig(path), getpeereidUID, func(context.Context, io.ReadWriteCloser, peerAuthentication) error {
			return nil
		})
	}()

	connection := dialDarwinTestSocket(t, path)
	_ = connection.Close()
	if err := os.Remove(path); err != nil {
		t.Fatalf("unlink owned socket before replacement: %v", err)
	}
	const replacement = "replacement must survive cleanup"
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatalf("create replacement path: %v", err)
	}

	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve result = %v, want context canceled", err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("replacement path was removed: %v", err)
	}
	if string(payload) != replacement {
		t.Fatalf("replacement contents = %q, want %q", payload, replacement)
	}
}

func TestRemoveVerifiedStaleDarwinSocketKeepsActiveListener(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on active socket: %v", err)
	}
	listener.SetUnlinkOnClose(false)
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(path)
	})
	if err := os.Chmod(path, darwinSocketMode); err != nil {
		t.Fatalf("chmod active socket: %v", err)
	}

	if err := removeVerifiedStaleDarwinSocket(path, uint32(os.Geteuid())); err == nil {
		t.Fatal("active socket was classified as stale")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("active socket path was removed: info=%v err=%v", info, err)
	}
}

func TestUIDFromDarwinCredentialRejectsUnknownVersion(t *testing.T) {
	if _, err := uidFromDarwinCredential(&unix.Xucred{Version: darwinXucredVersion + 1, Uid: uint32(os.Geteuid())}); err == nil {
		t.Fatal("accepted an unknown xucred version")
	}
	uid, err := uidFromDarwinCredential(&unix.Xucred{Version: darwinXucredVersion, Uid: uint32(os.Geteuid())})
	if err != nil || uid != uint64(os.Geteuid()) {
		t.Fatalf("valid xucred produced uid=%d err=%v", uid, err)
	}
}

func TestDarwinIdleTimeoutClosesInactiveCommandPeer(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	config := darwinTestConfig(path)
	config.IdleTimeout = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	handlerDone := make(chan struct{}, 1)
	go func() {
		result <- serveDarwinTransport(ctx, config, getpeereidUID, func(_ context.Context, connection io.ReadWriteCloser, _ peerAuthentication) error {
			defer func() { handlerDone <- struct{}{} }()
			var hello ClientHello
			if err := readJSON(connection, MaxCommandFrameSize, &hello); err != nil {
				return err
			}
			if err := writeJSON(connection, MaxCommandFrameSize, ServerHello{Kind: "hello", NegotiatedVersion: ProtocolVersion}); err != nil {
				return err
			}
			var payload [1]byte
			_, err := connection.Read(payload[:])
			return err
		})
	}()
	connection := dialDarwinTestSocket(t, path)
	defer connection.Close()
	hello := ClientHello{Kind: "hello", Channel: ChannelCommand, SupportedVersions: []uint32{ProtocolVersion}, Capabilities: DefaultCapabilities()}
	if err := writeJSON(connection, MaxCommandFrameSize, hello); err != nil {
		t.Fatalf("write command hello: %v", err)
	}
	var serverHello ServerHello
	if err := readJSON(connection, MaxCommandFrameSize, &serverHello); err != nil {
		t.Fatalf("read server hello: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("idle timeout did not close the inactive command peer")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve result = %v, want context canceled", err)
	}
}

func TestDarwinConnectionLimitRejectsExcessAndReleasesSlot(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	config := darwinTestConfig(path)
	config.MaxConnections = 1
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	entered := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	finished := make(chan struct{}, 2)
	go func() {
		result <- serveDarwinTransport(ctx, config, getpeereidUID, func(context.Context, io.ReadWriteCloser, peerAuthentication) error {
			entered <- struct{}{}
			<-release
			finished <- struct{}{}
			return nil
		})
	}()

	first := dialDarwinTestSocket(t, path)
	defer first.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first connection did not enter handler")
	}

	excess := dialDarwinTestSocket(t, path)
	_ = excess.SetReadDeadline(time.Now().Add(time.Second))
	var payload [1]byte
	if _, err := excess.Read(payload[:]); err == nil {
		t.Fatal("excess connection was not rejected")
	}
	_ = excess.Close()

	release <- struct{}{}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("first connection did not release its slot")
	}
	second := dialDarwinTestSocket(t, path)
	defer second.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("released connection slot was not reusable")
	}
	release <- struct{}{}
	<-finished
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve result = %v, want context canceled", err)
	}
}

func TestDarwinEventHelloWithoutSubscribeHitsIdleTimeout(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	config := darwinTestConfig(path)
	config.IdleTimeout = 40 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	handlerDone := make(chan struct{}, 1)
	go func() {
		result <- serveDarwinTransport(ctx, config, getpeereidUID, func(_ context.Context, connection io.ReadWriteCloser, _ peerAuthentication) error {
			defer func() { handlerDone <- struct{}{} }()
			var hello ClientHello
			if err := readJSON(connection, MaxCommandFrameSize, &hello); err != nil {
				return err
			}
			if err := writeJSON(connection, MaxCommandFrameSize, ServerHello{Kind: "hello", NegotiatedVersion: ProtocolVersion}); err != nil {
				return err
			}
			var payload [1]byte
			_, err := connection.Read(payload[:])
			return err
		})
	}()

	connection := dialDarwinTestSocket(t, path)
	defer connection.Close()
	hello := ClientHello{
		Kind:              "hello",
		Channel:           ChannelEvent,
		SupportedVersions: []uint32{ProtocolVersion},
		Capabilities:      DefaultCapabilities(),
	}
	if err := writeJSON(connection, MaxCommandFrameSize, hello); err != nil {
		t.Fatalf("write event hello: %v", err)
	}
	var serverHello ServerHello
	if err := readJSON(connection, MaxCommandFrameSize, &serverHello); err != nil {
		t.Fatalf("read server hello: %v", err)
	}

	select {
	case <-handlerDone:
	case <-time.After(4 * config.IdleTimeout):
		t.Fatal("event hello without subscribe bypassed the idle timeout")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve result = %v, want context canceled", err)
	}
}

func TestDarwinPartialEventServerHelloDoesNotBypassHandshakeTimeout(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	config := darwinTestConfig(path)
	config.HandshakeTimeout = 40 * time.Millisecond
	config.IdleTimeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	handlerDone := make(chan struct{}, 1)
	go func() {
		result <- serveDarwinTransport(ctx, config, getpeereidUID, func(_ context.Context, connection io.ReadWriteCloser, _ peerAuthentication) error {
			defer func() { handlerDone <- struct{}{} }()
			var hello ClientHello
			if err := readJSON(connection, MaxCommandFrameSize, &hello); err != nil {
				return err
			}
			if _, err := connection.Write([]byte{0, 0, 0, 64}); err != nil {
				return err
			}
			var payload [1]byte
			_, err := connection.Read(payload[:])
			return err
		})
	}()

	connection := dialDarwinTestSocket(t, path)
	defer connection.Close()
	hello := ClientHello{Kind: "hello", Channel: ChannelEvent, SupportedVersions: []uint32{ProtocolVersion}, Capabilities: DefaultCapabilities()}
	if err := writeJSON(connection, MaxCommandFrameSize, hello); err != nil {
		t.Fatalf("write event hello: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("event hello bypassed the server-response handshake timeout")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve result = %v, want context canceled", err)
	}
}

func TestServeDarwinTransportRejectsConcurrentServiceInstance(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	config := darwinTestConfig(path)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serveDarwinTransport(ctx, config, getpeereidUID, func(handlerContext context.Context, _ io.ReadWriteCloser, _ peerAuthentication) error {
			<-handlerContext.Done()
			return handlerContext.Err()
		})
	}()

	connection := dialDarwinTestSocket(t, path)
	defer connection.Close()
	secondErr := serveDarwinTransport(context.Background(), config, getpeereidUID, func(context.Context, io.ReadWriteCloser, peerAuthentication) error {
		return nil
	})
	if secondErr == nil {
		t.Fatal("concurrent Darwin service acquired the same instance lock")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("contending service altered active socket: info=%v err=%v", info, err)
	}
	lockInfo, err := os.Lstat(darwinServiceLockPath(path))
	if err != nil {
		t.Fatalf("inspect service lock: %v", err)
	}
	lockIdentity, err := validateDarwinServiceLock(lockInfo)
	if err != nil {
		t.Fatalf("validate service lock: %v", err)
	}
	if lockIdentity.uid != uint32(os.Geteuid()) {
		t.Fatalf("service lock uid = %d, want %d", lockIdentity.uid, os.Geteuid())
	}

	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve result = %v, want context canceled", err)
	}
	reacquired, err := acquireDarwinServiceLock(path)
	if err != nil {
		t.Fatalf("reacquire service lock after shutdown: %v", err)
	}
	if err := reacquired.release(); err != nil {
		t.Fatalf("release reacquired service lock: %v", err)
	}
}

func TestRemoveDarwinSocketIfSamePreservesReplacementAtCapture(t *testing.T) {
	path := filepath.Join(darwinTempDir(t), "desktop.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("create cleanup socket: %v", err)
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if err := os.Chmod(path, darwinSocketMode); err != nil {
		t.Fatalf("chmod cleanup socket: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect cleanup socket: %v", err)
	}
	expected, err := darwinIdentity(info)
	if err != nil {
		t.Fatalf("identify cleanup socket: %v", err)
	}

	const replacement = "replacement created at cleanup capture must survive"
	replaced := false
	renameExclusive := func(from, to string) error {
		if !replaced {
			replaced = true
			if err := os.Remove(path); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(replacement), darwinSocketMode); err != nil {
				return err
			}
		}
		return renameDarwinPathExclusive(from, to)
	}
	if err := removeDarwinSocketIfSameWithRename(path, expected, renameExclusive); err != nil {
		t.Fatalf("cleanup with replacement race: %v", err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("replacement path was not restored: %v", err)
	}
	if string(payload) != replacement {
		t.Fatalf("replacement contents = %q, want %q", payload, replacement)
	}
	matches, err := filepath.Glob(path + ".cleanup-*")
	if err != nil {
		t.Fatalf("glob cleanup captures: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("cleanup capture remains after restoration: %v", matches)
	}
}

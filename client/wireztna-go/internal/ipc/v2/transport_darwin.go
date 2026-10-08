//go:build darwin

package v2

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	darwinParentMode      = os.FileMode(0o755)
	darwinSocketMode      = os.FileMode(0o600)
	darwinActiveProbeTime = 100 * time.Millisecond
	darwinAcceptRetryTime = 25 * time.Millisecond
	darwinXucredVersion   = 0
)

type darwinPeerLookup func(*net.UnixConn) (uint64, error)
type darwinConnectionHandler func(context.Context, io.ReadWriteCloser, peerAuthentication) error

type darwinFileIdentity struct {
	device uint64
	inode  uint64
	uid    uint32
}

type darwinServiceLock struct {
	file *os.File
	once sync.Once
	err  error
}

var darwinCleanupSequence atomic.Uint64

// ServeDarwin serves authenticated IPC v2 connections on the single private
// Unix-domain socket reserved for the production macOS service. Arbitrary
// paths remain available only to the unexported transport seam used by tests.
func ServeDarwin(ctx context.Context, server *Server, config DarwinTransportConfig) error {
	if server == nil {
		return errors.New("Darwin IPC server is required")
	}
	normalized, err := config.normalized()
	if err != nil {
		return err
	}
	if normalized.SocketPath != DefaultDarwinSocketPath {
		return errors.New("Darwin IPC service socket path must use the fixed production endpoint")
	}
	return serveDarwinTransport(ctx, normalized, getpeereidUID, server.Serve)
}

func serveDarwinTransport(ctx context.Context, config DarwinTransportConfig, lookup darwinPeerLookup, handler darwinConnectionHandler) error {
	if ctx == nil {
		return errors.New("Darwin IPC context is required")
	}
	if lookup == nil || handler == nil {
		return errors.New("Darwin peer lookup and connection handler are required")
	}
	normalized, err := config.normalized()
	if err != nil {
		return err
	}

	if err := ensureDarwinParent(filepath.Dir(normalized.SocketPath)); err != nil {
		return err
	}
	serviceLock, err := acquireDarwinServiceLock(normalized.SocketPath)
	if err != nil {
		return err
	}

	listener, socketIdentity, err := listenDarwinSocket(normalized)
	if err != nil {
		return errors.Join(err, serviceLock.release())
	}
	stopClose := context.AfterFunc(ctx, func() { _ = listener.Close() })

	connections := make(chan struct{}, normalized.MaxConnections)
	var workers sync.WaitGroup
	var acceptErr error

acceptLoop:
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if networkError, ok := err.(net.Error); ok && networkError.Temporary() {
				select {
				case <-ctx.Done():
					break acceptLoop
				case <-time.After(darwinAcceptRetryTime):
					continue
				}
			}
			acceptErr = fmt.Errorf("accept Darwin IPC connection: %w", err)
			break
		}

		select {
		case connections <- struct{}{}:
		default:
			_ = connection.Close()
			continue
		}

		peerUID, err := lookup(connection)
		if err != nil || peerUID != normalized.OwnerUID {
			_ = connection.Close()
			<-connections
			continue
		}

		authentication := newUIDPeerAuthentication(peerUID)
		timedConnection := newDarwinTimedConnection(ctx, connection, normalized.HandshakeTimeout, normalized.IdleTimeout)
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-connections }()
			defer timedConnection.Close()
			_ = handler(ctx, timedConnection, authentication)
		}()
	}

	stopClose()
	_ = listener.Close()
	workers.Wait()
	cleanupErr := removeDarwinSocketIfSame(normalized.SocketPath, socketIdentity)
	lockErr := serviceLock.release()
	return errors.Join(ctx.Err(), acceptErr, cleanupErr, lockErr)
}

func listenDarwinSocket(config DarwinTransportConfig) (*net.UnixListener, darwinFileIdentity, error) {
	if err := removeVerifiedStaleDarwinSocket(config.SocketPath, uint32(config.OwnerUID)); err != nil {
		return nil, darwinFileIdentity{}, err
	}

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: config.SocketPath, Net: "unix"})
	if err != nil {
		return nil, darwinFileIdentity{}, fmt.Errorf("listen on Darwin IPC socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)

	initialInfo, err := os.Lstat(config.SocketPath)
	if err != nil {
		_ = listener.Close()
		return nil, darwinFileIdentity{}, fmt.Errorf("inspect newly-created Darwin IPC socket: %w", err)
	}
	initialIdentity, err := darwinIdentity(initialInfo)
	if err != nil {
		_ = listener.Close()
		return nil, darwinFileIdentity{}, err
	}
	cleanupOnError := func(cause error) error {
		closeErr := listener.Close()
		removeErr := removeDarwinSocketIfSame(config.SocketPath, initialIdentity)
		return errors.Join(cause, closeErr, removeErr)
	}

	if err := os.Chmod(config.SocketPath, darwinSocketMode); err != nil {
		return nil, darwinFileIdentity{}, cleanupOnError(fmt.Errorf("set Darwin IPC socket mode: %w", err))
	}
	if err := os.Chown(config.SocketPath, int(config.OwnerUID), -1); err != nil {
		return nil, darwinFileIdentity{}, cleanupOnError(fmt.Errorf("set Darwin IPC socket owner: %w", err))
	}
	finalInfo, err := os.Lstat(config.SocketPath)
	if err != nil {
		return nil, darwinFileIdentity{}, cleanupOnError(fmt.Errorf("verify Darwin IPC socket: %w", err))
	}
	finalIdentity, err := validateDarwinSocket(finalInfo, uint32(config.OwnerUID))
	if err != nil {
		return nil, darwinFileIdentity{}, cleanupOnError(err)
	}
	if finalIdentity.device != initialIdentity.device || finalIdentity.inode != initialIdentity.inode {
		return nil, darwinFileIdentity{}, cleanupOnError(errors.New("Darwin IPC socket changed during secure setup"))
	}
	return listener, finalIdentity, nil
}

func ensureDarwinParent(parent string) error {
	created := false
	initialInfo, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(parent, darwinParentMode); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create Darwin IPC parent directory: %w", err)
			}
		} else {
			created = true
		}
		initialInfo, err = os.Lstat(parent)
	}
	if err != nil {
		return fmt.Errorf("inspect Darwin IPC parent directory: %w", err)
	}
	if !initialInfo.IsDir() || initialInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("Darwin IPC parent must be a real directory")
	}
	initialIdentity, err := darwinIdentity(initialInfo)
	if err != nil {
		return err
	}
	if initialIdentity.uid != uint32(os.Geteuid()) {
		return errors.New("Darwin IPC parent must be owned by the service effective UID")
	}
	if !created && (initialInfo.Mode().Perm() != darwinParentMode || initialInfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0) {
		return errors.New("pre-existing Darwin IPC parent has unsafe permissions")
	}

	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open Darwin IPC parent directory: %w", err)
	}
	directory := os.NewFile(uintptr(fd), parent)
	if directory == nil {
		_ = unix.Close(fd)
		return errors.New("open Darwin IPC parent directory: invalid file descriptor")
	}
	defer directory.Close()

	openedInfo, err := directory.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened Darwin IPC parent directory: %w", err)
	}
	openedIdentity, err := darwinIdentity(openedInfo)
	if err != nil {
		return err
	}
	if !openedInfo.IsDir() || openedIdentity.uid != uint32(os.Geteuid()) ||
		openedIdentity.device != initialIdentity.device || openedIdentity.inode != initialIdentity.inode {
		return errors.New("Darwin IPC parent changed during secure setup")
	}
	if created {
		// Mkdir is affected by the process umask. Only repair the directory this
		// invocation created; an unsafe pre-existing directory is rejected above
		// so it is never trusted during a chmod repair window.
		if err := directory.Chmod(darwinParentMode); err != nil {
			return fmt.Errorf("set Darwin IPC parent mode: %w", err)
		}
	}
	finalInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("verify Darwin IPC parent directory: %w", err)
	}
	finalIdentity, err := darwinIdentity(finalInfo)
	if err != nil {
		return err
	}
	if !finalInfo.IsDir() || finalInfo.Mode()&os.ModeSymlink != 0 ||
		finalInfo.Mode().Perm() != darwinParentMode || finalInfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		finalIdentity.uid != uint32(os.Geteuid()) || finalIdentity.device != openedIdentity.device || finalIdentity.inode != openedIdentity.inode {
		return errors.New("Darwin IPC parent failed secure mode and identity verification")
	}
	return nil
}

func darwinServiceLockPath(socketPath string) string {
	return socketPath + ".lock"
}

func acquireDarwinServiceLock(socketPath string) (*darwinServiceLock, error) {
	path := darwinServiceLockPath(socketPath)
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(darwinSocketMode))
	if err != nil {
		return nil, fmt.Errorf("open Darwin IPC service lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open Darwin IPC service lock: invalid file descriptor")
	}
	closeWithError := func(cause error) (*darwinServiceLock, error) {
		return nil, errors.Join(cause, file.Close())
	}

	info, err := file.Stat()
	if err != nil {
		return closeWithError(fmt.Errorf("inspect Darwin IPC service lock: %w", err))
	}
	identity, err := validateDarwinServiceLock(info)
	if err != nil {
		return closeWithError(err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return closeWithError(errors.New("Darwin IPC service is already active"))
		}
		return closeWithError(fmt.Errorf("lock Darwin IPC service: %w", err))
	}

	pathInfo, err := os.Lstat(path)
	if err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeWithError(fmt.Errorf("verify Darwin IPC service lock path: %w", err))
	}
	pathIdentity, err := validateDarwinServiceLock(pathInfo)
	if err != nil || pathIdentity.device != identity.device || pathIdentity.inode != identity.inode {
		_ = unix.Flock(fd, unix.LOCK_UN)
		if err != nil {
			return closeWithError(err)
		}
		return closeWithError(errors.New("Darwin IPC service lock changed during acquisition"))
	}
	return &darwinServiceLock{file: file}, nil
}

func validateDarwinServiceLock(info os.FileInfo) (darwinFileIdentity, error) {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return darwinFileIdentity{}, errors.New("Darwin IPC service lock must be a regular file")
	}
	if info.Mode().Perm() != darwinSocketMode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return darwinFileIdentity{}, fmt.Errorf("Darwin IPC service lock mode must be %04o", darwinSocketMode)
	}
	identity, err := darwinIdentity(info)
	if err != nil {
		return darwinFileIdentity{}, err
	}
	if identity.uid != uint32(os.Geteuid()) {
		return darwinFileIdentity{}, errors.New("Darwin IPC service lock must be owned by the service effective UID")
	}
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Nlink != 1 {
		return darwinFileIdentity{}, errors.New("Darwin IPC service lock must have exactly one link")
	}
	return identity, nil
}

func (lock *darwinServiceLock) release() error {
	if lock == nil {
		return nil
	}
	lock.once.Do(func() {
		unlockErr := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
		closeErr := lock.file.Close()
		lock.err = errors.Join(unlockErr, closeErr)
	})
	return lock.err
}

func removeVerifiedStaleDarwinSocket(path string, ownerUID uint32) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing Darwin IPC path: %w", err)
	}
	identity, err := validateDarwinSocket(info, ownerUID)
	if err != nil {
		return err
	}

	connection, dialErr := net.DialTimeout("unix", path, darwinActiveProbeTime)
	if dialErr == nil {
		_ = connection.Close()
		return errors.New("Darwin IPC socket is already active")
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return fmt.Errorf("probe existing Darwin IPC socket: %w", dialErr)
	}
	if err := removeDarwinSocketIfSame(path, identity); err != nil {
		return fmt.Errorf("remove verified stale Darwin IPC socket: %w", err)
	}
	return nil
}

func validateDarwinSocket(info os.FileInfo, ownerUID uint32) (darwinFileIdentity, error) {
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return darwinFileIdentity{}, errors.New("Darwin IPC path exists and is not a socket")
	}
	if info.Mode().Perm() != darwinSocketMode {
		return darwinFileIdentity{}, fmt.Errorf("Darwin IPC socket mode must be %04o", darwinSocketMode)
	}
	identity, err := darwinIdentity(info)
	if err != nil {
		return darwinFileIdentity{}, err
	}
	if identity.uid != ownerUID {
		return darwinFileIdentity{}, errors.New("Darwin IPC socket owner does not match configured owner UID")
	}
	return identity, nil
}

func darwinIdentity(info os.FileInfo) (darwinFileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return darwinFileIdentity{}, errors.New("Darwin file metadata is unavailable")
	}
	return darwinFileIdentity{device: uint64(stat.Dev), inode: stat.Ino, uid: stat.Uid}, nil
}

func removeDarwinSocketIfSame(path string, expected darwinFileIdentity) error {
	return removeDarwinSocketIfSameWithRename(path, expected, renameDarwinPathExclusive)
}

type darwinExclusiveRename func(string, string) error

func removeDarwinSocketIfSameWithRename(path string, expected darwinFileIdentity, renameExclusive darwinExclusiveRename) error {
	if renameExclusive == nil {
		return errors.New("Darwin IPC cleanup rename is required")
	}
	quarantine := fmt.Sprintf("%s.cleanup-%d-%d", path, os.Getpid(), darwinCleanupSequence.Add(1))
	if err := renameExclusive(path, quarantine); err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("capture Darwin IPC socket for cleanup: %w", err)
	}

	info, err := os.Lstat(quarantine)
	if err != nil {
		return fmt.Errorf("inspect captured Darwin IPC socket: %w", err)
	}
	matches := info.Mode()&os.ModeSymlink == 0 && info.Mode()&os.ModeSocket != 0
	if matches {
		actual, identityErr := darwinIdentity(info)
		if identityErr != nil {
			return identityErr
		}
		matches = actual.device == expected.device && actual.inode == expected.inode
	}
	if matches {
		if err := os.Remove(quarantine); err != nil {
			return fmt.Errorf("remove captured Darwin IPC socket: %w", err)
		}
		return nil
	}
	if err := renameExclusive(quarantine, path); err != nil {
		return fmt.Errorf("restore replacement captured during Darwin IPC cleanup: %w", err)
	}
	return nil
}

func renameDarwinPathExclusive(from, to string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_EXCL)
}

// getpeereidUID implements Darwin getpeereid semantics using the x/sys/unix
// LOCAL_PEERCRED wrapper, avoiding cgo and authenticating from the kernel fd.
func getpeereidUID(connection *net.UnixConn) (uint64, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("access Darwin IPC socket descriptor: %w", err)
	}
	var credential *unix.Xucred
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credential, credentialErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("inspect Darwin IPC peer descriptor: %w", err)
	}
	if credentialErr != nil {
		return 0, fmt.Errorf("getpeereid Darwin IPC peer: %w", credentialErr)
	}
	return uidFromDarwinCredential(credential)
}

func uidFromDarwinCredential(credential *unix.Xucred) (uint64, error) {
	if credential == nil || credential.Version != darwinXucredVersion {
		return 0, errors.New("getpeereid returned invalid Darwin peer credentials")
	}
	return uint64(credential.Uid), nil
}

type darwinTimedConnection struct {
	*net.UnixConn
	handshakeTimeout time.Duration
	idleTimeout      time.Duration
	handshakeDone    chan struct{}
	eventSubscribed  chan struct{}
	activity         chan struct{}
	done             chan struct{}
	handshakeOnce    sync.Once
	closeOnce        sync.Once
	helloMu          sync.Mutex
	helloFrame       []byte
	helloFrameSize   int
	eventHello       bool
	subscribeOnce    sync.Once
	serverFrame      []byte
	serverFrameSize  int
	closeErr         error
}

func newDarwinTimedConnection(ctx context.Context, connection *net.UnixConn, handshakeTimeout, idleTimeout time.Duration) *darwinTimedConnection {
	timed := &darwinTimedConnection{
		UnixConn:         connection,
		handshakeTimeout: handshakeTimeout,
		idleTimeout:      idleTimeout,
		handshakeDone:    make(chan struct{}),
		eventSubscribed:  make(chan struct{}),
		activity:         make(chan struct{}, 1),
		done:             make(chan struct{}),
	}
	go timed.watch(ctx)
	return timed
}

func (connection *darwinTimedConnection) Read(payload []byte) (int, error) {
	read, err := connection.UnixConn.Read(payload)
	if read > 0 {
		connection.observeClientHello(payload[:read])
		connection.noteActivity()
	}
	return read, err
}

func (connection *darwinTimedConnection) Write(payload []byte) (int, error) {
	written, err := connection.UnixConn.Write(payload)
	if written > 0 {
		connection.observeServerHello(payload[:written])
		connection.noteActivity()
	}
	return written, err
}

func (connection *darwinTimedConnection) observeClientHello(payload []byte) {
	connection.helloMu.Lock()
	defer connection.helloMu.Unlock()
	if connection.helloFrameSize < 0 {
		return
	}
	remaining := int(MaxCommandFrameSize) + 4 - len(connection.helloFrame)
	if remaining <= 0 {
		connection.helloFrameSize = -1
		connection.helloFrame = nil
		return
	}
	if len(payload) > remaining {
		payload = payload[:remaining]
	}
	connection.helloFrame = append(connection.helloFrame, payload...)
	if connection.helloFrameSize == 0 && len(connection.helloFrame) >= 4 {
		length := binary.BigEndian.Uint32(connection.helloFrame[:4])
		if length == 0 || length > MaxCommandFrameSize {
			connection.helloFrameSize = -1
			connection.helloFrame = nil
			return
		}
		connection.helloFrameSize = int(length) + 4
	}
	if connection.helloFrameSize <= 0 || len(connection.helloFrame) < connection.helloFrameSize {
		return
	}
	var hello ClientHello
	payload = connection.helloFrame[4:connection.helloFrameSize]
	if decodeStrict(payload, &hello) == nil && hello.validate() == nil && hello.Channel == ChannelEvent {
		connection.eventHello = true
	}
	connection.helloFrameSize = -1
	connection.helloFrame = nil
}

func (connection *darwinTimedConnection) observeServerHello(payload []byte) {
	connection.helloMu.Lock()
	if connection.serverFrameSize < 0 {
		connection.helloMu.Unlock()
		return
	}
	remaining := int(MaxCommandFrameSize) + 4 - len(connection.serverFrame)
	if remaining <= 0 {
		connection.serverFrameSize = -1
		connection.serverFrame = nil
		connection.helloMu.Unlock()
		return
	}
	if len(payload) > remaining {
		payload = payload[:remaining]
	}
	connection.serverFrame = append(connection.serverFrame, payload...)
	if connection.serverFrameSize == 0 && len(connection.serverFrame) >= 4 {
		length := binary.BigEndian.Uint32(connection.serverFrame[:4])
		if length == 0 || length > MaxCommandFrameSize {
			connection.serverFrameSize = -1
			connection.serverFrame = nil
			connection.helloMu.Unlock()
			return
		}
		connection.serverFrameSize = int(length) + 4
	}
	if connection.serverFrameSize <= 0 || len(connection.serverFrame) < connection.serverFrameSize {
		connection.helloMu.Unlock()
		return
	}
	var hello ServerHello
	payload = connection.serverFrame[4:connection.serverFrameSize]
	valid := decodeStrict(payload, &hello) == nil && hello.Kind == "hello" && hello.Error == nil && hello.NegotiatedVersion == ProtocolVersion
	connection.serverFrameSize = -1
	connection.serverFrame = nil
	connection.helloMu.Unlock()
	if !valid {
		return
	}
	connection.handshakeOnce.Do(func() {
		close(connection.handshakeDone)
	})
}

// markEventSubscribed is called by the protocol server only after subscribe is
// validated and its acceptance response has been written. Event connections
// retain the ordinary idle timeout until this transition.
func (connection *darwinTimedConnection) markEventSubscribed() {
	connection.helloMu.Lock()
	event := connection.eventHello
	connection.helloMu.Unlock()
	if event {
		connection.subscribeOnce.Do(func() { close(connection.eventSubscribed) })
	}
}

func (connection *darwinTimedConnection) Close() error {
	connection.closeOnce.Do(func() {
		close(connection.done)
		connection.closeErr = connection.UnixConn.Close()
	})
	return connection.closeErr
}

func (connection *darwinTimedConnection) noteActivity() {
	select {
	case connection.activity <- struct{}{}:
	default:
	}
}

func (connection *darwinTimedConnection) watch(ctx context.Context) {
	handshakeTimer := time.NewTimer(connection.handshakeTimeout)
	select {
	case <-ctx.Done():
		stopTimer(handshakeTimer)
		_ = connection.Close()
		return
	case <-connection.done:
		stopTimer(handshakeTimer)
		return
	case <-connection.handshakeDone:
		stopTimer(handshakeTimer)
	case <-handshakeTimer.C:
		_ = connection.Close()
		return
	}

	select {
	case <-connection.eventSubscribed:
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-connection.done:
		}
		return
	default:
	}

	idleTimer := time.NewTimer(connection.idleTimeout)
	defer stopTimer(idleTimer)
	for {
		select {
		case <-ctx.Done():
			_ = connection.Close()
			return
		case <-connection.done:
			return
		case <-connection.eventSubscribed:
			stopTimer(idleTimer)
			select {
			case <-ctx.Done():
				_ = connection.Close()
			case <-connection.done:
			}
			return
		case <-connection.activity:
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(connection.idleTimeout)
		case <-idleTimer.C:
			_ = connection.Close()
			return
		}
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

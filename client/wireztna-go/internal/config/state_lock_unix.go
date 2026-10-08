//go:build !windows

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func acquireStateFileLock(path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(stateLockTimeout)
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() error {
				unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
				closeErr := file.Close()
				if unlockErr != nil {
					return unlockErr
				}
				return closeErr
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("timed out waiting for runtime state lock")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func acquireRemoteMutationFileLock(ctx context.Context, path string) (func() error, error) {
	file, err := openRegularPrivateFile(path, unix.O_CREAT|unix.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return lockRemoteMutationFile(ctx, file)
}

func acquireRemoteMutationFileLockForOwner(ctx context.Context, dir, name string, ownerUID uint32) (func() error, error) {
	dirFD, err := unix.Open(dir, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirFD)

	var dirStat unix.Stat_t
	if err := unix.Fstat(dirFD, &dirStat); err != nil {
		return nil, err
	}
	if dirStat.Mode&unix.S_IFMT != unix.S_IFDIR || dirStat.Uid != ownerUID || dirStat.Mode&0o077 != 0 {
		return nil, errors.New("remote mutation directory owner or permissions are unsafe")
	}

	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	fd, err := unix.Openat(dirFD, name, flags|unix.O_CREAT|unix.O_EXCL, 0600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(dirFD, name, flags, 0)
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("wrap remote mutation lock")
	}
	closeWithError := func(err error) (func() error, error) {
		_ = file.Close()
		return nil, err
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return closeWithError(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return closeWithError(errors.New("remote mutation lock must be a regular single-link file"))
	}
	if stat.Uid != ownerUID {
		currentUID := uint32(os.Geteuid())
		if !created || currentUID != 0 || stat.Uid != currentUID {
			return closeWithError(errors.New("remote mutation lock owner does not match configured owner"))
		}
		if err := unix.Fchown(fd, int(ownerUID), -1); err != nil {
			return closeWithError(fmt.Errorf("assign remote mutation lock owner: %w", err))
		}
	}
	if err := unix.Fchmod(fd, 0600); err != nil {
		return closeWithError(err)
	}
	return lockRemoteMutationFile(ctx, file)
}

func lockRemoteMutationFile(ctx context.Context, file *os.File) (func() error, error) {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() error {
				unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
				closeErr := file.Close()
				if unlockErr != nil {
					return unlockErr
				}
				return closeErr
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("waiting for remote mutation lock: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func replaceStateFile(source, destination string) error {
	return os.Rename(source, destination)
}

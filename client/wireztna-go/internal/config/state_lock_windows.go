//go:build windows

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func acquireStateFileLock(path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}

	handle := windows.Handle(file.Fd())
	var overlapped windows.Overlapped
	deadline := time.Now().Add(stateLockTimeout)
	for {
		err = windows.LockFileEx(
			handle,
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&overlapped,
		)
		if err == nil {
			return func() error {
				unlockErr := windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
				closeErr := file.Close()
				if unlockErr != nil {
					return unlockErr
				}
				return closeErr
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
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
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}

	handle := windows.Handle(file.Fd())
	var overlapped windows.Overlapped
	for {
		err = windows.LockFileEx(
			handle,
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&overlapped,
		)
		if err == nil {
			return func() error {
				unlockErr := windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
				closeErr := file.Close()
				if unlockErr != nil {
					return unlockErr
				}
				return closeErr
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
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

func acquireRemoteMutationFileLockForOwner(context.Context, string, string, uint32) (func() error, error) {
	return nil, errors.New("explicit Unix remote mutation ownership is unsupported on Windows")
}

func replaceStateFile(source, destination string) error {
	sourcePath, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPath, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(
		sourcePath,
		destinationPath,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}

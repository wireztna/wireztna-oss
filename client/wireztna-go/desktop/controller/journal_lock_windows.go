//go:build windows

package controller

import (
	"context"
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

type kernelFileLock struct {
	file       *os.File
	overlapped windows.Overlapped
	once       sync.Once
	err        error
}

func acquireKernelFileLock(ctx context.Context, path string) (*kernelFileLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	lock := &kernelFileLock{file: file}
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&lock.overlapped,
	)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errKernelLockBusy
		}
		return nil, err
	}
	return lock, nil
}

func (l *kernelFileLock) release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped); err != nil {
			l.err = err
		}
		if err := l.file.Close(); err != nil && l.err == nil {
			l.err = err
		}
	})
	return l.err
}

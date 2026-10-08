//go:build darwin || linux

package controller

import (
	"context"
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

type kernelFileLock struct {
	file *os.File
	once sync.Once
	err  error
}

func acquireKernelFileLock(ctx context.Context, path string) (*kernelFileLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errKernelLockBusy
		}
		return nil, err
	}
	return &kernelFileLock{file: file}, nil
}

func (l *kernelFileLock) release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN); err != nil {
			l.err = err
		}
		if err := l.file.Close(); err != nil && l.err == nil {
			l.err = err
		}
	})
	return l.err
}

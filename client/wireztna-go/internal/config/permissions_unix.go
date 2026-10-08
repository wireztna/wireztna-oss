//go:build !windows

package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openRegularPrivateFile(path string, flags int, perm uint32) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, perm)
	if err != nil {
		return nil, err
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("refuse non-regular private file %s", path)
	}

	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap private file %s", path)
	}
	return file, nil
}

func readPrivateSharedFile(path string) ([]byte, error) {
	file, err := openRegularPrivateFile(path, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func writePrivateSharedFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse non-regular private file %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	temporary, err := os.CreateTemp(dir, ".wireztna-private-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return syncPrivateDirectory(dir)
}

func syncPrivateDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func hardenSharedFilePermissions(path string) error {
	file, err := openRegularPrivateFile(path, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Chmod(0600)
}

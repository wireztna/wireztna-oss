//go:build windows

package config

import (
	"fmt"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func privateWindowsSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get current Windows user: %w", err)
	}

	// Protect the DACL from inheritance and grant full control only to the
	// account performing the sync, LocalSystem (service/helper), and local
	// administrators. Unlike os.Chmod, this enforces confidentiality on Windows.
	sddl := fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FA;;;SY)(A;;FA;;;BA)", user.User.Sid.String())
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("build Windows file DACL: %w", err)
	}
	return descriptor, nil
}

func windowsPath(path string) (*uint16, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode Windows file path: %w", err)
	}
	return pathPtr, nil
}

func validatePrivateWindowsHandle(handle windows.Handle, path string) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("inspect Windows private file %s: %w", path, err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return fmt.Errorf("refuse non-regular Windows private file %s", path)
	}
	if fileType, err := windows.GetFileType(handle); err != nil || fileType != windows.FILE_TYPE_DISK {
		if err != nil {
			return fmt.Errorf("inspect Windows private file type %s: %w", path, err)
		}
		return fmt.Errorf("refuse non-disk Windows private file %s", path)
	}
	return nil
}

func readPrivateSharedFile(path string) ([]byte, error) {
	descriptor, err := privateWindowsSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return nil, fmt.Errorf("read Windows file DACL: %w", err)
	}
	pathPtr, err := windowsPath(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	if err := validatePrivateWindowsHandle(handle, path); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("harden Windows private file before read: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("wrap private Windows file handle")
	}
	defer file.Close()
	return io.ReadAll(file)
}

func writePrivateSharedFile(path string, data []byte) error {
	descriptor, err := privateWindowsSecurityDescriptor()
	if err != nil {
		return err
	}
	pathPtr, err := windowsPath(path)
	if err != nil {
		return err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_WRITE,
		0,
		attributes,
		windows.CREATE_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fmt.Errorf("create private Windows file: %w", err)
	}
	if err := validatePrivateWindowsHandle(handle, path); err != nil {
		_ = windows.CloseHandle(handle)
		return err
	}

	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return fmt.Errorf("wrap private Windows file handle")
	}
	written, err := file.Write(data)
	if err != nil {
		_ = file.Close()
		return err
	}
	if written != len(data) {
		_ = file.Close()
		return fmt.Errorf("short write to private Windows file: wrote %d of %d bytes", written, len(data))
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncPrivateDirectory(_ string) error {
	// Windows FlushFileBuffers does not support directory handles. File writes
	// are flushed before close and MoveFile semantics are handled by the OS.
	return nil
}

func hardenSharedFilePermissions(path string) error {
	descriptor, err := privateWindowsSecurityDescriptor()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read Windows file DACL: %w", err)
	}
	pathPtr, err := windowsPath(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if err := validatePrivateWindowsHandle(handle, path); err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("apply Windows file DACL: %w", err)
	}
	return nil
}

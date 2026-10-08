//go:build windows

// Package elevation provides UAC elevation utilities for Windows.
package elevation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32          = syscall.NewLazyDLL("shell32.dll")
	shellExecuteExW  = shell32.NewProc("ShellExecuteExW")
)

// SHELLEXECUTEINFO structure
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	verb         *uint16
	file         *uint16
	parameters   *uint16
	directory    *uint16
	nShow        int32
	hInstApp     uintptr
	idList       uintptr
	class        *uint16
	hkeyClass    uintptr
	hotKey       uint32
	hIcon        uintptr
	hProcess     uintptr
}

const (
	seeMaskNoCloseProcess = 0x00000040
	swHide               = 0
	swNormal             = 1
	swShowMinNoActive    = 7
)

// IsElevated checks if the current process has administrator privileges.
func IsElevated() bool {
	var token windows.Token
	proc := windows.CurrentProcess()
	err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()

	var elevation struct {
		TokenIsElevated uint32
	}
	var size uint32
	err = windows.GetTokenInformation(token, windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &size)
	if err != nil {
		return false
	}
	return elevation.TokenIsElevated != 0
}

// RunElevated relaunches the current executable with the given arguments
// using ShellExecuteEx "runas" (triggers UAC prompt).
func RunElevated(args []string, hide bool) (uint32, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("cannot find executable: %w", err)
	}

	verb, _ := syscall.UTF16PtrFromString("runas")
	exeW, _ := syscall.UTF16PtrFromString(exe)
	argsW, _ := syscall.UTF16PtrFromString(strings.Join(args, " "))
	// Use the executable's directory as CWD so wintun.dll is found via SetDllDirectory
	exeDir := filepath.Dir(exe)
	cwdW, _ := syscall.UTF16PtrFromString(exeDir)

	show := int32(swNormal)
	if hide {
		show = swShowMinNoActive // Minimized, no focus — console programs need a window
	}

	info := &shellExecuteInfo{
		cbSize:     uint32(unsafe.Sizeof(shellExecuteInfo{})),
		fMask:      seeMaskNoCloseProcess,
		verb:       verb,
		file:       exeW,
		parameters: argsW,
		directory:  cwdW,
		nShow:      show,
	}

	ret, _, err := shellExecuteExW.Call(uintptr(unsafe.Pointer(info)))
	if ret == 0 {
		return 0, fmt.Errorf("ShellExecuteEx failed: %w", err)
	}

	// Get PID from process handle
	var pid uint32
	if info.hProcess != 0 {
		pid, _ = windows.GetProcessId(windows.Handle(info.hProcess))
		windows.CloseHandle(windows.Handle(info.hProcess))
	}

	return pid, nil
}

//go:build windows

package ipc

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

const pipePath = `\\.\pipe\wireztna`

// Listen creates a named pipe listener (server side — runs in service).
func Listen() (net.Listener, error) {
	cfg := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;BA)(A;;GA;;;SY)(A;;GRGW;;;BU)",
		// BA = Builtin Administrators (full access)
		// SY = Local System (full access)
		// BU = Builtin Users (read + write — allows unprivileged tray to connect)
	}
	return winio.ListenPipe(pipePath, cfg)
}

// Connect dials the named pipe (client side — runs in tray app).
func Connect() (net.Conn, error) {
	timeout := 5 * time.Second
	return winio.DialPipe(pipePath, &timeout)
}

//go:build !windows

package ipc

import (
	"net"
	"os"
	"time"
)

const sockPath = "/var/run/wireztna.sock"

// Listen creates a unix domain socket listener (server side — runs in service).
func Listen() (net.Listener, error) {
	// Remove stale socket file if it exists
	_ = os.Remove(sockPath)
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	// Allow all users to connect (tray runs unprivileged)
	_ = os.Chmod(sockPath, 0666)
	return listener, nil
}

// Connect dials the unix socket (client side — runs in tray app).
func Connect() (net.Conn, error) {
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

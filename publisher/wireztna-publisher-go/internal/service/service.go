package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	serviceName = "wireztna-publisher"
	unitPath    = "/etc/systemd/system/wireztna-publisher.service"
)

// unitTemplate is the systemd unit file for the publisher daemon
const unitTemplate = `[Unit]
Description=WireZTNA Publisher Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s start
Restart=on-failure
RestartSec=5
KillMode=process

# Security hardening
ProtectSystem=strict
ReadWritePaths=/etc/wireztna-publisher /proc/sys/net/ipv4
PrivateTmp=true
NoNewPrivileges=false
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW

[Install]
WantedBy=multi-user.target
`

// Install creates and enables the systemd service
func Install() error {
	binaryPath, err := getBinaryPath()
	if err != nil {
		return fmt.Errorf("failed to determine binary path: %w", err)
	}

	// Write unit file
	unit := fmt.Sprintf(unitTemplate, binaryPath)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("failed to write systemd unit file: %w", err)
	}

	// Reload systemd
	if err := systemctl("daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	// Enable service
	if err := systemctl("enable", serviceName); err != nil {
		return fmt.Errorf("failed to enable service: %w", err)
	}

	// Start service
	if err := systemctl("start", serviceName); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	return nil
}

// Uninstall stops, disables, and removes the systemd service
func Uninstall() error {
	// Stop service (ignore error if not running)
	_ = systemctl("stop", serviceName)

	// Disable service (ignore error if not enabled)
	_ = systemctl("disable", serviceName)

	// Remove unit file
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove unit file: %w", err)
	}

	// Reload systemd
	_ = systemctl("daemon-reload")

	return nil
}

// Start starts the systemd service
func Start() error {
	return systemctl("start", serviceName)
}

// Stop stops the systemd service
func Stop() error {
	return systemctl("stop", serviceName)
}

// Restart restarts the systemd service
func Restart() error {
	return systemctl("restart", serviceName)
}

// IsActive checks if the service is currently running
func IsActive() bool {
	err := systemctl("is-active", "--quiet", serviceName)
	return err == nil
}

// IsEnabled checks if the service is enabled at boot
func IsEnabled() bool {
	err := systemctl("is-enabled", "--quiet", serviceName)
	return err == nil
}

// HasSystemd checks if systemd is available on the system
func HasSystemd() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// systemctl runs a systemctl command
func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// getBinaryPath returns the absolute path to the current binary
func getBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}

	// Resolve symlinks
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}

	// Ensure the path is absolute
	if !strings.HasPrefix(exe, "/") {
		abs, err := filepath.Abs(exe)
		if err != nil {
			return "", err
		}
		exe = abs
	}

	return exe, nil
}

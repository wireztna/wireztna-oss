//go:build windows

// Package winsvc provides Windows Service Control Manager integration.
// This allows wireztna-service to run as a proper Windows service that
// starts at boot, respects stop/pause commands, and reports status.
package winsvc

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const ServiceName = "WireZTNA"
const ServiceDisplayName = "WireZTNA Network Access Service"
const ServiceDescription = "Manages WireGuard tunnel for Zero Trust Network Access"

// IsWindowsService reports whether the process is running as a Windows service.
func IsWindowsService() bool {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return isService
}

// RunAsService runs the given function within the Windows SCM framework.
// The function receives a stop channel that is closed when the service
// should shut down.
func RunAsService(serviceMain func(stop <-chan struct{})) error {
	elog, err := eventlog.Open(ServiceName)
	if err != nil {
		return fmt.Errorf("failed to open event log: %w", err)
	}
	defer elog.Close()

	elog.Info(1, fmt.Sprintf("%s service starting", ServiceName))

	err = svc.Run(ServiceName, &wireztnaService{
		main: serviceMain,
		elog: elog,
	})
	if err != nil {
		elog.Error(1, fmt.Sprintf("%s service failed: %v", ServiceName, err))
		return err
	}

	elog.Info(1, fmt.Sprintf("%s service stopped", ServiceName))
	return nil
}

// Install registers the service with the Windows SCM.
func Install() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find executable path: %w", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return err
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("cannot connect to service manager: %w", err)
	}
	defer m.Disconnect()

	// Check if already exists
	s, err := m.OpenService(ServiceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", ServiceName)
	}

	s, err = m.CreateService(ServiceName, exePath, mgr.Config{
		DisplayName:  ServiceDisplayName,
		Description:  ServiceDescription,
		StartType:    mgr.StartAutomatic,
		ServiceStartName: "LocalSystem",
	}, "service")
	if err != nil {
		return fmt.Errorf("cannot create service: %w", err)
	}
	defer s.Close()

	// Set recovery actions: restart on first two failures, then do nothing
	err = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.NoAction, Delay: 0},
	}, 86400) // Reset failure count after 24h
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not set recovery actions: %v\n", err)
	}

	// Install event log source
	err = eventlog.InstallAsEventCreate(ServiceName, eventlog.Error|eventlog.Warning|eventlog.Info)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install event log source: %v\n", err)
	}

	return nil
}

// Uninstall removes the service from the Windows SCM.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("cannot connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("service %s not found: %w", ServiceName, err)
	}
	defer s.Close()

	err = s.Delete()
	if err != nil {
		return fmt.Errorf("cannot delete service: %w", err)
	}

	_ = eventlog.Remove(ServiceName)
	return nil
}

// wireztnaService implements svc.Handler.
type wireztnaService struct {
	main func(stop <-chan struct{})
	elog *eventlog.Log
}

func (ws *wireztnaService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		ws.main(stop)
		close(done)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	for {
		select {
		case <-done:
			changes <- svc.Status{State: svc.StopPending}
			return
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				ws.elog.Info(1, "service stop requested")
				close(stop)
				changes <- svc.Status{State: svc.StopPending}
				<-done
				return
			default:
				ws.elog.Error(1, fmt.Sprintf("unexpected control request #%d", c))
			}
		}
	}
}

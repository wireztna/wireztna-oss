//go:build darwin

package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wireztna/client/desktop/controller"
	platformdarwin "github.com/wireztna/client/desktop/platform/darwin"
	v2 "github.com/wireztna/client/internal/ipc/v2"
	"github.com/wireztna/client/pkg/version"
)

const (
	desktopJournalCapacity     = 128
	desktopEventCapacity       = 256
	desktopIdempotencyCapacity = 1024
	desktopIdempotencyTTL      = 15 * time.Minute
	desktopShutdownTimeout     = 20 * time.Second
)

type desktopServiceOptions struct {
	ownerUID    uint64
	configDir   string
	journalPath string
	socketPath  string
}

type desktopServiceRuntime struct {
	controller *controller.Controller
	durable    *controller.DurableController
	lifecycle  *controller.Lifecycle
	server     *v2.Server
}

var serveDesktopDarwin = v2.ServeDarwin

func init() {
	rootCmd.AddCommand(newDesktopServiceCommand())
}

func newDesktopServiceCommand() *cobra.Command {
	options := desktopServiceOptions{}
	command := &cobra.Command{
		Use:   "desktop-service",
		Short: "Run the authenticated macOS desktop IPC v2 service",
		Long: `Runs the macOS arm64 desktop controller and authenticated IPC v2
transport for one explicit local user. This command does not expose legacy IPC
and does not provide a remote quit operation.`,
		Args: cobra.NoArgs,
		// Override root's legacy resolved-config pre-run. This service accepts only
		// the explicit owner/config pair validated by ResolveServicePaths.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOARCH != "arm64" {
				return errors.New("desktop-service pilot supports macOS arm64 only")
			}
			if os.Geteuid() != 0 {
				return errors.New("desktop-service must run as the root LaunchDaemon")
			}
			if !cmd.Flags().Changed("owner-uid") || options.ownerUID == 0 {
				return errors.New("--owner-uid must explicitly identify a non-root desktop user")
			}
			if !cmd.Flags().Changed("config-dir") || options.configDir == "" {
				return errors.New("--config-dir must be explicitly provided")
			}
			if options.socketPath != v2.DefaultDarwinSocketPath {
				return fmt.Errorf("--socket-path must be the authenticated IPC v2 endpoint %q", v2.DefaultDarwinSocketPath)
			}
			_, err := platformdarwin.ResolveServicePaths(options.ownerUID, options.configDir, options.journalPath)
			return err
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDesktopService(cmd.Context(), options)
		},
	}
	command.Flags().Uint64Var(&options.ownerUID, "owner-uid", 0, "UID of the sole authorized desktop user")
	command.Flags().StringVar(&options.configDir, "config-dir", "", "absolute owner-controlled directory containing config.yaml and token")
	command.Flags().StringVar(&options.journalPath, "journal-path", "", "applied journal path (default: private service state under /var/db)")
	command.Flags().StringVar(&options.socketPath, "socket-path", v2.DefaultDarwinSocketPath, "fixed authenticated desktop IPC v2 socket")
	return command
}

func runDesktopService(parent context.Context, options desktopServiceOptions) error {
	if parent == nil {
		return errors.New("desktop-service context is required")
	}
	serviceContext, stopSignals := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	// Signal cancellation stops transport and lifecycle inputs first. The
	// controller keeps an independent context until transient cleanup and lease
	// release have both completed.
	controllerContext, cancelController := context.WithCancel(context.WithoutCancel(parent))
	defer cancelController()
	runtimeService, err := buildDesktopService(controllerContext, options)
	if err != nil {
		return err
	}
	if startErr := runtimeService.lifecycle.Start(serviceContext); startErr != nil {
		lifecycleContext, cancelLifecycle := context.WithTimeout(context.Background(), desktopShutdownTimeout)
		lifecycleErr := runtimeService.lifecycle.Close(lifecycleContext)
		cancelLifecycle()
		if errors.Is(lifecycleErr, context.DeadlineExceeded) {
			// Even when Start loses a cancellation race, retain the controller
			// lease until the lifecycle proves recovered network state absent.
			lifecycleErr = errors.Join(lifecycleErr, runtimeService.lifecycle.Close(context.Background()))
		}
		closeContext, cancelClose := context.WithTimeout(context.Background(), desktopShutdownTimeout)
		closeErr := runtimeService.controller.Close(closeContext)
		cancelClose()
		return errors.Join(startErr, lifecycleErr, closeErr)
	}
	serveErr := serveDesktopDarwin(serviceContext, runtimeService.server, v2.DarwinTransportConfig{
		SocketPath: options.socketPath,
		OwnerUID:   options.ownerUID,
	})
	if errors.Is(serveErr, context.Canceled) && serviceContext.Err() != nil {
		serveErr = nil
	}
	lifecycleContext, cancelLifecycle := context.WithTimeout(context.Background(), desktopShutdownTimeout)
	lifecycleErr := runtimeService.lifecycle.Close(lifecycleContext)
	cancelLifecycle()
	if errors.Is(lifecycleErr, context.DeadlineExceeded) {
		// Close continues cleanup in the background. Do not release the
		// controller lease or exit while owned network state may still exist.
		lifecycleErr = errors.Join(lifecycleErr, runtimeService.lifecycle.Close(context.Background()))
	}
	closeContext, cancelClose := context.WithTimeout(context.Background(), desktopShutdownTimeout)
	closeErr := runtimeService.controller.Close(closeContext)
	cancelClose()
	return errors.Join(serveErr, lifecycleErr, closeErr)
}

func buildDesktopService(ctx context.Context, options desktopServiceOptions) (*desktopServiceRuntime, error) {
	paths, err := platformdarwin.ResolveServicePaths(options.ownerUID, options.configDir, options.journalPath)
	if err != nil {
		return nil, err
	}
	if err := platformdarwin.PrepareJournalPath(paths, options.ownerUID); err != nil {
		return nil, err
	}
	clientConfig, err := platformdarwin.LoadClientConfig(paths)
	if err != nil {
		return nil, err
	}
	owner := controller.OwnerID(fmt.Sprintf("uid:%d", options.ownerUID))
	tokenSource := platformdarwin.SecureTokenSource(paths.TokenFile, options.ownerUID)
	catalogProvider, err := platformdarwin.NewConnectionCatalogProvider(clientConfig, tokenSource)
	if err != nil {
		return nil, err
	}
	components, err := platformdarwin.NewComponents(owner, uint32(paths.OwnerUID), paths.ConfigDir, clientConfig, tokenSource)
	if err != nil {
		return nil, err
	}
	journal, err := controller.NewFileAppliedJournalStore(paths.JournalFile, desktopJournalCapacity)
	if err != nil {
		return nil, fmt.Errorf("create desktop applied journal: %w", err)
	}
	intentStore, err := controller.NewFileDurableIntentStore(paths.IntentFile)
	if err != nil {
		return nil, fmt.Errorf("create desktop durable intent store: %w", err)
	}
	backend, err := controller.NewController(ctx, controller.ControllerConfig{
		Owner:           owner,
		Sessions:        components.Sessions,
		Planner:         components.Planner,
		TruthGate:       components.TruthGate,
		WireGuard:       components.WireGuard,
		Routes:          components.Routes,
		DNS:             components.DNS,
		Journal:         journal,
		EventCapacity:   desktopEventCapacity,
		JournalCapacity: desktopJournalCapacity,
	})
	if err != nil {
		return nil, fmt.Errorf("create desktop controller: %w", err)
	}
	durable, err := controller.NewDurableController(ctx, backend, intentStore, owner, time.Now)
	if err != nil {
		closeContext, cancel := context.WithTimeout(context.Background(), desktopShutdownTimeout)
		defer cancel()
		return nil, errors.Join(fmt.Errorf("create desktop durable controller: %w", err), backend.Close(closeContext))
	}
	lifecycle, err := controller.NewLifecycle(controller.LifecycleConfig{
		Controller: backend,
		Durable:    durable,
		Observer:   components.NetworkObserver,
	})
	if err != nil {
		closeContext, cancel := context.WithTimeout(context.Background(), desktopShutdownTimeout)
		defer cancel()
		return nil, errors.Join(fmt.Errorf("create desktop lifecycle: %w", err), backend.Close(closeContext))
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			closeContext, cancel := context.WithTimeout(context.Background(), desktopShutdownTimeout)
			defer cancel()
			_ = backend.Close(closeContext)
		}
	}()

	expectedOwner := v2.NewExpectedUIDOwner(options.ownerUID)
	exactOwner := v2.ExactIdentityPolicy(expectedOwner)
	authorizer := v2.NewAuthorizer(v2.NewCommandRegistry(), map[v2.CommandClass]v2.AuthorizationPolicy{
		v2.CommandClassOwnerControl: exactOwner,
		v2.CommandClassRead:         exactOwner,
	})
	idempotency, err := v2.NewIdempotencyStore(desktopIdempotencyTTL, desktopIdempotencyCapacity)
	if err != nil {
		return nil, fmt.Errorf("create desktop idempotency store: %w", err)
	}
	streamIdentity, err := newDesktopStreamIdentity()
	if err != nil {
		return nil, err
	}
	server, err := v2.NewServer(durable, authorizer, idempotency, v2.ServerConfig{
		ServiceVersion:        version.Full(),
		ExpectedOwnerIdentity: expectedOwner,
		StreamIdentity:        streamIdentity,
		CatalogProvider:       catalogProvider,
	})
	if err != nil {
		return nil, fmt.Errorf("create desktop IPC v2 server: %w", err)
	}
	closeOnError = false
	return &desktopServiceRuntime{controller: backend, durable: durable, lifecycle: lifecycle, server: server}, nil
}

func newDesktopStreamIdentity() (v2.StreamIdentity, error) {
	var randomID [16]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return v2.StreamIdentity{}, fmt.Errorf("generate desktop stream identity: %w", err)
	}
	// Controller event sequences restart with each process. A fresh stream ID
	// prevents clients from reusing a cursor after restart; epoch 1 is local to it.
	return v2.StreamIdentity{StreamID: hex.EncodeToString(randomID[:]), Epoch: 1}, nil
}

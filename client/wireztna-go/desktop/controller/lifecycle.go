package controller

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	defaultLifecycleRenewBefore = 30 * time.Minute
	defaultLifecycleDebounce    = 1500 * time.Millisecond
	defaultLifecyclePoll        = 2 * time.Second
	defaultLifecycleRetry       = 30 * time.Second
)

type LifecycleConfig struct {
	Controller  *Controller
	Durable     *DurableController
	Observer    NetworkObserver
	Now         func() time.Time
	RenewBefore time.Duration
	Debounce    time.Duration
	Poll        time.Duration
	Retry       time.Duration
}

// Lifecycle supervises startup recovery, session deadlines, and coalesced
// wake/network signals. It never changes DurableIntent itself.
type Lifecycle struct {
	controller  *Controller
	durable     *DurableController
	observer    NetworkObserver
	now         func() time.Time
	renewBefore time.Duration
	debounce    time.Duration
	poll        time.Duration
	retry       time.Duration

	mu        sync.Mutex
	started   bool
	cancel    context.CancelFunc
	done      chan struct{}
	runErr    error
	closed    bool
	closeErr  error
	closeDone chan struct{}
	cleanup   func(context.Context) error
}

type lifecycleResult struct {
	kind CommandKind
	err  error
}

func NewLifecycle(config LifecycleConfig) (*Lifecycle, error) {
	if config.Controller == nil || config.Durable == nil || config.Observer == nil {
		return nil, &Error{Code: ErrorCodeInvalidArgument, Detail: "lifecycle dependencies are incomplete"}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.RenewBefore == 0 {
		config.RenewBefore = defaultLifecycleRenewBefore
	}
	if config.Debounce == 0 {
		config.Debounce = defaultLifecycleDebounce
	}
	if config.Poll == 0 {
		config.Poll = defaultLifecyclePoll
	}
	if config.Retry == 0 {
		config.Retry = defaultLifecycleRetry
	}
	if config.RenewBefore < 0 || config.Debounce < 0 || config.Poll <= 0 || config.Retry <= 0 {
		return nil, &Error{Code: ErrorCodeInvalidArgument, Detail: "lifecycle durations are invalid"}
	}
	lifecycle := &Lifecycle{
		controller: config.Controller, durable: config.Durable, observer: config.Observer,
		now: config.Now, renewBefore: config.RenewBefore, debounce: config.Debounce,
		poll: config.Poll, retry: config.Retry, closeDone: make(chan struct{}),
	}
	lifecycle.cleanup = lifecycle.cleanupBeforeShutdown
	return lifecycle, nil
}

// Start is non-blocking so the authenticated socket can become available while
// conservative startup reconciliation proceeds through the same controller.
func (l *Lifecycle) Start(parent context.Context) error {
	if parent == nil {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "lifecycle context is required"}
	}
	if err := parent.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return &Error{Code: ErrorCodeServiceUnavailable, Detail: "lifecycle is closed"}
	}
	if l.started {
		return &Error{Code: ErrorCodeConflict, Detail: "lifecycle is already started"}
	}
	ctx, cancel := context.WithCancel(parent)
	l.cancel = cancel
	l.done = make(chan struct{})
	l.started = true
	go l.run(ctx)
	return nil
}

func (l *Lifecycle) run(ctx context.Context) {
	defer close(l.done)
	startupSafe, startupErr := l.reconcileStartup(ctx)
	for !startupSafe {
		timer := time.NewTimer(l.retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			l.setRunErr(startupErr)
			return
		case <-timer.C:
			startupSafe, startupErr = l.reconcileStartup(ctx)
		}
	}
	l.durable.markReady()
	events, observerErr := l.observer.Events(ctx)
	if observerErr != nil {
		events = nil
	}

	poll := time.NewTicker(l.poll)
	defer poll.Stop()
	var debounceTimer, renewTimer, expiryTimer *time.Timer
	defer stopLifecycleTimer(debounceTimer)
	defer stopLifecycleTimer(renewTimer)
	defer stopLifecycleTimer(expiryTimer)
	results := make(chan lifecycleResult, 4)
	var pendingEvent, scheduledKind CommandKind
	var automaticRunning, expiryRunning bool
	var retryNotBefore, expiryRetryNotBefore time.Time

	refresh := func() {
		stopLifecycleTimer(renewTimer)
		stopLifecycleTimer(expiryTimer)
		renewTimer = nil
		expiryTimer = nil
		scheduledKind = ""
		intent, present, invalid := l.durable.Intent()
		if invalid || !present {
			return
		}
		snapshot, err := l.controller.Snapshot(ctx)
		if err != nil {
			return
		}
		now := l.now()
		if !intent.Desired.Connected {
			pendingEvent = ""
			stopLifecycleTimer(debounceTimer)
			debounceTimer = nil
			needsCleanup := snapshot.Applied.Connected || snapshot.State == ConnectionStateDegraded
			if needsCleanup && !expiryRunning {
				cleanupAt := now
				if expiryRetryNotBefore.After(cleanupAt) {
					cleanupAt = expiryRetryNotBefore
				}
				expiryTimer = time.NewTimer(nonNegativeDuration(cleanupAt.Sub(now)))
			}
			return
		}
		if !snapshot.Applied.Connected {
			if !automaticRunning && !expiryRunning && snapshot.ActiveOperation == nil {
				reconcileAt := now
				if retryNotBefore.After(reconcileAt) {
					reconcileAt = retryNotBefore
				}
				scheduledKind = CommandReconcile
				renewTimer = time.NewTimer(nonNegativeDuration(reconcileAt.Sub(now)))
			}
			return
		}
		if snapshot.Applied.ExpiresAt.IsZero() {
			return
		}
		expiresAt := snapshot.Applied.ExpiresAt
		if !expiryRunning {
			expiryAt := expiresAt
			if expiryRetryNotBefore.After(expiryAt) {
				expiryAt = expiryRetryNotBefore
			}
			expiryTimer = time.NewTimer(nonNegativeDuration(expiryAt.Sub(now)))
		}
		if automaticRunning || !expiresAt.After(now) {
			return
		}
		remaining := expiresAt.Sub(now)
		margin := l.renewBefore
		if margin >= remaining {
			margin = remaining / 4
		}
		renewAt := expiresAt.Add(-margin)
		if retryNotBefore.After(renewAt) {
			renewAt = retryNotBefore
		}
		if renewAt.Before(expiresAt) {
			scheduledKind = CommandRenew
			renewTimer = time.NewTimer(nonNegativeDuration(renewAt.Sub(now)))
		}
	}

	launch := func(kind CommandKind, force bool) bool {
		intent, present, invalid := l.durable.Intent()
		if invalid || !present || (!intent.Desired.Connected && kind != CommandDisconnect) {
			return false
		}
		expectedConnected := intent.Desired.Connected
		snapshot, err := l.controller.Snapshot(ctx)
		if err != nil {
			return false
		}
		if snapshot.ActiveOperation != nil && (!force || !expectedConnected) {
			return false
		}
		go func() {
			results <- lifecycleResult{kind: kind, err: l.durable.executeAutomatic(ctx, kind, expectedConnected)}
		}()
		return true
	}

	refresh()
	for {
		var debounceC, renewC, expiryC <-chan time.Time
		if debounceTimer != nil {
			debounceC = debounceTimer.C
		}
		if renewTimer != nil {
			renewC = renewTimer.C
		}
		if expiryTimer != nil {
			expiryC = expiryTimer.C
		}
		select {
		case <-ctx.Done():
			l.setRunErr(errors.Join(startupErr, observerErr))
			return
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event.Kind == NetworkEventWake {
				pendingEvent = CommandWake
			} else if pendingEvent == "" {
				pendingEvent = CommandNetworkChange
			}
			if debounceTimer == nil {
				debounceTimer = time.NewTimer(l.debounce)
			} else {
				resetLifecycleTimer(debounceTimer, l.debounce)
			}
		case <-debounceC:
			debounceTimer = nil
			if !automaticRunning && !expiryRunning && pendingEvent != "" && launch(pendingEvent, false) {
				automaticRunning = true
				pendingEvent = ""
			} else if pendingEvent != "" {
				debounceTimer = time.NewTimer(l.poll)
			}
			refresh()
		case <-renewC:
			renewTimer = nil
			kind := scheduledKind
			if kind == "" {
				kind = CommandRenew
			}
			if !automaticRunning && !expiryRunning && launch(kind, false) {
				automaticRunning = true
			} else {
				retryNotBefore = l.now().Add(l.retry)
			}
			refresh()
		case <-expiryC:
			expiryTimer = nil
			if !expiryRunning && launch(CommandDisconnect, true) {
				expiryRunning = true
			} else {
				expiryRetryNotBefore = l.now().Add(l.retry)
			}
			refresh()
		case result := <-results:
			if result.kind == CommandDisconnect {
				expiryRunning = false
				if result.err != nil {
					expiryRetryNotBefore = l.now().Add(l.retry)
				} else {
					expiryRetryNotBefore = time.Time{}
				}
			} else {
				automaticRunning = false
				if result.err != nil {
					retryNotBefore = l.now().Add(l.retry)
				} else {
					retryNotBefore = time.Time{}
					if result.kind == CommandReconcile {
						startupErr = nil
					}
				}
			}
			refresh()
		case <-l.durable.changed:
			retryNotBefore = time.Time{}
			intent, present, invalid := l.durable.Intent()
			if !invalid && present && !intent.Desired.Connected {
				expiryRetryNotBefore = l.now().Add(l.retry)
			} else {
				expiryRetryNotBefore = time.Time{}
			}
			refresh()
		case <-poll.C:
			refresh()
		}
	}
}

func (l *Lifecycle) reconcileStartup(ctx context.Context) (bool, error) {
	intent, present, invalid := l.durable.Intent()
	snapshot, err := l.controller.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	cleanupDesired := desiredFromApplied(snapshot.Applied)
	if cleanupDesired.ConfigurationID == "" {
		cleanupDesired = snapshot.Desired
	}
	if cleanupDesired.ConfigurationID == "" && present {
		cleanupDesired = intent.Desired
	}
	cleanupDesired.Connected = false
	needsCleanup := snapshot.Applied.Connected || snapshot.State == ConnectionStateDegraded
	if needsCleanup {
		if cleanupDesired.ConfigurationID == "" || cleanupDesired.Generation == 0 {
			return false, &Error{Code: ErrorCodeDegraded, Detail: "startup recovery lacks an owned configuration identity"}
		}
		if err := executeAndWait(ctx, l.controller, Command{Kind: CommandDisconnect, Desired: cleanupDesired}); err != nil {
			return false, err
		}
	}
	if invalid || !present {
		if err := l.durable.repairDisconnected(ctx, cleanupDesired); err != nil {
			return false, err
		}
		return true, nil
	}
	if !intent.Desired.Connected {
		return true, nil
	}
	reconcileErr := l.durable.executeAutomatic(ctx, CommandReconcile, true)
	if reconcileErr == nil {
		return true, nil
	}
	// Authentication or planning can fail before any mutation. Allow IPC to
	// repair credentials only when the post-failure view proves disconnected;
	// degraded/recovery state remains gated and is retried here.
	post, snapshotErr := l.controller.Snapshot(ctx)
	if snapshotErr != nil {
		return false, errors.Join(reconcileErr, snapshotErr)
	}
	return !post.Applied.Connected && post.State != ConnectionStateDegraded, reconcileErr
}

// Close first stops timers/observation, then performs a transient disconnected
// replacement while preserving durable intent. The caller closes Controller
// afterwards so its lifetime lease remains held throughout cleanup.
func (l *Lifecycle) Close(ctx context.Context) error {
	if ctx == nil {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "lifecycle close context is required"}
	}
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		started := l.started
		cancel := l.cancel
		done := l.done
		go l.completeClose(started, cancel, done)
	}
	done := l.closeDone
	l.mu.Unlock()
	select {
	case <-done:
		l.mu.Lock()
		err := l.closeErr
		l.mu.Unlock()
		return err
	case <-ctx.Done():
		// Cleanup deliberately continues in completeClose while Controller still
		// owns its lease. A later Close call can wait for the same result.
		return ctx.Err()
	}
}

func (l *Lifecycle) completeClose(started bool, cancel context.CancelFunc, done <-chan struct{}) {
	if cancel != nil {
		cancel()
	}
	if started && done != nil {
		<-done
	}
	for {
		if err := l.cleanupBeforeShutdownSafely(); err == nil {
			break
		}
		timer := time.NewTimer(l.retry)
		<-timer.C
	}
	l.mu.Lock()
	l.closeErr = l.runErr
	close(l.closeDone)
	l.mu.Unlock()
}

// cleanupBeforeShutdownSafely keeps the lifecycle's lease-owning close worker
// alive even if a platform adapter panics. A failed or panicking attempt is
// retried; closeDone is signalled only after an observation proves absence.
func (l *Lifecycle) cleanupBeforeShutdownSafely() (cleanupErr error) {
	defer func() {
		if recover() != nil {
			cleanupErr = &Error{Code: ErrorCodeDegraded, Detail: "shutdown cleanup panicked before proving network absence"}
		}
	}()
	if l.cleanup == nil {
		return &Error{Code: ErrorCodeDegraded, Detail: "shutdown cleanup verifier is unavailable"}
	}
	return l.cleanup(context.Background())
}

func (l *Lifecycle) cleanupBeforeShutdown(ctx context.Context) error {
	snapshot, err := l.controller.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !snapshot.Applied.Connected && snapshot.State != ConnectionStateDegraded {
		return nil
	}
	desired := desiredFromApplied(snapshot.Applied)
	if desired.ConfigurationID == "" {
		desired = snapshot.Desired
	}
	if desired.ConfigurationID == "" || desired.Generation == 0 {
		return &Error{Code: ErrorCodeDegraded, Detail: "shutdown cleanup lacks an owned configuration identity"}
	}
	desired.Connected = false
	return executeAndWait(ctx, l.controller, Command{Kind: CommandDisconnect, Desired: desired})
}

func executeAndWait(ctx context.Context, backend ControllerBackend, command Command) error {
	if ctx == nil {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "operation wait context is required"}
	}
	operationContext, cancel := context.WithCancel(ctx)
	defer cancel()
	snapshot, err := backend.Snapshot(operationContext)
	if err != nil {
		return err
	}
	events, err := backend.Subscribe(operationContext, snapshot.Sequence)
	if err != nil {
		return err
	}
	operationID, err := backend.Execute(operationContext, command)
	if err != nil {
		return err
	}
	return waitForOperation(operationContext, events, operationID)
}

func waitForOperation(ctx context.Context, events <-chan Event, operationID OperationID) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				return &Error{Code: ErrorCodeResyncRequired, Detail: "operation event stream closed before terminal result"}
			}
			if event.OperationID != operationID {
				continue
			}
			switch event.Kind {
			case EventOperationSucceeded:
				return nil
			case EventOperationFailed:
				if event.Error != nil {
					return event.Error
				}
				return &Error{Code: ErrorCodeServiceUnavailable, Detail: "operation failed without structured error"}
			}
		}
	}
}

func (l *Lifecycle) setRunErr(err error) {
	l.mu.Lock()
	l.runErr = err
	l.mu.Unlock()
}

func nonNegativeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}

func stopLifecycleTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func resetLifecycleTimer(timer *time.Timer, duration time.Duration) {
	stopLifecycleTimer(timer)
	timer.Reset(nonNegativeDuration(duration))
}

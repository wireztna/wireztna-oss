package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	defaultEventCapacity   = 256
	defaultJournalCapacity = 128
	defaultRollbackTimeout = 15 * time.Second
)

// ControllerConfig supplies every platform and policy dependency. It contains
// no transport, cmd, or service dependency.
type ControllerConfig struct {
	Owner           OwnerID
	Sessions        SessionProvider
	Planner         Planner
	TruthGate       TruthGate
	WireGuard       WireGuardDevice
	Routes          RouteManager
	DNS             DNSManager
	Journal         AppliedJournalStore
	OperationIDs    OperationIDGenerator
	Now             func() time.Time
	EventCapacity   int
	JournalCapacity int
	RollbackTimeout time.Duration
}

// Controller serializes desktop intents and owns their event lifecycle.
type Controller struct {
	owner     OwnerID
	sessions  SessionProvider
	planner   Planner
	truthGate TruthGate
	wireGuard WireGuardDevice
	routes    RouteManager
	dns       DNSManager
	journal   AppliedJournalStore
	lease     AppliedJournalLease
	ids       OperationIDGenerator
	now       func() time.Time

	eventLog        *eventLog
	journalLimit    int
	rollbackTimeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	notify chan struct{}
	done   chan struct{}

	mu           sync.Mutex
	closed       bool
	closeErr     error
	queue        []*operationRequest
	active       *operationRequest
	activeCancel context.CancelFunc
	snapshot     Snapshot

	journalMu        sync.Mutex
	journalState     AppliedJournal
	journalUncertain bool
}

type operationRequest struct {
	operation Operation
	previous  AppliedState
	terminal  bool
}

type operationOutcome struct {
	applied AppliedState
	truth   Truth
}

type appliedUndo struct {
	step JournalStep
	undo Undo
}

// NewController acquires exclusive lifetime ownership before reading durable
// state. Recovered connected or incomplete state is reported as degraded until
// an explicit complete replacement passes the truth gate.
func NewController(ctx context.Context, config ControllerConfig) (_ *Controller, resultErr error) {
	if ctx == nil {
		return nil, &Error{Code: ErrorCodeInvalidArgument, Detail: "controller context is required"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateControllerConfig(&config); err != nil {
		return nil, err
	}

	lease, err := safeAcquireLease(ctx, config.Journal, config.Owner)
	if err != nil {
		return nil, err
	}
	leaseTransferred := false
	defer func() {
		if recovered := recover(); recovered != nil {
			resultErr = &Error{Code: ErrorCodeServiceUnavailable, Detail: "controller initialization panicked"}
		}
		if !leaseTransferred {
			_ = safeReleaseLease(lease)
		}
	}()

	journal, err := safeLoadJournal(ctx, config.Journal, config.Owner)
	if err != nil {
		return nil, err
	}
	if journal.Revision == 0 {
		journal = AppliedJournal{Owner: config.Owner}
	} else if err := journal.validate(config.Owner, config.JournalCapacity); err != nil {
		return nil, &Error{Code: ErrorCodeConflict, Detail: "applied journal is invalid; conservative recovery is required"}
	}
	snapshot := recoveredSnapshot(journal)
	if err := snapshot.Validate(); err != nil {
		return nil, &Error{Code: ErrorCodeConflict, Detail: "recovered controller state is invalid; conservative recovery is required"}
	}

	controllerContext, cancel := context.WithCancel(ctx)
	controller := &Controller{
		owner:           config.Owner,
		sessions:        config.Sessions,
		planner:         config.Planner,
		truthGate:       config.TruthGate,
		wireGuard:       config.WireGuard,
		routes:          config.Routes,
		dns:             config.DNS,
		journal:         config.Journal,
		lease:           lease,
		ids:             config.OperationIDs,
		now:             config.Now,
		eventLog:        newEventLog(config.EventCapacity),
		journalLimit:    config.JournalCapacity,
		rollbackTimeout: config.RollbackTimeout,
		ctx:             controllerContext,
		cancel:          cancel,
		notify:          make(chan struct{}, 1),
		done:            make(chan struct{}),
		snapshot:        snapshot,
		journalState:    journal,
	}
	leaseTransferred = true
	go controller.processLoop()
	return controller, nil
}

// Execute accepts an intent and returns its correlation ID. Execution is
// asynchronous. Every accepted operation emits exactly one succeeded or failed
// event. While durable recovery evidence exists, only disconnect and explicit
// complete-replacement reconcile intents are admitted.
func (c *Controller) Execute(ctx context.Context, command Command) (OperationID, error) {
	if ctx == nil {
		return "", &Error{Code: ErrorCodeInvalidArgument, Detail: "execute context is required"}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !command.Kind.valid() {
		return "", &Error{Code: ErrorCodeInvalidArgument, Detail: "unknown controller command"}
	}
	operationID, err := safeOperationID(c.ids)
	if err != nil || operationID == "" {
		return "", &Error{Code: ErrorCodeServiceUnavailable, Detail: "operation ID is unavailable"}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", &Error{Code: ErrorCodeServiceUnavailable, Detail: "controller is closed"}
	}
	command = c.normalizeCommandLocked(command)
	recoveryRequired := c.recoveryRequired()
	pristineDisconnect := command.Kind == CommandDisconnect &&
		command.Desired == (DesiredState{}) &&
		c.snapshot.State == ConnectionStateDisconnected &&
		c.snapshot.Desired == (DesiredState{}) &&
		c.snapshot.Applied == (AppliedState{}) &&
		!recoveryRequired
	if !pristineDisconnect {
		if err := validateCommand(command); err != nil {
			return "", err
		}
	}
	if recoveryRequired && !command.Kind.allowedDuringRecovery() {
		return "", recoveryGateError()
	}

	request := &operationRequest{operation: Operation{
		ID:        operationID,
		Command:   command,
		StartedAt: c.timestamp(),
	}}
	c.publishLocked(Event{
		Kind:        EventOperationStarted,
		OperationID: operationID,
		State:       c.snapshot.State,
	})

	if command.Kind == CommandDisconnect {
		if c.activeCancel != nil {
			c.activeCancel()
		}
		for _, queued := range c.queue {
			c.finishLocked(queued, operationOutcome{}, &Error{
				Code:   ErrorCodeConflict,
				Detail: "operation superseded by explicit disconnect",
			})
		}
		c.queue = nil
		if pristineDisconnect {
			// No revision exists to plan against and there is no recovery evidence.
			// Preserve full operation/event semantics without inventing a catalog
			// identity or consulting authentication for an already-absent network.
			c.snapshot.Desired = DesiredState{}
			c.snapshot.Health = unknownHealth()
			c.snapshot.State = ConnectionStateDisconnected
			c.publishSnapshotLocked()
			c.publishLocked(Event{
				Kind:        EventOperationSucceeded,
				OperationID: operationID,
				State:       c.snapshot.State,
			})
			return operationID, nil
		}
		c.queue = []*operationRequest{request}
	} else {
		c.queue = append(c.queue, request)
	}
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return operationID, nil
}

// hasPendingCommand observes both the active request and the queue. Callers that
// also serialize through DurableController.executeMu can use it to avoid
// superseding an explicit command during the queued-to-active transition.
func (c *Controller) hasPendingCommand(kind CommandKind) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil && c.active.operation.Command.Kind == kind {
		return true
	}
	for _, queued := range c.queue {
		if queued != nil && queued.operation.Command.Kind == kind {
			return true
		}
	}
	return false
}

// Snapshot returns a defensive copy of the current controller view.
func (c *Controller) Snapshot(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, &Error{Code: ErrorCodeInvalidArgument, Detail: "snapshot context is required"}
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := cloneSnapshot(c.snapshot)
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, &Error{Code: ErrorCodeDegraded, Detail: "controller snapshot invariant failed"}
	}
	return snapshot, nil
}

// Subscribe replays events after the supplied cursor and then streams live
// events. RESYNC_REQUIRED means the bounded ring no longer contains the cursor.
// Slow subscribers are closed rather than blocking the processor.
func (c *Controller) Subscribe(ctx context.Context, after Sequence) (<-chan Event, error) {
	if ctx == nil {
		return nil, &Error{Code: ErrorCodeInvalidArgument, Detail: "subscription context is required"}
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, &Error{Code: ErrorCodeServiceUnavailable, Detail: "controller is closed"}
	}
	channel, err := c.eventLog.subscribe(ctx, after)
	c.mu.Unlock()
	return channel, err
}

// Close stops accepting commands, terminally fails queued commands, cancels the
// active operation, and waits for rollback and lease release. If ctx expires,
// shutdown continues and the lifetime lease remains held until all side effects
// have actually returned; callers may invoke Close again to wait.
func (c *Controller) Close(ctx context.Context) error {
	if ctx == nil {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "close context is required"}
	}
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		for _, queued := range c.queue {
			c.finishLocked(queued, operationOutcome{}, &Error{
				Code:   ErrorCodeServiceUnavailable,
				Detail: "controller closed before operation execution",
			})
		}
		c.queue = nil
		if c.activeCancel != nil {
			c.activeCancel()
		}
		c.cancel()
	}
	c.mu.Unlock()

	select {
	case <-c.done:
		c.mu.Lock()
		err := c.closeErr
		c.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Controller) processLoop() {
	defer close(c.done)
	defer func() {
		if err := safeReleaseLease(c.lease); err != nil {
			c.mu.Lock()
			c.closeErr = &Error{Code: ErrorCodeDegraded, Detail: "controller lease release failed"}
			c.mu.Unlock()
		}
	}()
	defer c.eventLog.close()
	defer func() {
		if recover() != nil {
			c.markRecoveryRequired()
			c.failProcessorRequests()
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			c.failQueuedOnShutdown()
			return
		case <-c.notify:
		}

		for {
			request, operationContext := c.nextOperation()
			if request == nil {
				break
			}
			c.runOperation(operationContext, request)
		}
	}
}

func (c *Controller) runOperation(ctx context.Context, request *operationRequest) {
	outcome, err := c.processOperation(ctx, request)
	c.mu.Lock()
	c.finishLocked(request, outcome, err)
	c.active = nil
	c.activeCancel = nil
	c.snapshot.ActiveOperation = nil
	c.publishSnapshotLocked()
	c.mu.Unlock()
}

func (c *Controller) nextOperation() (*operationRequest, context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) == 0 {
		return nil, nil
	}
	request := c.queue[0]
	c.queue = c.queue[1:]
	request.previous = c.snapshot.Applied
	operationContext, cancel := context.WithCancel(c.ctx)
	c.active = request
	c.activeCancel = cancel
	operation := request.operation
	c.snapshot.ActiveOperation = &operation
	c.snapshot.Desired = request.operation.Command.Desired
	c.snapshot.Health = unknownHealth()
	switch request.operation.Command.Kind {
	case CommandDisconnect:
		if c.snapshot.Applied.Connected {
			c.snapshot.State = ConnectionStateDegraded
		} else {
			c.snapshot.State = ConnectionStateDisconnected
		}
	case CommandRenew, CommandWake, CommandNetworkChange, CommandSwitch:
		c.snapshot.State = ConnectionStateReconnecting
	case CommandReconcile:
		if !request.operation.Command.Desired.Connected && !c.snapshot.Applied.Connected {
			c.snapshot.State = ConnectionStateDisconnected
		} else if c.snapshot.Applied.Connected {
			c.snapshot.State = ConnectionStateReconnecting
		} else {
			c.snapshot.State = ConnectionStateConnecting
		}
	default:
		c.snapshot.State = ConnectionStateConnecting
	}
	c.publishSnapshotLocked()
	return request, operationContext
}

func (c *Controller) processOperation(ctx context.Context, request *operationRequest) (outcome operationOutcome, resultErr error) {
	undos := make([]appliedUndo, 0, 3)
	transactionStarted := false
	transactionCommitted := false
	ambiguousApply := false
	recoveryOperation := false
	defer func() {
		if recover() == nil {
			return
		}
		if transactionCommitted {
			resultErr = nil
			return
		}
		panicErr := &Error{Code: ErrorCodeServiceUnavailable, Detail: "controller dependency panicked"}
		if transactionStarted {
			resultErr = c.rollbackOperation(request.operation.Command.Desired.Connected, undos, panicErr, ambiguousApply, recoveryOperation)
		} else {
			if recoveryOperation {
				c.markRecoveryRequired()
			}
			resultErr = panicErr
		}
	}()

	var err error
	recoveryOperation, err = c.prepareOperationJournal(ctx, request.operation.Command.Kind)
	if err != nil {
		return outcome, err
	}
	desired := request.operation.Command.Desired
	var session Session
	if desired.Connected {
		c.progress(request, ProgressStageAuthenticating)
		session, err = safeValueCall(func() (Session, error) {
			return c.sessions.Acquire(ctx, c.owner, desired)
		})
		if err != nil {
			return outcome, dependencyError(err, "session acquisition failed")
		}
		validSession, validationErr := safeValueCall(func() (bool, error) {
			return session != nil && session.Owner() == c.owner && session.Generation() == desired.Generation &&
				session.ExpiresAt().After(c.timestamp()), nil
		})
		if validationErr != nil || !validSession {
			return outcome, &Error{Code: ErrorCodeConflict, Detail: "session owner or generation mismatch"}
		}
		c.progress(request, ProgressStageRequestingAccess)
	}
	if err := ctx.Err(); err != nil {
		return outcome, err
	}
	plan, err := safeValueCall(func() (Plan, error) {
		return c.planner.Plan(ctx, c.owner, desired, session)
	})
	if err != nil {
		return outcome, dependencyError(err, "planning failed")
	}
	if err := plan.Validate(desired); err != nil {
		return outcome, &Error{Code: ErrorCodeServiceUnavailable, Detail: "planner returned an inconsistent plan"}
	}
	if desired.Connected && (session == nil || plan.Applied.ExpiresAt.IsZero() || !plan.Applied.ExpiresAt.Equal(session.ExpiresAt())) {
		return outcome, &Error{Code: ErrorCodeServiceUnavailable, Detail: "planner returned an inconsistent session deadline"}
	}
	if err := c.beginJournal(request.previous, plan.Applied, recoveryOperation); err != nil {
		return outcome, err
	}
	transactionStarted = true

	apply := func(step JournalStep, progress ProgressStage, mutation func() (Undo, error)) error {
		c.progress(request, progress)
		if err := c.checkpoint(step, JournalEntryPrepared); err != nil {
			return err
		}
		ambiguousApply = true
		undo, mutationErr := safeValueCall(mutation)
		if undo.Owner() == c.owner {
			undos = append(undos, appliedUndo{step: step, undo: undo})
			ambiguousApply = false
		}
		if mutationErr != nil {
			return dependencyError(mutationErr, "platform apply failed")
		}
		if undo.Owner() != c.owner {
			return &Error{Code: ErrorCodeServiceUnavailable, Detail: "platform apply returned an invalid undo token"}
		}
		if err := c.checkpoint(step, JournalEntryApplied); err != nil {
			return err
		}
		return ctx.Err()
	}

	if err := apply(JournalStepWireGuard, ProgressStageConfiguringWireGuard, func() (Undo, error) {
		return c.wireGuard.Apply(ctx, c.owner, plan.WireGuard)
	}); err != nil {
		return outcome, c.rollbackOperation(request.operation.Command.Desired.Connected, undos, err, ambiguousApply, recoveryOperation)
	}
	if err := apply(JournalStepRoutes, ProgressStageConfiguringRoutes, func() (Undo, error) {
		return c.routes.Apply(ctx, c.owner, plan.Routes)
	}); err != nil {
		return outcome, c.rollbackOperation(request.operation.Command.Desired.Connected, undos, err, ambiguousApply, recoveryOperation)
	}
	if err := apply(JournalStepDNS, ProgressStageConfiguringDNS, func() (Undo, error) {
		return c.dns.Apply(ctx, c.owner, plan.DNS)
	}); err != nil {
		return outcome, c.rollbackOperation(request.operation.Command.Desired.Connected, undos, err, ambiguousApply, recoveryOperation)
	}

	c.progress(request, ProgressStageVerifyingConnection)
	if err := c.setJournalStatus(JournalStatusVerifying); err != nil {
		return outcome, c.rollbackOperation(request.operation.Command.Desired.Connected, undos, err, false, recoveryOperation)
	}
	observation, err := c.observeHealth(ctx)
	if err != nil {
		return outcome, c.rollbackOperation(request.operation.Command.Desired.Connected, undos, err, false, recoveryOperation)
	}
	truth, err := safeValueCall(func() (Truth, error) {
		return c.truthGate.Verify(ctx, c.owner, desired, plan.Applied, observation)
	})
	if err != nil {
		return outcome, c.rollbackOperation(request.operation.Command.Desired.Connected, undos, dependencyError(err, "truth verification failed"), false, recoveryOperation)
	}
	if err := truth.Validate(plan.Applied.Connected); err != nil {
		detail := fmt.Sprintf(
			"truth gate rejected the applied connection (wireguard=%s routes=%s dns=%s end_to_end=%s)",
			truth.Health.WireGuard, truth.Health.Routes, truth.Health.DNS, truth.Health.EndToEnd,
		)
		return outcome, c.rollbackOperation(request.operation.Command.Desired.Connected, undos, &Error{Code: ErrorCodeDegraded, Detail: detail}, false, recoveryOperation)
	}
	if desired.Connected && (session == nil || !session.ExpiresAt().After(c.timestamp())) {
		return outcome, c.rollbackOperation(request.operation.Command.Desired.Connected, undos, &Error{Code: ErrorCodeConflict, Detail: "session expired before connection commit"}, false, recoveryOperation)
	}
	if err := c.commitJournal(plan.Applied); err != nil {
		return outcome, c.rollbackOperation(request.operation.Command.Desired.Connected, undos, err, false, recoveryOperation)
	}
	transactionCommitted = true
	outcome = operationOutcome{applied: plan.Applied, truth: truth}
	if plan.Applied.Connected {
		c.progress(request, ProgressStageConnected)
	}
	return outcome, nil
}

func (c *Controller) observeHealth(ctx context.Context) (HealthObservation, error) {
	wireGuard, err := safeValueCall(func() (WireGuardHealth, error) {
		return c.wireGuard.Health(ctx, c.owner)
	})
	if err != nil {
		return HealthObservation{}, dependencyError(err, "WireGuard health check failed")
	}
	routes, err := safeValueCall(func() (RouteHealth, error) {
		return c.routes.Health(ctx, c.owner)
	})
	if err != nil {
		return HealthObservation{}, dependencyError(err, "route health check failed")
	}
	dns, err := safeValueCall(func() (DNSHealth, error) {
		return c.dns.Health(ctx, c.owner)
	})
	if err != nil {
		return HealthObservation{}, dependencyError(err, "DNS health check failed")
	}
	return HealthObservation{WireGuard: wireGuard, Routes: routes, DNS: dns}, nil
}

func (c *Controller) rollbackOperation(connected bool, undos []appliedUndo, cause error, ambiguousApply bool, forceRecovery bool) error {
	if connected {
		return c.rollback(undos, cause, ambiguousApply, forceRecovery)
	}
	// Disconnect/cleanup is intentionally one-way. Replaying Undo from a
	// previously connected state could resurrect an expired or explicitly
	// revoked tunnel. Preserve recovery evidence and let lifecycle retry removal.
	c.markRecoveryRequired()
	return &Error{Code: ErrorCodeDegraded, Detail: "disconnect cleanup did not establish absence; conservative retry is required"}
}

// rollback always traverses successfully acquired Undo values in reverse order.
// Any ambiguity, journal failure, panic, or pre-existing recovery evidence keeps
// the journal recovery-required even when the new replacement was reverted.
func (c *Controller) rollback(undos []appliedUndo, cause error, ambiguousApply bool, forceRecovery bool) (result error) {
	defer func() {
		if recover() != nil {
			c.markRecoveryRequired()
			result = &Error{Code: ErrorCodeDegraded, Detail: "rollback panicked; conservative recovery is required"}
		}
	}()
	rollbackContext, cancel := context.WithTimeout(context.Background(), c.rollbackTimeout)
	defer cancel()
	journalFailed := c.setJournalStatusContext(rollbackContext, JournalStatusRollingBack) != nil
	rollbackFailed := ambiguousApply
	for index := len(undos) - 1; index >= 0; index-- {
		item := undos[index]
		if err := c.checkpointContext(rollbackContext, item.step, JournalEntryRollbackStarted); err != nil {
			journalFailed = true
		}
		revertErr := safeErrorCall(func() error { return item.undo.Revert(rollbackContext) })
		if revertErr != nil {
			rollbackFailed = true
			if err := c.checkpointContext(rollbackContext, item.step, JournalEntryRollbackFailed); err != nil {
				journalFailed = true
			}
		} else if err := c.checkpointContext(rollbackContext, item.step, JournalEntryRollbackComplete); err != nil {
			journalFailed = true
		}
	}
	if forceRecovery || rollbackFailed || journalFailed {
		c.markRecoveryRequiredContext(rollbackContext)
		return &Error{Code: ErrorCodeDegraded, Detail: "rollback could not establish clean truth; conservative recovery is required"}
	}
	if err := c.setJournalStatusContext(rollbackContext, JournalStatusRolledBack); err != nil {
		c.markRecoveryRequiredContext(rollbackContext)
		return &Error{Code: ErrorCodeDegraded, Detail: "rollback completed but could not be durably recorded; conservative recovery is required"}
	}
	return canonicalError(cause)
}

func (c *Controller) prepareOperationJournal(ctx context.Context, kind CommandKind) (bool, error) {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if c.journalUncertain {
		journal, err := safeLoadJournal(ctx, c.journal, c.owner)
		if err != nil {
			return true, recoveryGateError()
		}
		if journal.Revision == 0 {
			journal = AppliedJournal{Owner: c.owner}
		} else if err := journal.validate(c.owner, c.journalLimit); err != nil {
			return true, recoveryGateError()
		}
		c.journalState = journal
		c.journalUncertain = false
	}
	recovery := c.journalState.Revision != 0 && c.journalState.Status.requiresRecovery()
	if recovery && !kind.allowedDuringRecovery() {
		return true, recoveryGateError()
	}
	return recovery, nil
}

func (c *Controller) recoveryRequired() bool {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	return c.journalUncertain ||
		(c.journalState.Revision != 0 && c.journalState.Status.requiresRecovery())
}

func (c *Controller) beginJournal(stable AppliedState, target AppliedState, recovering bool) error {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if c.journalUncertain {
		return recoveryGateError()
	}
	if c.journalState.Revision != 0 && c.journalState.Status.requiresRecovery() && !recovering {
		return recoveryGateError()
	}
	next := AppliedJournal{
		SchemaVersion: appliedJournalSchemaVersion,
		Owner:         c.owner,
		Generation:    target.Generation,
		Revision:      c.journalState.Revision + 1,
		Status:        JournalStatusApplying,
		Stable:        stable,
		Target:        target,
	}
	if recovering {
		// Preserve prior checkpoints while the complete replacement is in
		// flight. Only a successful truth gate and commit may supersede them.
		next.Stable = c.journalState.Stable
		next.Entries = append([]JournalEntry(nil), c.journalState.Entries...)
	}
	return c.swapJournalContextLocked(c.ctx, next)
}

func (c *Controller) checkpoint(step JournalStep, status JournalEntryStatus) error {
	return c.checkpointContext(c.ctx, step, status)
}

func (c *Controller) checkpointContext(ctx context.Context, step JournalStep, status JournalEntryStatus) error {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if c.journalUncertain || c.journalState.Revision == 0 {
		return &Error{Code: ErrorCodeConflict, Detail: "applied journal transaction is not initialized"}
	}
	next := c.journalState
	next.Revision++
	next.Entries = append(append([]JournalEntry(nil), c.journalState.Entries...), JournalEntry{
		Step:       step,
		Status:     status,
		OccurredAt: c.timestamp(),
	})
	if len(next.Entries) > c.journalLimit {
		next.Entries = append([]JournalEntry(nil), next.Entries[len(next.Entries)-c.journalLimit:]...)
	}
	return c.swapJournalContextLocked(ctx, next)
}

func (c *Controller) setJournalStatus(status JournalStatus) error {
	return c.setJournalStatusContext(c.ctx, status)
}

func (c *Controller) setJournalStatusContext(ctx context.Context, status JournalStatus) error {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if c.journalUncertain || c.journalState.Revision == 0 {
		return &Error{Code: ErrorCodeConflict, Detail: "applied journal transaction is not initialized"}
	}
	next := c.journalState
	next.Revision++
	next.Status = status
	return c.swapJournalContextLocked(ctx, next)
}

func (c *Controller) commitJournal(applied AppliedState) error {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if c.journalUncertain || c.journalState.Revision == 0 {
		return recoveryGateError()
	}
	next := c.journalState
	next.Revision++
	next.Status = JournalStatusCommitted
	next.Stable = applied
	next.Target = applied
	next.Generation = applied.Generation
	return c.swapJournalContextLocked(c.ctx, next)
}

func (c *Controller) swapJournalContextLocked(ctx context.Context, next AppliedJournal) error {
	expected := c.journalState.Cursor()
	if expected.Owner == "" {
		expected.Owner = c.owner
	}
	if err := safeCompareAndSwap(ctx, c.journal, expected, next); err != nil {
		c.journalUncertain = true
		if c.journalState.Revision != 0 {
			c.journalState.Status = JournalStatusRecoveryRequired
		}
		return canonicalError(err)
	}
	c.journalState = next
	c.journalUncertain = false
	return nil
}

func (c *Controller) markRecoveryRequired() {
	c.markRecoveryRequiredContext(context.Background())
}

func (c *Controller) markRecoveryRequiredContext(ctx context.Context) {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if c.journalState.Revision == 0 {
		c.journalUncertain = true
		return
	}
	if c.journalUncertain {
		c.journalState.Status = JournalStatusRecoveryRequired
		return
	}
	next := c.journalState
	next.Revision++
	next.Status = JournalStatusRecoveryRequired
	if err := safeCompareAndSwap(ctx, c.journal, c.journalState.Cursor(), next); err != nil {
		c.journalUncertain = true
		c.journalState.Status = JournalStatusRecoveryRequired
		return
	}
	c.journalState = next
}

func (c *Controller) progress(request *operationRequest, stage ProgressStage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if request.terminal {
		return
	}
	progress := &Progress{Stage: stage}
	c.publishLocked(Event{
		Kind:        EventOperationProgress,
		OperationID: request.operation.ID,
		State:       c.snapshot.State,
		Progress:    progress,
	})
}

func (c *Controller) finishLocked(request *operationRequest, outcome operationOutcome, err error) {
	if request.terminal {
		return
	}
	if c.active != request {
		structured := canonicalError(err)
		c.publishLocked(Event{
			Kind:        EventOperationFailed,
			OperationID: request.operation.ID,
			State:       c.snapshot.State,
			Error:       structured,
		})
		request.terminal = true
		return
	}
	if err == nil {
		c.snapshot.Applied = outcome.applied
		c.snapshot.Health = outcome.truth.Health
		c.snapshot.State = outcome.truth.State
		c.publishSnapshotLocked()
		c.publishLocked(Event{
			Kind:        EventOperationSucceeded,
			OperationID: request.operation.ID,
			State:       c.snapshot.State,
		})
		request.terminal = true
		return
	}

	structured := canonicalError(err)
	c.snapshot.Applied = request.previous
	c.snapshot.Health = unknownHealth()
	if c.recoveryRequired() {
		c.snapshot.State = ConnectionStateDegraded
	} else {
		switch structured.Code {
		case ErrorCodeReauthRequired, ErrorCodeUnauthorized:
			c.snapshot.State = ConnectionStateAuthRequired
		case ErrorCodeUnsupportedVersion:
			c.snapshot.State = ConnectionStateUpdateRequired
		default:
			if request.previous.Connected {
				c.snapshot.State = ConnectionStateDegraded
			} else {
				c.snapshot.State = ConnectionStateDisconnected
			}
		}
	}
	c.publishSnapshotLocked()
	c.publishLocked(Event{
		Kind:        EventOperationFailed,
		OperationID: request.operation.ID,
		State:       c.snapshot.State,
		Error:       structured,
	})
	request.terminal = true
}

func (c *Controller) failQueuedOnShutdown() {
	c.mu.Lock()
	c.closed = true
	for _, queued := range c.queue {
		c.finishLocked(queued, operationOutcome{}, &Error{
			Code:   ErrorCodeServiceUnavailable,
			Detail: "controller stopped before operation execution",
		})
	}
	c.queue = nil
	c.mu.Unlock()
}

func (c *Controller) failProcessorRequests() {
	c.mu.Lock()
	failure := &Error{Code: ErrorCodeDegraded, Detail: "controller processor panicked; conservative recovery is required"}
	if c.active != nil {
		c.finishLocked(c.active, operationOutcome{}, failure)
	}
	for _, queued := range c.queue {
		c.finishLocked(queued, operationOutcome{}, failure)
	}
	c.queue = nil
	c.active = nil
	c.activeCancel = nil
	c.snapshot.ActiveOperation = nil
	c.snapshot.Health = unknownHealth()
	c.snapshot.State = ConnectionStateDegraded
	c.publishSnapshotLocked()
	c.closed = true
	c.mu.Unlock()
}

func (c *Controller) normalizeCommandLocked(command Command) Command {
	current := c.snapshot.Desired
	if current.ConfigurationID == "" {
		current = desiredFromApplied(c.snapshot.Applied)
	}
	switch command.Kind {
	case CommandDisconnect:
		if command.Desired.ConfigurationID == "" {
			command.Desired = current
		}
		command.Desired.Connected = false
	case CommandRenew, CommandWake, CommandNetworkChange:
		if command.Desired.ConfigurationID == "" {
			command.Desired = current
		}
		command.Desired.Connected = true
	}
	return command
}

// publishSnapshotLocked is the only snapshot publication path. It validates the
// candidate and fails closed to a degraded, unknown-health view before emitting.
func (c *Controller) publishSnapshotLocked() {
	if err := c.snapshot.Validate(); err != nil {
		c.snapshot.Health = unknownHealth()
		c.snapshot.State = ConnectionStateDegraded
	}
	c.publishLocked(Event{Kind: EventSnapshotChanged, State: c.snapshot.State})
}

func (c *Controller) publishLocked(event Event) {
	event.OccurredAt = c.timestamp()
	published := c.eventLog.publish(event)
	c.snapshot.Sequence = published.Sequence
}

func validateControllerConfig(config *ControllerConfig) error {
	if config.Owner == "" || config.Sessions == nil || config.Planner == nil ||
		config.TruthGate == nil || config.WireGuard == nil || config.Routes == nil ||
		config.DNS == nil || config.Journal == nil {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "controller dependencies are incomplete"}
	}
	if config.OperationIDs == nil {
		config.OperationIDs = randomOperationIDGenerator{}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.EventCapacity == 0 {
		config.EventCapacity = defaultEventCapacity
	}
	if err := validateEventCapacity(config.EventCapacity); err != nil {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
	}
	if config.JournalCapacity == 0 {
		config.JournalCapacity = defaultJournalCapacity
	}
	if config.JournalCapacity < 1 {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "journal capacity must be positive"}
	}
	if config.RollbackTimeout == 0 {
		config.RollbackTimeout = defaultRollbackTimeout
	}
	if config.RollbackTimeout < 0 {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "rollback timeout must be positive"}
	}
	return nil
}

func validateCommand(command Command) error {
	if command.Desired.ConfigurationID == "" {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "configuration ID is required"}
	}
	if command.Desired.Generation == 0 {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "configuration generation is required"}
	}
	switch command.Kind {
	case CommandConnect, CommandSwitch, CommandRenew, CommandWake, CommandNetworkChange:
		if !command.Desired.Connected || command.Desired.GroupID == "" {
			return &Error{Code: ErrorCodeInvalidArgument, Detail: "connected intent requires a group"}
		}
	case CommandDisconnect:
		if command.Desired.Connected {
			return &Error{Code: ErrorCodeInvalidArgument, Detail: "disconnect intent cannot request connected state"}
		}
	case CommandReconcile:
		if command.Desired.Connected && command.Desired.GroupID == "" {
			return &Error{Code: ErrorCodeInvalidArgument, Detail: "connected reconcile requires a group"}
		}
	}
	return nil
}

func (k CommandKind) valid() bool {
	switch k {
	case CommandConnect, CommandDisconnect, CommandSwitch, CommandRenew, CommandWake, CommandNetworkChange, CommandReconcile:
		return true
	default:
		return false
	}
}

func (k CommandKind) allowedDuringRecovery() bool {
	return k == CommandDisconnect || k == CommandReconcile
}

func recoveryGateError() error {
	return &Error{Code: ErrorCodeDegraded, Detail: "durable recovery is required; only disconnect or complete reconcile is allowed"}
}

func recoveredSnapshot(journal AppliedJournal) Snapshot {
	health := unknownHealth()
	if journal.Revision == 0 {
		return Snapshot{State: ConnectionStateDisconnected, Health: health}
	}
	snapshot := Snapshot{
		Desired: desiredFromApplied(journal.Stable),
		Applied: journal.Stable,
		Health:  health,
	}
	if journal.Status.requiresRecovery() {
		snapshot.State = ConnectionStateDegraded
		if journal.Target.ConfigurationID != "" {
			snapshot.Desired = desiredFromApplied(journal.Target)
			snapshot.Desired.Connected = false
		}
	} else if journal.Stable.Connected {
		// Durable state is evidence of intent, not current health.
		snapshot.State = ConnectionStateDegraded
	} else {
		snapshot.State = ConnectionStateDisconnected
	}
	return snapshot
}

func desiredFromApplied(applied AppliedState) DesiredState {
	return DesiredState{
		ConfigurationID: applied.ConfigurationID,
		Generation:      applied.Generation,
		GroupID:         applied.GroupID,
		ExitNodeID:      applied.ExitNodeID,
		Connected:       applied.Connected,
	}
}

func (c *Controller) timestamp() (value time.Time) {
	defer func() {
		if recover() != nil {
			value = time.Now()
		}
	}()
	return c.now()
}

func unknownHealth() Health {
	return Health{
		WireGuard: HealthUnknown,
		Routes:    HealthUnknown,
		DNS:       HealthUnknown,
		EndToEnd:  HealthUnknown,
	}
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	if snapshot.ActiveOperation != nil {
		operation := *snapshot.ActiveOperation
		snapshot.ActiveOperation = &operation
	}
	return snapshot
}

func safeAcquireLease(ctx context.Context, store AppliedJournalStore, owner OwnerID) (lease AppliedJournalLease, err error) {
	defer func() {
		if recover() != nil {
			lease = nil
			err = &Error{Code: ErrorCodeServiceUnavailable, Detail: "journal lease dependency panicked"}
		}
	}()
	lease, err = store.AcquireLease(ctx, owner)
	if err != nil {
		return nil, canonicalError(err)
	}
	if lease == nil {
		return nil, &Error{Code: ErrorCodeServiceUnavailable, Detail: "journal lease dependency returned no lease"}
	}
	return lease, nil
}

func safeReleaseLease(lease AppliedJournalLease) (err error) {
	if lease == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = errors.New("journal lease release panicked")
		}
	}()
	return lease.Release()
}

func safeLoadJournal(ctx context.Context, store AppliedJournalStore, owner OwnerID) (journal AppliedJournal, err error) {
	defer func() {
		if recover() != nil {
			journal = AppliedJournal{}
			err = conservativeJournalError("load panic")
		}
	}()
	return store.Load(ctx, owner)
}

func safeCompareAndSwap(ctx context.Context, store AppliedJournalStore, expected JournalCursor, next AppliedJournal) (err error) {
	defer func() {
		if recover() != nil {
			err = conservativeJournalError("CAS panic")
		}
	}()
	return store.CompareAndSwap(ctx, expected, next)
}

func safeOperationID(generator OperationIDGenerator) (id OperationID, err error) {
	defer func() {
		if recover() != nil {
			id = ""
			err = errors.New("operation ID generator panicked")
		}
	}()
	return generator.NewOperationID()
}

func safeValueCall[T any](call func() (T, error)) (value T, err error) {
	defer func() {
		if recover() != nil {
			var zero T
			value = zero
			err = errors.New("controller dependency panicked")
		}
	}()
	return call()
}

func safeErrorCall(call func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("controller dependency panicked")
		}
	}()
	return call()
}

func dependencyError(err error, detail string) error {
	if err == nil {
		return nil
	}
	if structured, ok := AsError(err); ok {
		copy := *structured
		return &copy
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: ErrorCodeDeadlineExceeded, Detail: "controller dependency deadline exceeded"}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: ErrorCodeConflict, Detail: "operation canceled by a newer controller intent"}
	}
	return &Error{Code: ErrorCodeServiceUnavailable, Detail: detail}
}

func canonicalError(err error) *Error {
	if structured, ok := AsError(err); ok {
		copy := *structured
		return &copy
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: ErrorCodeDeadlineExceeded, Detail: "controller operation deadline exceeded"}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: ErrorCodeConflict, Detail: "operation canceled by a newer controller intent"}
	}
	return &Error{Code: ErrorCodeServiceUnavailable, Detail: "controller operation failed"}
}

type randomOperationIDGenerator struct{}

func (randomOperationIDGenerator) NewOperationID() (OperationID, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate operation ID: %w", err)
	}
	return OperationID(hex.EncodeToString(value[:])), nil
}

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	durableIntentSchemaVersion = 1
	defaultIntentMaxBytes      = 64 << 10
)

// DurableIntent is the user's non-secret connection preference. It is kept
// separate from AppliedJournal: intent authorizes reconnect after a restart,
// while the journal is only evidence about host mutations.
type DurableIntent struct {
	SchemaVersion int          `json:"schema_version"`
	Owner         OwnerID      `json:"owner"`
	Revision      uint64       `json:"revision"`
	Desired       DesiredState `json:"desired"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

func (i DurableIntent) validate(owner OwnerID) error {
	if i.SchemaVersion != durableIntentSchemaVersion || owner == "" || i.Owner != owner {
		return errors.New("durable intent schema or owner mismatch")
	}
	if i.Revision == 0 || i.UpdatedAt.IsZero() {
		return errors.New("durable intent revision or timestamp is missing")
	}
	if i.Desired.Connected {
		return validateCommand(Command{Kind: CommandConnect, Desired: i.Desired})
	}
	if i.Desired.ConfigurationID == "" {
		if i.Desired != (DesiredState{}) {
			return errors.New("unconfigured disconnected intent is inconsistent")
		}
		return nil
	}
	if i.Desired.Generation == 0 {
		return errors.New("configured disconnected intent requires a generation")
	}
	return nil
}

// DurableIntentStore atomically persists one owner-scoped preference.
type DurableIntentStore interface {
	Load(context.Context, OwnerID) (DurableIntent, error)
	CompareAndSwap(context.Context, uint64, DurableIntent) error
	Replace(context.Context, DurableIntent) error
}

// FileDurableIntentStore writes bounded JSON through same-directory fsync and
// rename. The controller's lifetime journal lease excludes a second service;
// this store additionally serializes all accesses within the process.
type FileDurableIntentStore struct {
	path     string
	maxBytes int64
	mu       sync.Mutex
}

func NewFileDurableIntentStore(path string) (*FileDurableIntentStore, error) {
	if path == "" {
		return nil, errors.New("durable intent path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return &FileDurableIntentStore{path: absolute, maxBytes: defaultIntentMaxBytes}, nil
}

func (s *FileDurableIntentStore) Load(ctx context.Context, owner OwnerID) (DurableIntent, error) {
	if ctx == nil {
		return DurableIntent{}, errors.New("durable intent context is required")
	}
	if err := ctx.Err(); err != nil {
		return DurableIntent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked(owner)
}

func (s *FileDurableIntentStore) loadUnlocked(owner OwnerID) (DurableIntent, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return DurableIntent{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return DurableIntent{}, errors.New("durable intent path is unsafe")
	}
	file, err := os.Open(s.path)
	if err != nil {
		return DurableIntent{}, errors.New("durable intent is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, s.maxBytes+1))
	if err != nil || int64(len(data)) > s.maxBytes {
		return DurableIntent{}, errors.New("durable intent is unreadable")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var intent DurableIntent
	if err := decoder.Decode(&intent); err != nil || ensureJSONEOF(decoder) != nil {
		return DurableIntent{}, errors.New("durable intent is corrupt")
	}
	if err := intent.validate(owner); err != nil {
		return DurableIntent{}, errors.New("durable intent is invalid")
	}
	return intent, nil
}

func (s *FileDurableIntentStore) CompareAndSwap(ctx context.Context, expectedRevision uint64, next DurableIntent) error {
	if ctx == nil {
		return errors.New("durable intent context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := next.validate(next.Owner); err != nil || next.Revision != expectedRevision+1 {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "invalid durable intent revision"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.loadUnlocked(next.Owner)
	if err != nil {
		return &Error{Code: ErrorCodeConflict, Detail: "durable intent requires conservative recovery"}
	}
	if current.Revision != expectedRevision {
		return &Error{Code: ErrorCodeConflict, Detail: "durable intent changed concurrently"}
	}
	return s.writeAtomic(ctx, next)
}

// Replace is reserved for fail-closed repair after a disconnected cleanup. It
// never follows or replaces a symlink and never writes outside the fixed path.
func (s *FileDurableIntentStore) Replace(ctx context.Context, next DurableIntent) error {
	if ctx == nil {
		return errors.New("durable intent context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := next.validate(next.Owner); err != nil {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "invalid durable intent replacement"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if info, err := os.Lstat(s.path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return &Error{Code: ErrorCodeConflict, Detail: "unsafe durable intent cannot be replaced"}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return &Error{Code: ErrorCodeConflict, Detail: "durable intent path is unavailable"}
	}
	return s.writeAtomic(ctx, next)
}

func (s *FileDurableIntentStore) writeAtomic(ctx context.Context, intent DurableIntent) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return &Error{Code: ErrorCodeConflict, Detail: "durable intent directory is unavailable"}
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".desktop-intent-*")
	if err != nil {
		return &Error{Code: ErrorCodeConflict, Detail: "durable intent temporary file is unavailable"}
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	data, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if int64(len(data)) > s.maxBytes {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "durable intent exceeds byte limit"}
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return err
	}
	removeTemporary = false
	return syncJournalDirectory(s.path)
}

// ControllerBackend is the state/event contract shared with IPC without any
// transport dependency.
type ControllerBackend interface {
	Execute(context.Context, Command) (OperationID, error)
	Snapshot(context.Context) (Snapshot, error)
	Subscribe(context.Context, Sequence) (<-chan Event, error)
}

// DurableController persists explicit user intent before forwarding an
// operation. Automatic lifecycle commands bypass this facade and therefore can
// never turn a transient cleanup into a permanent disconnect.
type DurableController struct {
	backend   ControllerBackend
	store     DurableIntentStore
	owner     OwnerID
	now       func() time.Time
	lifetime  context.Context
	changed   chan struct{}
	ready     chan struct{}
	readyOnce sync.Once

	mu        sync.Mutex
	executeMu sync.Mutex
	intent    DurableIntent
	present   bool
	invalid   bool
}

func NewDurableController(ctx context.Context, backend ControllerBackend, store DurableIntentStore, owner OwnerID, now func() time.Time) (*DurableController, error) {
	if ctx == nil || backend == nil || store == nil || owner == "" {
		return nil, &Error{Code: ErrorCodeInvalidArgument, Detail: "durable controller dependencies are incomplete"}
	}
	if now == nil {
		now = time.Now
	}
	result := &DurableController{
		backend: backend, store: store, owner: owner, now: now, lifetime: ctx,
		changed: make(chan struct{}, 1), ready: make(chan struct{}),
	}
	intent, err := store.Load(ctx, owner)
	if err != nil {
		result.invalid = true
		return result, nil
	}
	if intent.Revision != 0 {
		result.intent = intent
		result.present = true
	}
	return result, nil
}

func (c *DurableController) Execute(ctx context.Context, command Command) (OperationID, error) {
	if ctx == nil {
		return "", &Error{Code: ErrorCodeInvalidArgument, Detail: "execute context is required"}
	}
	if command.Kind != CommandConnect && command.Kind != CommandSwitch && command.Kind != CommandDisconnect {
		return c.backend.Execute(ctx, command)
	}
	if command.Kind != CommandDisconnect {
		select {
		case <-c.ready:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	c.executeMu.Lock()
	defer c.executeMu.Unlock()

	var watchContext context.Context
	var watchCancel context.CancelFunc
	var events <-chan Event
	var rollbackDesired DesiredState
	if command.Kind == CommandDisconnect {
		snapshot, err := c.backend.Snapshot(ctx)
		if err != nil {
			return "", err
		}
		if command.Desired.ConfigurationID == "" {
			command.Desired = snapshot.Desired
			if command.Desired.ConfigurationID == "" {
				command.Desired = desiredFromApplied(snapshot.Applied)
			}
		}
		command.Desired.Connected = false
	} else {
		if err := validateCommand(command); err != nil {
			return "", err
		}
		snapshot, err := c.backend.Snapshot(ctx)
		if err != nil {
			return "", err
		}
		rollbackDesired = command.Desired
		rollbackDesired.Connected = false
		if snapshot.Applied.Connected {
			rollbackDesired = desiredFromApplied(snapshot.Applied)
		}
		watchContext, watchCancel = context.WithCancel(c.lifetime)
		events, err = c.backend.Subscribe(watchContext, snapshot.Sequence)
		if err != nil {
			watchCancel()
			return "", err
		}
	}
	cancelWatch := func() {
		if watchCancel != nil {
			watchCancel()
		}
	}

	previous, previousPresent, previousInvalid := c.Intent()
	persisted, err := c.persistExplicit(ctx, command.Desired, command.Kind == CommandDisconnect)
	if err != nil {
		cancelWatch()
		return "", err
	}
	operationID, err := c.backend.Execute(ctx, command)
	if err != nil {
		cancelWatch()
		if command.Kind != CommandDisconnect {
			compensationContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			restoreErr := c.restoreSafeIntent(compensationContext, previous, previousPresent, previousInvalid)
			cancel()
			c.signalChanged()
			return "", errors.Join(err, restoreErr)
		}
	} else if watchCancel != nil {
		go c.observeExplicitTerminal(watchContext, watchCancel, events, operationID, persisted, rollbackDesired)
	}
	c.signalChanged()
	return operationID, err
}

func (c *DurableController) persistExplicit(ctx context.Context, desired DesiredState, allowRepair bool) (DurableIntent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.invalid && !allowRepair {
		return DurableIntent{}, &Error{Code: ErrorCodeDegraded, Detail: "durable intent is invalid; disconnect is required before reconnect"}
	}
	now := c.now().UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	next := DurableIntent{
		SchemaVersion: durableIntentSchemaVersion,
		Owner:         c.owner,
		Revision:      c.intent.Revision + 1,
		Desired:       desired,
		UpdatedAt:     now,
	}
	var err error
	if c.invalid {
		next.Revision = 1
		err = c.store.Replace(ctx, next)
	} else {
		err = c.store.CompareAndSwap(ctx, c.intent.Revision, next)
	}
	if err != nil {
		return DurableIntent{}, err
	}
	c.intent = next
	c.present = true
	c.invalid = false
	return next, nil
}

func (c *DurableController) observeExplicitTerminal(
	ctx context.Context,
	cancel context.CancelFunc,
	events <-chan Event,
	operationID OperationID,
	expected DurableIntent,
	rollbackDesired DesiredState,
) {
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.OperationID != operationID {
				continue
			}
			switch event.Kind {
			case EventOperationSucceeded:
				return
			case EventOperationFailed:
				if event.Validate() == nil {
					c.rollbackFailedExplicitIntent(ctx, expected, rollbackDesired)
				}
				return
			}
		}
	}
}

func (c *DurableController) rollbackFailedExplicitIntent(ctx context.Context, expected DurableIntent, rollbackDesired DesiredState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.invalid || !c.present ||
		c.intent.SchemaVersion != expected.SchemaVersion || c.intent.Owner != expected.Owner ||
		c.intent.Revision != expected.Revision || !c.intent.Desired.Connected ||
		c.intent.Desired != expected.Desired {
		return
	}
	next := c.intent
	next.Revision++
	next.Desired = rollbackDesired
	next.UpdatedAt = c.now().UTC()
	if next.UpdatedAt.IsZero() {
		next.UpdatedAt = time.Now().UTC()
	}
	if err := c.store.CompareAndSwap(ctx, c.intent.Revision, next); err != nil {
		return
	}
	c.intent = next
	c.signalChanged()
}

func (c *DurableController) restoreSafeIntent(ctx context.Context, previous DurableIntent, present, invalid bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if invalid {
		c.invalid = true
		c.present = false
		return &Error{Code: ErrorCodeDegraded, Detail: "previous durable intent was invalid"}
	}
	desired := DesiredState{}
	if present {
		desired = previous.Desired
	}
	now := c.now().UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	next := DurableIntent{
		SchemaVersion: durableIntentSchemaVersion,
		Owner:         c.owner,
		Revision:      c.intent.Revision + 1,
		Desired:       desired,
		UpdatedAt:     now,
	}
	if err := c.store.CompareAndSwap(ctx, c.intent.Revision, next); err != nil {
		// A disconnected replacement is the final fail-closed fallback when CAS
		// state became uncertain after the rejected connected operation.
		next.Revision = 1
		if replaceErr := c.store.Replace(ctx, next); replaceErr != nil {
			c.invalid = true
			c.present = false
			return errors.Join(err, replaceErr)
		}
	}
	c.intent = next
	c.present = true
	c.invalid = false
	return nil
}

func (c *DurableController) Snapshot(ctx context.Context) (Snapshot, error) {
	snapshot, err := c.backend.Snapshot(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	intent, present, _ := c.Intent()
	if present {
		snapshot.Desired = intent.Desired
	} else {
		fallback := desiredFromApplied(snapshot.Applied)
		if fallback.ConfigurationID == "" {
			fallback = snapshot.Desired
		}
		fallback.Connected = false
		snapshot.Desired = fallback
	}
	return snapshot, nil
}

func (c *DurableController) Subscribe(ctx context.Context, after Sequence) (<-chan Event, error) {
	return c.backend.Subscribe(ctx, after)
}

// executeAutomatic atomically rechecks intent while accepting a lifecycle
// operation. Holding the intent mutex through backend acceptance guarantees an
// explicit disconnect either prevents this enqueue or runs afterwards and
// cancels/supersedes it.
func (c *DurableController) executeAutomatic(ctx context.Context, kind CommandKind, expectedConnected bool) error {
	operationContext, cancel := context.WithCancel(ctx)
	defer cancel()
	snapshot, err := c.backend.Snapshot(operationContext)
	if err != nil {
		return err
	}
	events, err := c.backend.Subscribe(operationContext, snapshot.Sequence)
	if err != nil {
		return err
	}
	operationID, accepted, err := func() (operationID OperationID, accepted bool, executeErr error) {
		c.executeMu.Lock()
		defer c.executeMu.Unlock()
		c.mu.Lock()
		defer c.mu.Unlock()
		defer func() {
			if recover() != nil {
				operationID = ""
				accepted = false
				executeErr = &Error{Code: ErrorCodeServiceUnavailable, Detail: "automatic controller operation panicked before safe acceptance"}
			}
		}()
		if c.invalid || !c.present || c.intent.Desired.Connected != expectedConnected || (!expectedConnected && kind != CommandDisconnect) {
			return "", false, nil
		}
		if kind == CommandDisconnect {
			if pending, ok := c.backend.(interface{ hasPendingCommand(CommandKind) bool }); ok && pending.hasPendingCommand(kind) {
				return "", false, nil
			}
		}
		desired := c.intent.Desired
		if kind == CommandDisconnect {
			desired.Connected = false
		}
		operationID, executeErr = c.backend.Execute(operationContext, Command{Kind: kind, Desired: desired})
		return operationID, true, executeErr
	}()
	if err != nil || !accepted {
		return err
	}
	return waitForOperation(operationContext, events, operationID)
}

// Intent returns a copy plus presence and corruption status.
func (c *DurableController) Intent() (DurableIntent, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.intent, c.present, c.invalid
}

func (c *DurableController) repairDisconnected(ctx context.Context, desired DesiredState) error {
	desired.Connected = false
	c.executeMu.Lock()
	defer c.executeMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	// A pre-ready explicit disconnect may already have established a newer safe
	// preference; startup repair must never overwrite it.
	if c.present && !c.invalid && !c.intent.Desired.Connected {
		return nil
	}
	next := DurableIntent{
		SchemaVersion: durableIntentSchemaVersion,
		Owner:         c.owner,
		Revision:      1,
		Desired:       desired,
		UpdatedAt:     c.now().UTC(),
	}
	if next.UpdatedAt.IsZero() {
		next.UpdatedAt = time.Now().UTC()
	}
	if err := c.store.Replace(ctx, next); err != nil {
		return err
	}
	c.intent = next
	c.present = true
	c.invalid = false
	c.signalChanged()
	return nil
}

func (c *DurableController) markReady() {
	c.readyOnce.Do(func() { close(c.ready) })
	c.signalChanged()
}

func (c *DurableController) signalChanged() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

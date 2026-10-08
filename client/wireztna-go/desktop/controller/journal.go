package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	appliedJournalSchemaVersion = 1
	defaultJournalMaxBytes      = 1 << 20
)

var errKernelLockBusy = errors.New("kernel file lock is already held")

// JournalStatus identifies a durable transaction checkpoint.
type JournalStatus string

const (
	JournalStatusApplying         JournalStatus = "applying"
	JournalStatusVerifying        JournalStatus = "verifying"
	JournalStatusCommitted        JournalStatus = "committed"
	JournalStatusRollingBack      JournalStatus = "rolling_back"
	JournalStatusRolledBack       JournalStatus = "rolled_back"
	JournalStatusRecoveryRequired JournalStatus = "recovery_required"
)

// JournalStep is a non-secret platform transaction step.
type JournalStep string

const (
	JournalStepWireGuard JournalStep = "wireguard"
	JournalStepRoutes    JournalStep = "routes"
	JournalStepDNS       JournalStep = "dns"
)

// JournalEntryStatus identifies the last durable observation for one step.
type JournalEntryStatus string

const (
	JournalEntryPrepared         JournalEntryStatus = "prepared"
	JournalEntryApplied          JournalEntryStatus = "applied"
	JournalEntryRollbackStarted  JournalEntryStatus = "rollback_started"
	JournalEntryRollbackComplete JournalEntryStatus = "rollback_complete"
	JournalEntryRollbackFailed   JournalEntryStatus = "rollback_failed"
)

// JournalEntry contains only owner-safe transaction metadata. It deliberately
// excludes configurations, endpoint data, credential values, and error detail.
type JournalEntry struct {
	Step       JournalStep        `json:"step"`
	Status     JournalEntryStatus `json:"status"`
	OccurredAt time.Time          `json:"occurred_at"`
}

// JournalCursor is the compare-and-swap identity for one durable revision.
type JournalCursor struct {
	Owner      OwnerID
	Generation Generation
	Revision   uint64
}

// AppliedJournal durably separates the last stable state from an in-progress
// target. Both states are non-secret domain values.
type AppliedJournal struct {
	SchemaVersion int            `json:"schema_version"`
	Owner         OwnerID        `json:"owner"`
	Generation    Generation     `json:"generation"`
	Revision      uint64         `json:"revision"`
	Status        JournalStatus  `json:"status"`
	Stable        AppliedState   `json:"stable"`
	Target        AppliedState   `json:"target"`
	Entries       []JournalEntry `json:"entries"`
}

// Cursor returns the owner/generation/revision CAS identity.
func (j AppliedJournal) Cursor() JournalCursor {
	return JournalCursor{Owner: j.Owner, Generation: j.Generation, Revision: j.Revision}
}

func (j AppliedJournal) validate(owner OwnerID, maxEntries int) error {
	if j.SchemaVersion != appliedJournalSchemaVersion {
		return errors.New("unsupported applied journal schema")
	}
	if owner == "" || j.Owner != owner {
		return errors.New("applied journal owner mismatch")
	}
	if j.Revision == 0 {
		return errors.New("applied journal revision is required")
	}
	if j.Generation != j.Target.Generation {
		return errors.New("applied journal generation does not match target")
	}
	if !j.Status.valid() {
		return errors.New("unknown applied journal status")
	}
	if len(j.Entries) > maxEntries {
		return errors.New("applied journal exceeds entry limit")
	}
	if j.Status == JournalStatusCommitted && j.Stable != j.Target {
		return errors.New("committed applied journal is not stable")
	}
	for _, entry := range j.Entries {
		if !entry.Step.valid() || !entry.Status.valid() {
			return errors.New("applied journal contains an unknown step checkpoint")
		}
	}
	return nil
}

func (s JournalStatus) valid() bool {
	switch s {
	case JournalStatusApplying, JournalStatusVerifying, JournalStatusCommitted,
		JournalStatusRollingBack, JournalStatusRolledBack, JournalStatusRecoveryRequired:
		return true
	default:
		return false
	}
}

func (s JournalStatus) requiresRecovery() bool {
	switch s {
	case JournalStatusApplying, JournalStatusVerifying, JournalStatusRollingBack, JournalStatusRecoveryRequired:
		return true
	default:
		return false
	}
}

func (s JournalStep) valid() bool {
	return s == JournalStepWireGuard || s == JournalStepRoutes || s == JournalStepDNS
}

func (s JournalEntryStatus) valid() bool {
	switch s {
	case JournalEntryPrepared, JournalEntryApplied, JournalEntryRollbackStarted,
		JournalEntryRollbackComplete, JournalEntryRollbackFailed:
		return true
	default:
		return false
	}
}

// AppliedJournalLease is exclusive ownership of one journal and its platform
// resources. It is retained for the entire Controller lifetime. Implementations
// must use crash-released kernel ownership rather than pathname creation.
type AppliedJournalLease interface {
	Release() error
}

// AppliedJournalStore provides a lifetime lease plus durable owner/generation
// CAS. A zero journal from Load means no journal exists yet. Methods must obey
// context cancellation and must not leave background writes running after return.
type AppliedJournalStore interface {
	AcquireLease(context.Context, OwnerID) (AppliedJournalLease, error)
	Load(context.Context, OwnerID) (AppliedJournal, error)
	CompareAndSwap(context.Context, JournalCursor, AppliedJournal) error
}

// FileAppliedJournalStore is a bounded JSON journal using same-directory temp
// files, file sync, atomic rename, and directory sync. A lifetime kernel lease
// excludes other controllers, while a distinct kernel lock serializes every CAS.
// Lock pathnames are persistent rendezvous points; ownership is attached to open
// handles and therefore released by the kernel after a crash.
type FileAppliedJournalStore struct {
	path       string
	maxEntries int
	maxBytes   int64
	pathLock   *sync.Mutex
	leaseLock  *sync.Mutex
}

var (
	appliedJournalPathLocks  sync.Map
	appliedJournalLeaseLocks sync.Map
)

// NewFileAppliedJournalStore creates a durable store. The parent directory is
// created with owner-only permissions when a lease or first write is acquired.
func NewFileAppliedJournalStore(path string, maxEntries int) (*FileAppliedJournalStore, error) {
	if path == "" {
		return nil, errors.New("applied journal path is required")
	}
	if maxEntries < 1 {
		return nil, errors.New("applied journal entry limit must be positive")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve applied journal path: %w", err)
	}
	pathLock, _ := appliedJournalPathLocks.LoadOrStore(absolute, &sync.Mutex{})
	leaseLock, _ := appliedJournalLeaseLocks.LoadOrStore(absolute, &sync.Mutex{})
	return &FileAppliedJournalStore{
		path:       absolute,
		maxEntries: maxEntries,
		maxBytes:   defaultJournalMaxBytes,
		pathLock:   pathLock.(*sync.Mutex),
		leaseLock:  leaseLock.(*sync.Mutex),
	}, nil
}

// AcquireLease takes exclusive in-process and kernel ownership until Release.
// It never uses O_EXCL and therefore cannot leave stale ownership after a crash.
func (s *FileAppliedJournalStore) AcquireLease(ctx context.Context, owner OwnerID) (AppliedJournalLease, error) {
	if ctx == nil {
		return nil, errors.New("journal lease context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if owner == "" {
		return nil, &Error{Code: ErrorCodeInvalidArgument, Detail: "journal lease owner is required"}
	}
	if !s.leaseLock.TryLock() {
		return nil, &Error{Code: ErrorCodeConflict, Detail: "another controller owns the applied journal"}
	}
	releaseProcessLock := true
	defer func() {
		if releaseProcessLock {
			s.leaseLock.Unlock()
		}
	}()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, conservativeJournalError("create lease directory")
	}
	kernel, err := acquireKernelFileLock(ctx, s.path+".lease")
	if errors.Is(err, errKernelLockBusy) {
		return nil, &Error{Code: ErrorCodeConflict, Detail: "another controller owns the applied journal"}
	}
	if err != nil {
		return nil, conservativeJournalError("acquire controller lease")
	}
	releaseProcessLock = false
	return &fileAppliedJournalLease{kernel: kernel, processLock: s.leaseLock}, nil
}

type fileAppliedJournalLease struct {
	kernel      *kernelFileLock
	processLock *sync.Mutex
	once        sync.Once
	err         error
}

func (l *fileAppliedJournalLease) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.kernel != nil {
			l.err = l.kernel.release()
		}
		if l.processLock != nil {
			l.processLock.Unlock()
		}
	})
	return l.err
}

// Load reads and strictly validates the journal. Corrupt, oversized, foreign,
// or unsupported data fails closed so callers cannot claim connected state.
func (s *FileAppliedJournalStore) Load(ctx context.Context, owner OwnerID) (AppliedJournal, error) {
	if ctx == nil {
		return AppliedJournal{}, errors.New("journal context is required")
	}
	if err := ctx.Err(); err != nil {
		return AppliedJournal{}, err
	}
	if !s.pathLock.TryLock() {
		return AppliedJournal{}, &Error{Code: ErrorCodeConflict, Detail: "applied journal access is already in progress"}
	}
	defer s.pathLock.Unlock()
	return s.loadUnlocked(owner)
}

func (s *FileAppliedJournalStore) loadUnlocked(owner OwnerID) (AppliedJournal, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return AppliedJournal{}, nil
	}
	if err != nil {
		return AppliedJournal{}, conservativeJournalError("open")
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, s.maxBytes+1))
	if err != nil || int64(len(data)) > s.maxBytes {
		return AppliedJournal{}, conservativeJournalError("read")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var journal AppliedJournal
	if err := decoder.Decode(&journal); err != nil {
		return AppliedJournal{}, conservativeJournalError("decode")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return AppliedJournal{}, conservativeJournalError("decode")
	}
	if err := journal.validate(owner, s.maxEntries); err != nil {
		return AppliedJournal{}, conservativeJournalError("validate")
	}
	return journal, nil
}

// CompareAndSwap atomically replaces the file only when owner, generation, and
// revision still match expected. Every load/compare/write sequence holds a
// crash-released kernel lock; lock ownership never depends on deleting a file.
func (s *FileAppliedJournalStore) CompareAndSwap(ctx context.Context, expected JournalCursor, next AppliedJournal) error {
	if ctx == nil {
		return errors.New("journal context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if expected.Owner == "" || next.Owner != expected.Owner {
		return &Error{Code: ErrorCodeConflict, Detail: "applied journal owner CAS mismatch"}
	}
	if err := next.validate(expected.Owner, s.maxEntries); err != nil {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "invalid applied journal revision"}
	}
	if !s.pathLock.TryLock() {
		return &Error{Code: ErrorCodeConflict, Detail: "applied journal CAS is already in progress"}
	}
	defer s.pathLock.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return conservativeJournalError("create CAS lock directory")
	}
	kernel, err := acquireKernelFileLock(ctx, s.path+".cas.lock")
	if errors.Is(err, errKernelLockBusy) {
		return &Error{Code: ErrorCodeConflict, Detail: "applied journal CAS is already in progress"}
	}
	if err != nil {
		return conservativeJournalError("acquire CAS lock")
	}
	defer kernel.release()

	current, err := s.loadUnlocked(expected.Owner)
	if err != nil {
		return err
	}
	actual := JournalCursor{Owner: expected.Owner}
	if current.Revision != 0 {
		actual = current.Cursor()
	}
	if actual != expected || next.Revision != expected.Revision+1 {
		return &Error{Code: ErrorCodeConflict, Detail: "applied journal changed concurrently"}
	}
	return s.writeAtomic(ctx, next)
}

func (s *FileAppliedJournalStore) writeAtomic(ctx context.Context, journal AppliedJournal) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return conservativeJournalError("create directory")
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".applied-journal-*")
	if err != nil {
		return conservativeJournalError("create temporary file")
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
		return conservativeJournalError("set permissions")
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return conservativeJournalError("encode")
	}
	data = append(data, '\n')
	if int64(len(data)) > s.maxBytes {
		return &Error{Code: ErrorCodeInvalidArgument, Detail: "applied journal exceeds byte limit"}
	}
	if _, err := temporary.Write(data); err != nil {
		return conservativeJournalError("write")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return conservativeJournalError("sync temporary file")
	}
	if err := temporary.Close(); err != nil {
		return conservativeJournalError("close temporary file")
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return conservativeJournalError("replace")
	}
	removeTemporary = false
	if err := syncJournalDirectory(s.path); err != nil {
		return conservativeJournalError("sync directory")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func conservativeJournalError(_ string) error {
	return &Error{Code: ErrorCodeConflict, Detail: "applied journal is unavailable; conservative recovery is required"}
}

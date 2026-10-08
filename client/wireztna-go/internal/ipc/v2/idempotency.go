package v2

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

var (
	// ErrIdempotencyConflict means a key was reused with different payload bytes.
	ErrIdempotencyConflict = errors.New("idempotency key was already used with a different payload")
	// ErrIdempotencyCapacity means the bounded store has no available entry.
	ErrIdempotencyCapacity = errors.New("idempotency store is at capacity")
)

// IdempotencyResult is independent of request_id so a replay response can be
// correlated to the retrying request while preserving its original operation.
type IdempotencyResult struct {
	OperationID string
	Error       *WireError
}

type idempotencyEntry struct {
	hash      [sha256.Size]byte
	expiresAt time.Time
	ready     chan struct{}
	complete  bool
	result    IdempotencyResult
}

// IdempotencyStore bounds mutation execution by configured owner, command, key,
// and canonical payload hash. It is safe for concurrent callers.
type IdempotencyStore struct {
	mu       sync.Mutex
	entries  map[string]*idempotencyEntry
	ttl      time.Duration
	capacity int
	now      func() time.Time
}

// NewIdempotencyStore creates a bounded store. TTL starts when execution completes.
func NewIdempotencyStore(ttl time.Duration, capacity int) (*IdempotencyStore, error) {
	if ttl <= 0 {
		return nil, errors.New("idempotency TTL must be positive")
	}
	if capacity <= 0 {
		return nil, errors.New("idempotency capacity must be positive")
	}
	return &IdempotencyStore{
		entries:  make(map[string]*idempotencyEntry),
		ttl:      ttl,
		capacity: capacity,
		now:      time.Now,
	}, nil
}

// Execute runs action at most once for a scope. Concurrent exact replays wait
// for the first terminal result; key reuse with a changed payload fails immediately.
func (s *IdempotencyStore) Execute(
	ctx context.Context,
	owner ExpectedOwnerIdentity,
	command Command,
	key string,
	canonicalPayload []byte,
	action func(context.Context) IdempotencyResult,
) (result IdempotencyResult, err error) {
	if s == nil || action == nil {
		return IdempotencyResult{}, errors.New("idempotency execution is not configured")
	}
	ownerKey, err := identityKey(owner)
	if err != nil {
		return IdempotencyResult{}, err
	}
	if !command.mutates() || key == "" {
		return IdempotencyResult{}, errors.New("idempotency requires a mutation command and key")
	}
	if ctx == nil {
		return IdempotencyResult{}, errors.New("idempotency context is nil")
	}
	if err := ctx.Err(); err != nil {
		return IdempotencyResult{}, err
	}

	scope := ownerKey + "\x00" + string(command) + "\x00" + key
	hash := sha256.Sum256(canonicalPayload)

	s.mu.Lock()
	now := s.now()
	s.removeExpiredLocked(now)
	if existing, ok := s.entries[scope]; ok {
		if existing.hash != hash {
			s.mu.Unlock()
			return IdempotencyResult{}, ErrIdempotencyConflict
		}
		ready := existing.ready
		s.mu.Unlock()
		select {
		case <-ready:
			s.mu.Lock()
			result := cloneIdempotencyResult(existing.result)
			s.mu.Unlock()
			return result, nil
		case <-ctx.Done():
			return IdempotencyResult{}, ctx.Err()
		}
	}
	if len(s.entries) >= s.capacity {
		s.mu.Unlock()
		return IdempotencyResult{}, ErrIdempotencyCapacity
	}

	entry := &idempotencyEntry{
		hash:  hash,
		ready: make(chan struct{}),
	}
	s.entries[scope] = entry
	s.mu.Unlock()

	// The defer is the terminal-state boundary. A panic may have happened after
	// an external side effect, so the ambiguous action is never re-executed.
	func() {
		defer func() {
			if recover() != nil {
				result = IdempotencyResult{Error: &WireError{
					Code:   ErrorCodeServiceUnavailable,
					Detail: "operation outcome is unavailable",
				}}
			}
			s.finalize(entry, result)
		}()
		result = action(ctx)
	}()

	return cloneIdempotencyResult(result), nil
}

func (s *IdempotencyStore) finalize(entry *idempotencyEntry, result IdempotencyResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.complete {
		return
	}
	entry.result = cloneIdempotencyResult(result)
	entry.complete = true
	entry.expiresAt = s.now().Add(s.ttl)
	close(entry.ready)
}

func (s *IdempotencyStore) removeExpiredLocked(now time.Time) {
	for key, entry := range s.entries {
		if entry.complete && !entry.expiresAt.After(now) {
			delete(s.entries, key)
		}
	}
}

func cloneIdempotencyResult(source IdempotencyResult) IdempotencyResult {
	result := source
	if source.Error != nil {
		wireError := *source.Error
		result.Error = &wireError
	}
	return result
}

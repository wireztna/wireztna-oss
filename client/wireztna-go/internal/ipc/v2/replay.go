package v2

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/wireztna/client/desktop/controller"
)

// ErrResyncRequired means the cursor cannot continue exactly on this stream.
var ErrResyncRequired = errors.New("event replay requires a fresh snapshot")

type replaySubscriber struct {
	events chan controller.Event
	done   chan struct{}
}

// ReplayBuffer is a bounded event hub for one explicit stream epoch. Append
// and Subscribe share one lock, making replay-to-live handoff atomic.
type ReplayBuffer struct {
	mu          sync.RWMutex
	capacity    int
	identity    StreamIdentity
	events      []controller.Event
	subscribers map[uint64]*replaySubscriber
	nextID      uint64
}

// NewReplayBuffer creates a bounded replay window for one stream epoch.
func NewReplayBuffer(capacity int, identity StreamIdentity) (*ReplayBuffer, error) {
	if capacity <= 0 {
		return nil, errors.New("replay capacity must be positive")
	}
	if err := identity.validate(); err != nil {
		return nil, fmt.Errorf("invalid replay stream identity: %w", err)
	}
	return &ReplayBuffer{
		capacity:    capacity,
		identity:    identity,
		subscribers: make(map[uint64]*replaySubscriber),
	}, nil
}

// Identity returns the immutable identity of this replay epoch.
func (b *ReplayBuffer) Identity() StreamIdentity {
	if b == nil {
		return StreamIdentity{}
	}
	return b.identity
}

// Append validates one source event, retains it, and publishes it in exactly
// contiguous sequence order.
func (b *ReplayBuffer) Append(event controller.Event) error {
	if b == nil {
		return errors.New("replay buffer is nil")
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("append invalid controller event: %w", err)
	}
	if event.Sequence == 0 {
		return errors.New("event sequence must be positive")
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if count := len(b.events); count > 0 {
		last := b.events[count-1].Sequence
		if last == controller.Sequence(math.MaxUint64) || event.Sequence != last+1 {
			return errors.New("event sequence must be exactly contiguous")
		}
	}
	b.events = append(b.events, cloneControllerEvent(event))
	if len(b.events) > b.capacity {
		copy(b.events, b.events[len(b.events)-b.capacity:])
		b.events = b.events[:b.capacity]
	}

	for id, subscriber := range b.subscribers {
		select {
		case subscriber.events <- cloneControllerEvent(event):
		default:
			// Server maps every unexpected source close to a terminal
			// RESYNC_REQUIRED frame; overflow is never a clean wire EOF.
			close(subscriber.events)
			close(subscriber.done)
			delete(b.subscribers, id)
		}
	}
	return nil
}

// Replay returns retained events strictly after cursor. Stale, future, and
// wrong-epoch cursors require a fresh snapshot.
func (b *ReplayBuffer) Replay(cursor StreamCursor) ([]controller.Event, error) {
	if b == nil {
		return nil, errors.New("replay buffer is nil")
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.replayLocked(cursor)
}

// Subscribe atomically queues replayed events and registers for future Append calls.
func (b *ReplayBuffer) Subscribe(ctx context.Context, cursor StreamCursor) (<-chan controller.Event, error) {
	if b == nil {
		return nil, errors.New("replay buffer is nil")
	}
	if ctx == nil {
		return nil, errors.New("replay subscription context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	replay, err := b.replayLocked(cursor)
	if err != nil {
		b.mu.Unlock()
		return nil, err
	}
	if b.nextID == math.MaxUint64 {
		b.mu.Unlock()
		return nil, errors.New("replay subscriber identifier exhausted")
	}
	b.nextID++
	id := b.nextID
	subscriber := &replaySubscriber{
		events: make(chan controller.Event, b.capacity+1),
		done:   make(chan struct{}),
	}
	for _, event := range replay {
		subscriber.events <- event
	}
	b.subscribers[id] = subscriber
	b.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			b.mu.Lock()
			if active, exists := b.subscribers[id]; exists {
				delete(b.subscribers, id)
				close(active.events)
				close(active.done)
			}
			b.mu.Unlock()
		case <-subscriber.done:
		}
	}()
	return subscriber.events, nil
}

func (b *ReplayBuffer) replayLocked(cursor StreamCursor) ([]controller.Event, error) {
	if cursor.StreamIdentity != b.identity {
		return nil, ErrResyncRequired
	}
	if len(b.events) == 0 {
		if cursor.Sequence != 0 {
			return nil, ErrResyncRequired
		}
		return nil, nil
	}
	oldest := uint64(b.events[0].Sequence)
	latest := uint64(b.events[len(b.events)-1].Sequence)
	if cursor.Sequence > latest {
		return nil, ErrResyncRequired
	}
	if cursor.Sequence == latest {
		return nil, nil
	}
	if cursor.Sequence == math.MaxUint64 || cursor.Sequence+1 < oldest {
		return nil, ErrResyncRequired
	}

	index := 0
	for index < len(b.events) && uint64(b.events[index].Sequence) <= cursor.Sequence {
		index++
	}
	result := make([]controller.Event, len(b.events)-index)
	for i := range result {
		result[i] = cloneControllerEvent(b.events[index+i])
	}
	return result, nil
}

func cloneControllerEvent(source controller.Event) controller.Event {
	result := source
	if source.Progress != nil {
		progress := *source.Progress
		result.Progress = &progress
	}
	if source.Error != nil {
		controllerError := *source.Error
		result.Error = &controllerError
	}
	return result
}

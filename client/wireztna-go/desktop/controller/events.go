package controller

import (
	"context"
	"errors"
	"sync"
)

type eventLog struct {
	mu          sync.Mutex
	capacity    int
	latest      Sequence
	events      []Event
	nextID      uint64
	subscribers map[uint64]chan Event
	done        chan struct{}
	closed      bool
}

func newEventLog(capacity int) *eventLog {
	return &eventLog{
		capacity:    capacity,
		events:      make([]Event, 0, capacity),
		subscribers: make(map[uint64]chan Event),
		done:        make(chan struct{}),
	}
}

func (l *eventLog) publish(event Event) Event {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return event
	}
	l.latest++
	event.Sequence = l.latest
	event = cloneEvent(event)
	if len(l.events) == l.capacity {
		copy(l.events, l.events[1:])
		l.events[len(l.events)-1] = event
	} else {
		l.events = append(l.events, event)
	}

	for id, subscriber := range l.subscribers {
		select {
		case subscriber <- cloneEvent(event):
		default:
			// A slow subscriber cannot stall intent processing. Closing the
			// stream forces it to take a fresh Snapshot and resubscribe.
			delete(l.subscribers, id)
			close(subscriber)
		}
	}
	return event
}

func (l *eventLog) subscribe(ctx context.Context, after Sequence) (<-chan Event, error) {
	if ctx == nil {
		return nil, &Error{Code: ErrorCodeInvalidArgument, Detail: "subscription context is required"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, &Error{Code: ErrorCodeServiceUnavailable, Detail: "controller is closed"}
	}
	if after > l.latest {
		l.mu.Unlock()
		return nil, &Error{Code: ErrorCodeInvalidArgument, Detail: "subscription cursor is ahead of the controller"}
	}
	if len(l.events) > 0 {
		oldest := l.events[0].Sequence
		if after < oldest-1 {
			l.mu.Unlock()
			return nil, &Error{Code: ErrorCodeResyncRequired, Detail: "event cursor is no longer available; take a fresh snapshot"}
		}
	}

	channel := make(chan Event, l.capacity)
	for _, event := range l.events {
		if event.Sequence > after {
			channel <- cloneEvent(event)
		}
	}
	l.nextID++
	id := l.nextID
	l.subscribers[id] = channel
	done := l.done
	l.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			l.mu.Lock()
			if current, ok := l.subscribers[id]; ok && current == channel {
				delete(l.subscribers, id)
				close(channel)
			}
			l.mu.Unlock()
		case <-done:
			// close owns subscriber removal and channel closure. Selecting the
			// log lifetime prevents a Background subscription leaking a watcher.
		}
	}()
	return channel, nil
}

func (l *eventLog) close() {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.done)
		for id, subscriber := range l.subscribers {
			delete(l.subscribers, id)
			close(subscriber)
		}
	}
	l.mu.Unlock()
}

func cloneEvent(event Event) Event {
	if event.Progress != nil {
		progress := *event.Progress
		event.Progress = &progress
	}
	if event.Error != nil {
		structured := *event.Error
		event.Error = &structured
	}
	return event
}

func validateEventCapacity(capacity int) error {
	if capacity < 1 {
		return errors.New("event capacity must be positive")
	}
	return nil
}

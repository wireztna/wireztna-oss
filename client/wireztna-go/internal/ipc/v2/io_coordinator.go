package v2

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
	"time"
)

// connectionCoordinator is the sole owner of deadlines and cancellation
// wakeups for one connection. Scopes may nest; removing a child recomputes the
// earliest surviving parent deadline instead of clearing it blindly.
type connectionCoordinator struct {
	conn    io.ReadWriteCloser
	network net.Conn
	parent  context.Context

	mu      sync.Mutex
	scopes  map[uint64]*connectionScope
	nextID  uint64
	closed  bool
	changed chan struct{}
	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

type connectionScope struct {
	owner    *connectionCoordinator
	id       uint64
	ctx      context.Context
	cancel   context.CancelFunc
	deadline time.Time
	once     sync.Once
}

type coordinatorWaitTarget struct {
	kind uint8
	id   uint64
}

const (
	waitForStop uint8 = iota
	waitForChange
	waitForParent
	waitForScope
)

func newConnectionCoordinator(conn io.ReadWriteCloser, parent context.Context) (*connectionCoordinator, error) {
	if conn == nil || parent == nil {
		return nil, errors.New("connection and parent context are required")
	}
	coordinator := &connectionCoordinator{
		conn:    conn,
		parent:  parent,
		scopes:  make(map[uint64]*connectionScope),
		changed: make(chan struct{}, 1),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	coordinator.network, _ = conn.(net.Conn)
	coordinator.mu.Lock()
	err := coordinator.applyDeadlineLocked()
	coordinator.mu.Unlock()
	if err != nil {
		return nil, err
	}
	go coordinator.watch()
	return coordinator, nil
}

func (c *connectionCoordinator) begin(ctx context.Context, envelopeDeadline time.Time) (*connectionScope, error) {
	if ctx == nil {
		return nil, errors.New("I/O context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	effective := ctx
	cancel := func() {}
	if !envelopeDeadline.IsZero() {
		effective, cancel = context.WithDeadline(ctx, envelopeDeadline)
	}
	if err := effective.Err(); err != nil {
		cancel()
		return nil, err
	}
	deadline, _ := effective.Deadline()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		return nil, errors.New("connection coordinator is closed")
	}
	c.nextID++
	if c.nextID == 0 {
		c.mu.Unlock()
		cancel()
		return nil, errors.New("connection scope identifier exhausted")
	}
	scope := &connectionScope{owner: c, id: c.nextID, ctx: effective, cancel: cancel, deadline: deadline}
	c.scopes[scope.id] = scope
	if err := c.applyDeadlineLocked(); err != nil {
		delete(c.scopes, scope.id)
		c.mu.Unlock()
		cancel()
		return nil, err
	}
	c.mu.Unlock()
	c.notifyChanged()
	return scope, nil
}

func (s *connectionScope) end() {
	if s == nil || s.owner == nil {
		return
	}
	s.once.Do(func() {
		s.owner.mu.Lock()
		delete(s.owner.scopes, s.id)
		if !s.owner.closed {
			_ = s.owner.applyDeadlineLocked()
		}
		s.owner.mu.Unlock()
		s.cancel()
		s.owner.notifyChanged()
	})
}

func (s *connectionScope) normalize(err error) error {
	if err == nil || s == nil {
		return err
	}
	if contextErr := s.ctx.Err(); contextErr != nil {
		return contextErr
	}
	if parentErr := s.owner.parent.Err(); parentErr != nil {
		return parentErr
	}
	if scopeErr := s.owner.canceledScopeError(); scopeErr != nil {
		return scopeErr
	}
	return err
}

func (c *connectionCoordinator) canceledScopeError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, scope := range c.scopes {
		if err := scope.ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (c *connectionCoordinator) applyDeadlineLocked() error {
	if c.network == nil {
		return nil
	}
	deadline, hasDeadline := c.parent.Deadline()
	canceled := c.parent.Err() != nil
	for _, scope := range c.scopes {
		if scope.ctx.Err() != nil {
			canceled = true
		}
		if scope.deadline.IsZero() {
			continue
		}
		if !hasDeadline || scope.deadline.Before(deadline) {
			deadline = scope.deadline
			hasDeadline = true
		}
	}
	if !hasDeadline {
		deadline = time.Time{}
	}
	if canceled {
		deadline = time.Now()
	}
	return c.network.SetDeadline(deadline)
}

func (c *connectionCoordinator) watch() {
	defer close(c.stopped)
	for {
		cases, targets := c.waitCases()
		chosen, _, _ := reflect.Select(cases)
		target := targets[chosen]
		switch target.kind {
		case waitForStop:
			return
		case waitForChange:
			continue
		case waitForParent:
			c.interruptIfActive(0)
		case waitForScope:
			c.interruptIfActive(target.id)
		}
	}
}

func (c *connectionCoordinator) waitCases() ([]reflect.SelectCase, []coordinatorWaitTarget) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cases := []reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.stop)},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.changed)},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.parent.Done())},
	}
	targets := []coordinatorWaitTarget{
		{kind: waitForStop},
		{kind: waitForChange},
		{kind: waitForParent},
	}
	for id, scope := range c.scopes {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(scope.ctx.Done())})
		targets = append(targets, coordinatorWaitTarget{kind: waitForScope, id: id})
	}
	return cases, targets
}

func (c *connectionCoordinator) interruptIfActive(id uint64) {
	c.mu.Lock()
	active := false
	if id == 0 {
		active = c.parent.Err() != nil
	} else if scope, exists := c.scopes[id]; exists {
		active = scope.ctx.Err() != nil
	}
	if active {
		if c.network != nil {
			_ = c.network.SetDeadline(time.Now())
		} else {
			_ = c.conn.Close()
		}
	}
	c.mu.Unlock()
	if active {
		// Avoid a spin on an already-closed context until scope state changes.
		select {
		case <-c.changed:
		case <-c.stop:
		}
	}
}

func (c *connectionCoordinator) notifyChanged() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *connectionCoordinator) close() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		close(c.stop)
		<-c.stopped
		if c.network != nil {
			_ = c.network.SetDeadline(time.Time{})
		}
	})
}

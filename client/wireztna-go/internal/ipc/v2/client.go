package v2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/wireztna/client/desktop/controller"
)

// ClientConfig controls negotiation only; it contains no transport address.
type ClientConfig struct {
	SupportedVersions    []uint32
	Capabilities         []Capability
	RequiredCapabilities []Capability
}

// Client uses one caller-injected connection and one deadline coordinator.
type Client struct {
	conn             io.ReadWriteCloser
	io               *connectionCoordinator
	config           ClientConfig
	mu               sync.Mutex
	hello            *ServerHello
	channel          Channel
	handshakeAttempt bool
	subscribed       bool
}

// NewClient creates an inert endpoint and performs no I/O.
func NewClient(conn io.ReadWriteCloser, config ClientConfig) (*Client, error) {
	if conn == nil {
		return nil, errors.New("client connection is required")
	}
	if len(config.SupportedVersions) == 0 {
		config.SupportedVersions = []uint32{ProtocolVersion}
	} else {
		config.SupportedVersions = append([]uint32(nil), config.SupportedVersions...)
	}
	if len(config.Capabilities) == 0 {
		config.Capabilities = DefaultCapabilities()
	} else {
		config.Capabilities = append([]Capability(nil), config.Capabilities...)
	}
	config.RequiredCapabilities = append([]Capability(nil), config.RequiredCapabilities...)
	probe := ClientHello{
		Kind:                 "hello",
		Channel:              ChannelCommand,
		SupportedVersions:    config.SupportedVersions,
		Capabilities:         config.Capabilities,
		RequiredCapabilities: config.RequiredCapabilities,
	}
	if err := probe.validate(); err != nil {
		return nil, fmt.Errorf("invalid client configuration: %w", err)
	}
	coordinator, err := newConnectionCoordinator(conn, context.Background())
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, io: coordinator, config: config}, nil
}

// Handshake negotiates one command or event channel. It may be attempted once.
func (c *Client) Handshake(ctx context.Context, channel Channel) (ServerHello, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handshakeAttempt {
		return ServerHello{}, errors.New("client handshake already attempted")
	}
	c.handshakeAttempt = true
	if channel != ChannelCommand && channel != ChannelEvent {
		return ServerHello{}, errors.New("invalid client channel")
	}

	scope, err := c.io.begin(ctx, time.Time{})
	if err != nil {
		return ServerHello{}, err
	}
	defer scope.end()
	request := ClientHello{
		Kind:                 "hello",
		Channel:              channel,
		SupportedVersions:    c.config.SupportedVersions,
		Capabilities:         c.config.Capabilities,
		RequiredCapabilities: c.config.RequiredCapabilities,
	}
	if err := request.validate(); err != nil {
		return ServerHello{}, err
	}
	if err := writeJSON(c.conn, MaxCommandFrameSize, request); err != nil {
		return ServerHello{}, fmt.Errorf("write client hello: %w", scope.normalize(err))
	}
	var response ServerHello
	if err := readJSON(c.conn, MaxCommandFrameSize, &response); err != nil {
		return ServerHello{}, fmt.Errorf("read server hello: %w", scope.normalize(err))
	}
	if err := response.validateForClient(request); err != nil {
		return response, err
	}
	c.hello = &response
	c.channel = channel
	return response, nil
}

// Do sends one command-channel request and validates its correlated response.
func (c *Client) Do(ctx context.Context, request Request) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hello == nil || c.channel != ChannelCommand {
		return Response{}, errors.New("client has not negotiated a command channel")
	}
	request.ProtocolVersion = c.hello.NegotiatedVersion
	if err := request.validate(c.hello.NegotiatedVersion); err != nil {
		return Response{}, err
	}
	if request.Command == CommandSubscribe {
		return Response{}, errors.New("subscribe requires a dedicated event channel")
	}
	scope, err := c.io.begin(ctx, request.Deadline)
	if err != nil {
		return Response{}, err
	}
	defer scope.end()

	if err := writeJSON(c.conn, MaxCommandFrameSize, request); err != nil {
		return Response{}, fmt.Errorf("write command request: %w", scope.normalize(err))
	}
	var response Response
	if err := readJSON(c.conn, MaxDataFrameSize, &response); err != nil {
		return Response{}, fmt.Errorf("read command response: %w", scope.normalize(err))
	}
	if err := response.validateForRequest(c.hello.NegotiatedVersion, request); err != nil {
		return Response{}, err
	}
	if response.Error != nil {
		return response, response.Error
	}
	return response, nil
}

// GetReadModel returns the capability-gated wire snapshot including its
// non-secret connection catalog.
func (c *Client) GetReadModel(ctx context.Context, requestID string, deadline time.Time) (Snapshot, StreamCursor, error) {
	response, err := c.Do(ctx, Request{
		RequestID: requestID,
		Deadline:  deadline,
		Command:   CommandGetSnapshot,
		Payload:   []byte("{}"),
	})
	if err != nil {
		return Snapshot{}, StreamCursor{}, err
	}
	if response.Snapshot.StreamIdentity() != c.hello.StreamIdentity() {
		return Snapshot{}, StreamCursor{}, &WireError{Code: ErrorCodeResyncRequired, Detail: "snapshot stream identity changed"}
	}
	if response.Snapshot.Catalog == nil || !containsCapability(c.hello.Capabilities, CapabilityConnectionCatalog) {
		return Snapshot{}, StreamCursor{}, errors.New("connection catalog was not negotiated")
	}
	return *response.Snapshot, response.Snapshot.Cursor(), nil
}

// GetSnapshot executes the legacy controller-state read command.
func (c *Client) GetSnapshot(ctx context.Context, requestID string, deadline time.Time) (controller.Snapshot, StreamCursor, error) {
	response, err := c.Do(ctx, Request{
		RequestID: requestID,
		Deadline:  deadline,
		Command:   CommandGetSnapshot,
		Payload:   []byte("{}"),
	})
	if err != nil {
		return controller.Snapshot{}, StreamCursor{}, err
	}
	if response.Snapshot.StreamIdentity() != c.hello.StreamIdentity() {
		return controller.Snapshot{}, StreamCursor{}, &WireError{Code: ErrorCodeResyncRequired, Detail: "snapshot stream identity changed"}
	}
	snapshot, err := response.Snapshot.Controller()
	if err != nil {
		return controller.Snapshot{}, StreamCursor{}, err
	}
	return snapshot, response.Snapshot.Cursor(), nil
}

func (c *Client) Connect(ctx context.Context, requestID, idempotencyKey string, deadline time.Time, desired DesiredPayload) (string, error) {
	return c.mutate(ctx, requestID, idempotencyKey, deadline, CommandConnect, desired)
}

// ConnectSelection requests a service-stamped connection using only catalog IDs.
func (c *Client) ConnectSelection(ctx context.Context, requestID, idempotencyKey string, deadline time.Time, selection SelectionPayload) (string, error) {
	return c.mutate(ctx, requestID, idempotencyKey, deadline, CommandConnect, selection)
}

func (c *Client) Disconnect(ctx context.Context, requestID, idempotencyKey string, deadline time.Time) (string, error) {
	return c.mutate(ctx, requestID, idempotencyKey, deadline, CommandDisconnect, DisconnectPayload{})
}

func (c *Client) Switch(ctx context.Context, requestID, idempotencyKey string, deadline time.Time, desired DesiredPayload) (string, error) {
	return c.mutate(ctx, requestID, idempotencyKey, deadline, CommandSwitch, desired)
}

// SwitchSelection requests a service-stamped switch using only catalog IDs.
func (c *Client) SwitchSelection(ctx context.Context, requestID, idempotencyKey string, deadline time.Time, selection SelectionPayload) (string, error) {
	return c.mutate(ctx, requestID, idempotencyKey, deadline, CommandSwitch, selection)
}

func (c *Client) mutate(ctx context.Context, requestID, idempotencyKey string, deadline time.Time, command Command, payload any) (string, error) {
	encoded, err := marshalPayload(payload)
	if err != nil {
		return "", err
	}
	response, err := c.Do(ctx, Request{
		RequestID:      requestID,
		IdempotencyKey: idempotencyKey,
		Deadline:       deadline,
		Command:        command,
		Payload:        encoded,
	})
	if err != nil {
		return response.OperationID, err
	}
	return response.OperationID, nil
}

// Subscription owns the retained event-channel lifetime until Close.
type Subscription struct {
	client    *Client
	requestID string
	cursor    StreamCursor
	lifetime  *connectionScope
	mu        sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
}

// Subscribe starts replay strictly after cursor and retains ctx/deadline as the
// lifetime scope. The dedicated event connection remains owned until Close.
func (c *Client) Subscribe(ctx context.Context, requestID string, deadline time.Time, cursor StreamCursor) (*Subscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hello == nil || c.channel != ChannelEvent {
		return nil, errors.New("client has not negotiated an event channel")
	}
	if c.subscribed {
		return nil, errors.New("event channel already has an active subscription")
	}
	if err := cursor.validate(); err != nil {
		return nil, err
	}
	if cursor.StreamIdentity != c.hello.StreamIdentity() {
		return nil, &WireError{Code: ErrorCodeResyncRequired, Detail: "subscription cursor belongs to another stream"}
	}
	payload, err := marshalPayload(SubscribePayload{
		StreamID:      cursor.StreamID,
		Epoch:         cursor.Epoch,
		AfterSequence: cursor.Sequence,
	})
	if err != nil {
		return nil, err
	}
	request := Request{
		ProtocolVersion: c.hello.NegotiatedVersion,
		RequestID:       requestID,
		Deadline:        deadline,
		Command:         CommandSubscribe,
		Payload:         payload,
	}
	if err := request.validate(c.hello.NegotiatedVersion); err != nil {
		return nil, err
	}
	lifetime, err := c.io.begin(ctx, deadline)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			lifetime.end()
		}
	}()
	if err := writeJSON(c.conn, MaxCommandFrameSize, request); err != nil {
		return nil, lifetime.normalize(err)
	}
	var response Response
	if err := readJSON(c.conn, MaxDataFrameSize, &response); err != nil {
		return nil, lifetime.normalize(err)
	}
	if err := response.validateForRequest(c.hello.NegotiatedVersion, request); err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, response.Error
	}
	c.subscribed = true
	failed = false
	return &Subscription{
		client:    c,
		requestID: requestID,
		cursor:    cursor,
		lifetime:  lifetime,
		done:      make(chan struct{}),
	}, nil
}

// Next reads and validates exactly one contiguous event frame.
func (s *Subscription) Next(ctx context.Context) (controller.Event, error) {
	if s == nil || s.client == nil {
		return controller.Event{}, errors.New("subscription is nil")
	}
	select {
	case <-s.done:
		return controller.Event{}, errors.New("subscription is closed")
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope, err := s.client.io.begin(ctx, time.Time{})
	if err != nil {
		return controller.Event{}, err
	}
	defer scope.end()

	var envelope EventEnvelope
	if err := readJSON(s.client.conn, MaxDataFrameSize, &envelope); err != nil {
		return controller.Event{}, scope.normalize(err)
	}
	if err := envelope.validate(s.client.hello.NegotiatedVersion, s.requestID, s.cursor.StreamIdentity); err != nil {
		return controller.Event{}, err
	}
	event, err := envelope.Event.Controller()
	if err != nil {
		return controller.Event{}, err
	}
	if s.cursor.Sequence == ^uint64(0) || uint64(event.Sequence) != s.cursor.Sequence+1 {
		return controller.Event{}, &WireError{Code: ErrorCodeResyncRequired, Detail: "event sequence is not contiguous"}
	}
	s.cursor.Sequence = uint64(event.Sequence)
	return event, nil
}

// Close terminates the retained subscription lifetime and its dedicated connection.
func (s *Subscription) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		close(s.done)
		s.lifetime.end()
		closeErr = s.client.Close()
	})
	return closeErr
}

// Close closes only the caller-supplied connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	c.io.close()
	return c.conn.Close()
}

func marshalPayload(payload any) ([]byte, error) {
	encoded, err := jsonMarshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request payload: %w", err)
	}
	return encoded, nil
}

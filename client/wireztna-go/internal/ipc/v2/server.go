package v2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/wireztna/client/desktop/controller"
)

// Controller is the platform-neutral state and event source consumed by IPC v2.
type Controller interface {
	Execute(context.Context, controller.Command) (controller.OperationID, error)
	Snapshot(context.Context) (controller.Snapshot, error)
	Subscribe(context.Context, controller.Sequence) (<-chan controller.Event, error)
}

// ServerConfig contains no transport address or activation behavior.
type ServerConfig struct {
	ServiceVersion        string
	Capabilities          []Capability
	ExpectedOwnerIdentity ExpectedOwnerIdentity
	StreamIdentity        StreamIdentity
	CatalogProvider       CatalogProvider
	Now                   func() time.Time
}

// Server serves only explicitly injected, already-authenticated connections.
type Server struct {
	controller     Controller
	authorizer     *Authorizer
	idempotency    *IdempotencyStore
	config         ServerConfig
	generationMu   sync.Mutex
	lastGeneration controller.Generation
}

func NewServer(backend Controller, authorizer *Authorizer, idempotency *IdempotencyStore, config ServerConfig) (*Server, error) {
	if backend == nil {
		return nil, errors.New("controller backend is required")
	}
	if authorizer == nil {
		return nil, errors.New("authorizer is required")
	}
	if idempotency == nil {
		return nil, errors.New("idempotency store is required")
	}
	if config.ServiceVersion == "" {
		return nil, errors.New("service version is required")
	}
	if _, err := identityKey(config.ExpectedOwnerIdentity); err != nil {
		return nil, fmt.Errorf("invalid expected owner identity: %w", err)
	}
	if err := config.StreamIdentity.validate(); err != nil {
		return nil, fmt.Errorf("invalid server stream identity: %w", err)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if len(config.Capabilities) == 0 {
		config.Capabilities = DefaultCapabilities()
	} else {
		config.Capabilities = append([]Capability(nil), config.Capabilities...)
	}
	if config.CatalogProvider != nil && !containsCapability(config.Capabilities, CapabilityConnectionCatalog) {
		config.Capabilities = append(config.Capabilities, CapabilityConnectionCatalog)
	}
	if config.CatalogProvider == nil && containsCapability(config.Capabilities, CapabilityConnectionCatalog) {
		return nil, errors.New("connection_catalog capability requires a catalog provider")
	}
	if hasDuplicateCapabilities(config.Capabilities) {
		return nil, errors.New("server capabilities are unknown, empty, or duplicated")
	}
	return &Server{controller: backend, authorizer: authorizer, idempotency: idempotency, config: config}, nil
}

// Serve accepts only opaque authentication produced by a future build-tagged
// adapter in this same package. The current transport-neutral package exposes no
// way for external callers to manufacture that value.
func (s *Server) Serve(ctx context.Context, conn io.ReadWriteCloser, authentication peerAuthentication) (returnErr error) {
	defer func() {
		if recover() != nil {
			returnErr = errors.New("IPC server recovered from an internal failure")
		}
	}()
	if ctx == nil || conn == nil || authentication == nil {
		return errors.New("server context, connection, and peer authentication are required")
	}
	peer := authentication.authenticatedPeerIdentity()
	if _, err := identityKey(peer); err != nil {
		return err
	}
	coordinator, err := newConnectionCoordinator(conn, ctx)
	if err != nil {
		return err
	}
	defer coordinator.close()

	var hello ClientHello
	if err := readJSON(conn, MaxCommandFrameSize, &hello); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("read client hello: %w", err)
	}
	if err := hello.validate(); err != nil {
		return fmt.Errorf("validate client hello: %w", err)
	}
	serverHello, err := s.negotiate(hello)
	if writeErr := writeJSON(conn, MaxCommandFrameSize, serverHello); writeErr != nil {
		return fmt.Errorf("write server hello: %w", writeErr)
	}
	if err != nil {
		return err
	}
	if hello.Channel == ChannelEvent {
		return s.serveEventChannel(ctx, coordinator, peer, serverHello.NegotiatedVersion)
	}
	return s.serveCommandChannel(ctx, coordinator, peer, serverHello.NegotiatedVersion, serverHello.Capabilities)
}

func (s *Server) negotiate(client ClientHello) (ServerHello, error) {
	response := ServerHello{Kind: "hello", ServiceVersion: s.config.ServiceVersion}
	if !containsVersion(client.SupportedVersions, ProtocolVersion) {
		response.Error = &WireError{Code: ErrorCodeUnsupportedVersion, Detail: "protocol v2 is required"}
		return response, response.Error
	}
	capabilities := intersectCapabilities(s.config.Capabilities, client.Capabilities)
	for _, required := range client.RequiredCapabilities {
		if !containsCapability(capabilities, required) {
			response.Error = &WireError{Code: ErrorCodeUnsupportedVersion, Detail: "a required capability is unavailable"}
			return response, response.Error
		}
	}
	response.NegotiatedVersion = ProtocolVersion
	response.Capabilities = capabilities
	response.Compatibility = CompatibilityCurrent
	response.StreamID = s.config.StreamIdentity.StreamID
	response.Epoch = s.config.StreamIdentity.Epoch
	return response, nil
}

func (s *Server) serveCommandChannel(ctx context.Context, coordinator *connectionCoordinator, peer PeerIdentity, version uint32, capabilities []Capability) error {
	for {
		var request Request
		if err := readJSON(coordinator.conn, MaxCommandFrameSize, &request); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read command request: %w", err)
		}
		if request.RequestID == "" {
			return errors.New("command request omitted request_id")
		}
		response := s.handleRequest(ctx, peer, ChannelCommand, version, capabilities, request)
		if err := response.validateForRequest(version, request); err != nil {
			return fmt.Errorf("refuse invalid command response: %w", err)
		}
		if err := writeJSON(coordinator.conn, MaxDataFrameSize, response); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("write command response: %w", err)
		}
	}
}

func (s *Server) serveEventChannel(ctx context.Context, coordinator *connectionCoordinator, peer PeerIdentity, version uint32) (returnErr error) {
	var request Request
	accepted := false
	defer func() {
		if recover() != nil {
			if accepted {
				_ = s.writeEventTerminal(coordinator.conn, version, request.RequestID, ErrorCodeServiceUnavailable, "event stream terminated")
			}
			returnErr = errors.New("IPC event channel recovered from an internal failure")
		}
	}()
	if err := readJSON(coordinator.conn, MaxCommandFrameSize, &request); err != nil {
		return fmt.Errorf("read subscription request: %w", err)
	}
	if request.RequestID == "" {
		return errors.New("subscription request omitted request_id")
	}
	if err := request.validate(version); err != nil {
		return s.writeEventRejection(coordinator.conn, version, request, ErrorCodeInvalidArgument, err.Error())
	}
	if request.Command != CommandSubscribe {
		return s.writeEventRejection(coordinator.conn, version, request, ErrorCodeInvalidArgument, "event channel accepts only subscribe")
	}
	if _, err := s.authorizer.Authorize(peer, request.Command); err != nil {
		return s.writeEventError(coordinator.conn, version, request, err)
	}
	if !request.Deadline.After(s.config.Now()) {
		return s.writeEventRejection(coordinator.conn, version, request, ErrorCodeDeadlineExceeded, "subscription deadline has expired")
	}
	payload, _, err := decodePayload[SubscribePayload](request.Payload)
	if err != nil {
		return s.writeEventRejection(coordinator.conn, version, request, ErrorCodeInvalidArgument, err.Error())
	}
	cursor := payload.Cursor()
	if err := cursor.validate(); err != nil {
		return s.writeEventRejection(coordinator.conn, version, request, ErrorCodeInvalidArgument, err.Error())
	}
	if cursor.StreamIdentity != s.config.StreamIdentity {
		return s.writeEventRejection(coordinator.conn, version, request, ErrorCodeResyncRequired, "subscription cursor belongs to another stream")
	}

	subscriptionContext, cancel := context.WithDeadline(ctx, request.Deadline)
	defer cancel()
	lifetime, err := coordinator.begin(subscriptionContext, time.Time{})
	if err != nil {
		return err
	}
	defer lifetime.end()
	events, err := s.controller.Subscribe(subscriptionContext, controller.Sequence(cursor.Sequence))
	if err != nil {
		return s.writeEventError(coordinator.conn, version, request, err)
	}
	acceptedResponse := Response{ProtocolVersion: version, RequestID: request.RequestID, Success: true}
	if err := acceptedResponse.validateForRequest(version, request); err != nil {
		return err
	}
	if err := writeJSON(coordinator.conn, MaxDataFrameSize, acceptedResponse); err != nil {
		return lifetime.normalize(err)
	}
	if subscribed, ok := coordinator.conn.(interface{ markEventSubscribed() }); ok {
		subscribed.markEventSubscribed()
	}
	accepted = true

	lastSequence := cursor.Sequence
	for {
		select {
		case <-subscriptionContext.Done():
			return subscriptionContext.Err()
		case event, ok := <-events:
			if !ok {
				if subscriptionContext.Err() != nil {
					return subscriptionContext.Err()
				}
				_ = s.writeEventTerminal(coordinator.conn, version, request.RequestID, ErrorCodeResyncRequired, "event source closed; resync required")
				return ErrResyncRequired
			}
			if lastSequence == math.MaxUint64 || uint64(event.Sequence) != lastSequence+1 {
				_ = s.writeEventTerminal(coordinator.conn, version, request.RequestID, ErrorCodeResyncRequired, "event sequence is not contiguous")
				return ErrResyncRequired
			}
			wireEvent, err := EventFromController(event)
			if err != nil {
				_ = s.writeEventTerminal(coordinator.conn, version, request.RequestID, ErrorCodeServiceUnavailable, "event conversion failed")
				return err
			}
			envelope := EventEnvelope{
				ProtocolVersion: version,
				RequestID:       request.RequestID,
				StreamID:        s.config.StreamIdentity.StreamID,
				Epoch:           s.config.StreamIdentity.Epoch,
				Event:           &wireEvent,
			}
			if err := writeJSON(coordinator.conn, MaxDataFrameSize, envelope); err != nil {
				return lifetime.normalize(err)
			}
			lastSequence = uint64(event.Sequence)
		}
	}
}

func (s *Server) handleRequest(ctx context.Context, peer PeerIdentity, channel Channel, version uint32, capabilities []Capability, request Request) (response Response) {
	response = Response{ProtocolVersion: version, RequestID: request.RequestID, IdempotencyKey: request.IdempotencyKey}
	defer func() {
		if recover() != nil {
			response = Response{
				ProtocolVersion: version,
				RequestID:       request.RequestID,
				IdempotencyKey:  request.IdempotencyKey,
				Error:           &WireError{Code: ErrorCodeServiceUnavailable, Detail: "request processing failed"},
			}
		}
	}()
	if err := request.validate(version); err != nil {
		response.Error = &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
		return response
	}
	if request.Command == CommandSubscribe || channel != ChannelCommand {
		response.Error = &WireError{Code: ErrorCodeInvalidArgument, Detail: "command is not valid on this channel"}
		return response
	}
	if _, err := s.authorizer.Authorize(peer, request.Command); err != nil {
		response.Error = wireErrorFromError(err)
		return response
	}
	if !request.Deadline.After(s.config.Now()) {
		response.Error = &WireError{Code: ErrorCodeDeadlineExceeded, Detail: "request deadline has expired"}
		return response
	}

	requestContext, cancel := context.WithDeadline(ctx, request.Deadline)
	defer cancel()
	if request.Command == CommandGetSnapshot {
		if _, _, err := decodePayload[GetSnapshotPayload](request.Payload); err != nil {
			response.Error = &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
			return response
		}
		snapshot, err := s.snapshot(requestContext, containsCapability(capabilities, CapabilityConnectionCatalog))
		if err != nil {
			response.Error = wireErrorFromError(err)
			return response
		}
		response.Success = true
		response.Snapshot = &snapshot
		return response
	}

	canonicalPayload, err := s.canonicalPayload(request, capabilities)
	if err != nil {
		response.Error = wireErrorFromError(err)
		return response
	}
	result, err := s.idempotency.Execute(requestContext, s.config.ExpectedOwnerIdentity, request.Command, request.IdempotencyKey, canonicalPayload, func(callContext context.Context) IdempotencyResult {
		if callErr := callContext.Err(); callErr != nil {
			return IdempotencyResult{Error: wireErrorFromError(callErr)}
		}
		command, commandErr := s.controllerCommand(callContext, request, capabilities)
		if commandErr != nil {
			return IdempotencyResult{Error: wireErrorFromError(commandErr)}
		}
		operationID, executeErr := s.controller.Execute(callContext, command)
		if executeErr != nil {
			return IdempotencyResult{OperationID: string(operationID), Error: wireErrorFromError(executeErr)}
		}
		if operationID == "" {
			return IdempotencyResult{Error: &WireError{Code: ErrorCodeServiceUnavailable, Detail: "controller omitted operation identifier"}}
		}
		return IdempotencyResult{OperationID: string(operationID)}
	})
	if err != nil {
		response.Error = wireErrorFromError(err)
		return response
	}
	response.OperationID = result.OperationID
	response.Error = result.Error
	response.Success = result.Error == nil
	return response
}

func (s *Server) canonicalPayload(request Request, capabilities []Capability) ([]byte, error) {
	switch request.Command {
	case CommandConnect, CommandSwitch:
		if s.config.CatalogProvider != nil {
			selection, err := selectionFromRequest(request, capabilities)
			if err != nil {
				return nil, err
			}
			return marshalPayload(selection)
		}
		payload, canonical, err := decodePayload[DesiredPayload](request.Payload)
		if err != nil {
			return nil, &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
		}
		if err := payload.validate(); err != nil {
			return nil, &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
		}
		return canonical, nil
	case CommandDisconnect:
		_, canonical, err := decodePayload[DisconnectPayload](request.Payload)
		if err != nil {
			return nil, &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
		}
		return canonical, nil
	default:
		return nil, &WireError{Code: ErrorCodeInvalidArgument, Detail: "unsupported mutation command"}
	}
}

func selectionFromRequest(request Request, capabilities []Capability) (SelectionPayload, error) {
	if containsCapability(capabilities, CapabilityConnectionCatalog) {
		payload, _, err := decodePayload[SelectionPayload](request.Payload)
		if err != nil {
			return SelectionPayload{}, &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
		}
		if err := payload.validate(); err != nil {
			return SelectionPayload{}, &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
		}
		return payload, nil
	}
	payload, _, err := decodePayload[DesiredPayload](request.Payload)
	if err != nil {
		return SelectionPayload{}, &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
	}
	if err := payload.validate(); err != nil {
		return SelectionPayload{}, &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
	}
	return SelectionPayload{GroupID: payload.GroupID, ExitNodeID: payload.ExitNodeID}, nil
}

func (s *Server) controllerCommand(ctx context.Context, request Request, capabilities []Capability) (controller.Command, error) {
	switch request.Command {
	case CommandConnect, CommandSwitch:
		kind := controller.CommandConnect
		if request.Command == CommandSwitch {
			kind = controller.CommandSwitch
		}
		if s.config.CatalogProvider != nil {
			selection, err := selectionFromRequest(request, capabilities)
			if err != nil {
				return controller.Command{}, err
			}
			catalog, err := s.config.CatalogProvider.ConnectionCatalog(ctx)
			if err != nil {
				return controller.Command{}, err
			}
			if err := catalog.validate(); err != nil {
				return controller.Command{}, errors.New("catalog provider returned an invalid catalog")
			}
			if err := catalog.validateSelection(selection); err != nil {
				return controller.Command{}, &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
			}
			snapshot, err := s.controller.Snapshot(ctx)
			if err != nil {
				return controller.Command{}, err
			}
			generation, err := s.allocateGeneration(snapshot)
			if err != nil {
				return controller.Command{}, err
			}
			return controller.Command{
				Kind: kind,
				Desired: controller.DesiredState{
					ConfigurationID: controller.ConfigurationID(catalog.ConfigurationID),
					Generation:      generation,
					GroupID:         controller.GroupID(selection.GroupID),
					ExitNodeID:      controller.ExitNodeID(selection.ExitNodeID),
					Connected:       true,
				},
			}, nil
		}

		payload, _, err := decodePayload[DesiredPayload](request.Payload)
		if err != nil {
			return controller.Command{}, &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
		}
		if err := payload.validate(); err != nil {
			return controller.Command{}, &WireError{Code: ErrorCodeInvalidArgument, Detail: err.Error()}
		}
		return controller.Command{
			Kind: kind,
			Desired: controller.DesiredState{
				ConfigurationID: controller.ConfigurationID(payload.ConfigurationID),
				Generation:      controller.Generation(payload.Generation),
				GroupID:         controller.GroupID(payload.GroupID),
				ExitNodeID:      controller.ExitNodeID(payload.ExitNodeID),
				Connected:       true,
			},
		}, nil
	case CommandDisconnect:
		snapshot, err := s.controller.Snapshot(ctx)
		if err != nil {
			return controller.Command{}, err
		}
		desired := snapshot.Desired
		desired.Connected = false
		return controller.Command{Kind: controller.CommandDisconnect, Desired: desired}, nil
	default:
		return controller.Command{}, &WireError{Code: ErrorCodeInvalidArgument, Detail: "unsupported mutation command"}
	}
}

func (s *Server) allocateGeneration(snapshot controller.Snapshot) (controller.Generation, error) {
	highWater := snapshot.Desired.Generation
	if snapshot.Applied.Generation > highWater {
		highWater = snapshot.Applied.Generation
	}
	s.generationMu.Lock()
	defer s.generationMu.Unlock()
	if s.lastGeneration > highWater {
		highWater = s.lastGeneration
	}
	if uint64(highWater) == math.MaxUint64 {
		return 0, &WireError{Code: ErrorCodeConflict, Detail: "configuration generation is exhausted"}
	}
	s.lastGeneration = highWater + 1
	return s.lastGeneration, nil
}

func (s *Server) snapshot(ctx context.Context, includeCatalog bool) (Snapshot, error) {
	source, err := s.controller.Snapshot(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	result, err := SnapshotFromController(source, s.config.StreamIdentity)
	if err != nil {
		return Snapshot{}, err
	}
	if s.config.CatalogProvider == nil || !includeCatalog {
		return result, nil
	}
	catalog, err := s.config.CatalogProvider.ConnectionCatalog(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if err := catalog.validate(); err != nil {
		return Snapshot{}, errors.New("catalog provider returned an invalid catalog")
	}
	if pristineSnapshot(source) {
		result.Desired.ConfigurationID = catalog.ConfigurationID
		// This is a non-authoritative UI seed only. Mutation generations are
		// allocated under generationMu when a command is accepted, so a read must
		// not race against or expose the allocator high-water mark.
		result.Desired.Generation = 1
		if len(catalog.Projects) == 1 {
			result.Desired.GroupID = catalog.Projects[0].GroupID
		}
	}
	if includeCatalog {
		result.Catalog = &catalog
	}
	return result, nil
}

func (s *Server) writeEventRejection(conn io.Writer, version uint32, request Request, code ErrorCode, detail string) error {
	response := Response{
		ProtocolVersion: version,
		RequestID:       request.RequestID,
		IdempotencyKey:  request.IdempotencyKey,
		Error:           &WireError{Code: code, Detail: detail},
	}
	if err := response.validateForRequest(version, request); err != nil {
		return err
	}
	return writeJSON(conn, MaxDataFrameSize, response)
}

func (s *Server) writeEventError(conn io.Writer, version uint32, request Request, err error) error {
	wireError := wireErrorFromError(err)
	return s.writeEventRejection(conn, version, request, wireError.Code, wireError.Detail)
}

func (s *Server) writeEventTerminal(conn io.Writer, version uint32, requestID string, code ErrorCode, detail string) error {
	wireError := &WireError{Code: code, Detail: detail}
	if err := wireError.validate(); err != nil {
		return err
	}
	return writeJSON(conn, MaxDataFrameSize, EventEnvelope{
		ProtocolVersion: version,
		RequestID:       requestID,
		StreamID:        s.config.StreamIdentity.StreamID,
		Epoch:           s.config.StreamIdentity.Epoch,
		Error:           wireError,
	})
}

func wireErrorFromError(err error) *WireError {
	if err == nil {
		return nil
	}
	var wireError *WireError
	if errors.As(err, &wireError) {
		copy := *wireError
		if copy.validate() == nil {
			return &copy
		}
		return &WireError{Code: ErrorCodeServiceUnavailable, Detail: "operation failed"}
	}
	if errors.Is(err, context.Canceled) {
		return &WireError{Code: ErrorCodeCanceled, Detail: "operation canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &WireError{Code: ErrorCodeDeadlineExceeded, Detail: "operation deadline exceeded"}
	}
	if errors.Is(err, ErrIdempotencyConflict) {
		return &WireError{Code: ErrorCodeConflict, Detail: err.Error()}
	}
	if errors.Is(err, ErrIdempotencyCapacity) {
		return &WireError{Code: ErrorCodeServiceUnavailable, Detail: "idempotency capacity unavailable"}
	}
	if errors.Is(err, ErrResyncRequired) {
		return &WireError{Code: ErrorCodeResyncRequired, Detail: ErrResyncRequired.Error()}
	}
	if structured, ok := controller.AsError(err); ok {
		mapped := wireErrorFromController(structured)
		if mapped != nil && mapped.validate() == nil {
			return mapped
		}
	}
	return &WireError{Code: ErrorCodeServiceUnavailable, Detail: "controller operation failed"}
}

func intersectCapabilities(service, client []Capability) []Capability {
	result := make([]Capability, 0, len(service))
	for _, capability := range service {
		if containsCapability(client, capability) {
			result = append(result, capability)
		}
	}
	return result
}

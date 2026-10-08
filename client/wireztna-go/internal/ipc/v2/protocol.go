// Package v2 defines the inert, transport-independent WireZTNA desktop IPC v2
// protocol. It contains no listener, path discovery, legacy codec, or quit command.
package v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"
)

const (
	// ProtocolVersion is the only protocol version implemented by this codec.
	ProtocolVersion uint32 = 2

	MaxCommandFrameSize uint32 = 64 * 1024
	MaxDataFrameSize    uint32 = 256 * 1024
)

type Channel string

const (
	ChannelCommand Channel = "command"
	ChannelEvent   Channel = "event"
)

type Compatibility string

const CompatibilityCurrent Compatibility = "current"

type Capability string

const (
	CapabilityStrictFraming       Capability = "strict_framing"
	CapabilityControllerState     Capability = "controller_state"
	CapabilityIdempotency         Capability = "idempotency"
	CapabilityEventReplay         Capability = "event_replay"
	CapabilityAuthorizationPolicy Capability = "authorization_policy"
	// CapabilityConnectionCatalog opts into Snapshot.catalog and selection-only
	// connect/switch payloads whose configuration identity and generation are
	// stamped authoritatively by the service.
	CapabilityConnectionCatalog Capability = "connection_catalog"
)

var defaultCapabilities = []Capability{
	CapabilityStrictFraming,
	CapabilityControllerState,
	CapabilityIdempotency,
	CapabilityEventReplay,
	CapabilityAuthorizationPolicy,
}

var knownCapabilities = append(append([]Capability(nil), defaultCapabilities...), CapabilityConnectionCatalog)

func DefaultCapabilities() []Capability { return append([]Capability(nil), defaultCapabilities...) }

type Command string

const (
	CommandConnect     Command = "connect"
	CommandDisconnect  Command = "disconnect"
	CommandSwitch      Command = "switch"
	CommandGetSnapshot Command = "get_snapshot"
	CommandSubscribe   Command = "subscribe"
)

type CommandClass string

const (
	CommandClassRead         CommandClass = "read"
	CommandClassOwnerControl CommandClass = "owner_control"
	CommandClassAdmin        CommandClass = "admin"
)

type ErrorCode string

const (
	ErrorCodeUnauthorized       ErrorCode = "UNAUTHORIZED"
	ErrorCodeUnsupportedVersion ErrorCode = "UNSUPPORTED_VERSION"
	ErrorCodeInvalidArgument    ErrorCode = "INVALID_ARGUMENT"
	ErrorCodeDeadlineExceeded   ErrorCode = "DEADLINE_EXCEEDED"
	ErrorCodeCanceled           ErrorCode = "CANCELED"
	ErrorCodeConflict           ErrorCode = "CONFLICT"
	ErrorCodeServiceUnavailable ErrorCode = "SERVICE_UNAVAILABLE"
	ErrorCodeDegraded           ErrorCode = "DEGRADED"
	ErrorCodeReauthRequired     ErrorCode = "REAUTH_REQUIRED"
	ErrorCodeResyncRequired     ErrorCode = "RESYNC_REQUIRED"
)

type WireError struct {
	Code   ErrorCode `json:"code"`
	Detail string    `json:"detail,omitempty"`
}

func (e *WireError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Detail == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

func (e WireError) validate() error {
	switch e.Code {
	case ErrorCodeUnauthorized, ErrorCodeUnsupportedVersion, ErrorCodeInvalidArgument,
		ErrorCodeDeadlineExceeded, ErrorCodeCanceled, ErrorCodeConflict,
		ErrorCodeServiceUnavailable, ErrorCodeDegraded, ErrorCodeReauthRequired,
		ErrorCodeResyncRequired:
		return nil
	default:
		return fmt.Errorf("unknown error code %q", e.Code)
	}
}

type StreamIdentity struct {
	StreamID string `json:"stream_id"`
	Epoch    uint64 `json:"epoch"`
}

func (i StreamIdentity) validate() error {
	if i.StreamID == "" || i.Epoch == 0 {
		return errors.New("stream_id and epoch are required")
	}
	return nil
}

type StreamCursor struct {
	StreamIdentity
	Sequence uint64 `json:"sequence"`
}

func (c StreamCursor) validate() error { return c.StreamIdentity.validate() }

type ClientHello struct {
	Kind                 string       `json:"kind"`
	Channel              Channel      `json:"channel"`
	SupportedVersions    []uint32     `json:"supported_versions"`
	Capabilities         []Capability `json:"capabilities"`
	RequiredCapabilities []Capability `json:"required_capabilities,omitempty"`
}

type ServerHello struct {
	Kind              string        `json:"kind"`
	NegotiatedVersion uint32        `json:"negotiated_version"`
	ServiceVersion    string        `json:"service_version"`
	Capabilities      []Capability  `json:"capabilities"`
	Compatibility     Compatibility `json:"compatibility,omitempty"`
	StreamID          string        `json:"stream_id,omitempty"`
	Epoch             uint64        `json:"epoch,omitempty"`
	Error             *WireError    `json:"error,omitempty"`
}

func (h ServerHello) StreamIdentity() StreamIdentity {
	return StreamIdentity{StreamID: h.StreamID, Epoch: h.Epoch}
}

type Request struct {
	ProtocolVersion uint32          `json:"protocol_version"`
	RequestID       string          `json:"request_id"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Deadline        time.Time       `json:"deadline"`
	Command         Command         `json:"command"`
	Payload         json.RawMessage `json:"payload"`
}

type Response struct {
	ProtocolVersion uint32     `json:"protocol_version"`
	RequestID       string     `json:"request_id"`
	IdempotencyKey  string     `json:"idempotency_key"`
	OperationID     string     `json:"operation_id"`
	Success         bool       `json:"success"`
	Snapshot        *Snapshot  `json:"snapshot,omitempty"`
	Error           *WireError `json:"error,omitempty"`
}

// EventEnvelope contains either one event or one explicit terminal stream error.
type EventEnvelope struct {
	ProtocolVersion uint32     `json:"protocol_version"`
	RequestID       string     `json:"request_id"`
	StreamID        string     `json:"stream_id"`
	Epoch           uint64     `json:"epoch"`
	Event           *Event     `json:"event,omitempty"`
	Error           *WireError `json:"error,omitempty"`
}

type DesiredPayload struct {
	ConfigurationID string `json:"configuration_id"`
	Generation      uint64 `json:"generation"`
	GroupID         string `json:"group_id"`
	ExitNodeID      string `json:"exit_node_id,omitempty"`
}

func (p DesiredPayload) validate() error {
	if p.ConfigurationID == "" || p.Generation == 0 || p.GroupID == "" {
		return errors.New("configuration_id, generation, and group_id are required")
	}
	return nil
}

type DisconnectPayload struct{}
type GetSnapshotPayload struct{}

type SubscribePayload struct {
	StreamID      string `json:"stream_id"`
	Epoch         uint64 `json:"epoch"`
	AfterSequence uint64 `json:"after_sequence"`
}

func (p SubscribePayload) Cursor() StreamCursor {
	return StreamCursor{StreamIdentity: StreamIdentity{StreamID: p.StreamID, Epoch: p.Epoch}, Sequence: p.AfterSequence}
}

func (h ClientHello) validate() error {
	if h.Kind != "hello" {
		return errors.New("hello kind must be hello")
	}
	if h.Channel != ChannelCommand && h.Channel != ChannelEvent {
		return errors.New("hello channel must be command or event")
	}
	if len(h.SupportedVersions) != 1 || h.SupportedVersions[0] != ProtocolVersion {
		return errors.New("hello must offer exactly protocol v2")
	}
	if hasDuplicateCapabilities(h.Capabilities) || hasDuplicateCapabilities(h.RequiredCapabilities) {
		return errors.New("hello capabilities must be known, non-empty, and unique")
	}
	for _, required := range h.RequiredCapabilities {
		if !containsCapability(h.Capabilities, required) {
			return errors.New("required capabilities must be a subset of offered capabilities")
		}
	}
	return nil
}

func (h ServerHello) validateForClient(client ClientHello) error {
	if h.Kind != "hello" {
		return errors.New("hello kind must be hello")
	}
	if h.ServiceVersion == "" {
		return errors.New("server omitted service_version")
	}
	if h.Error != nil {
		if err := h.Error.validate(); err != nil {
			return err
		}
		if h.NegotiatedVersion != 0 || h.Compatibility != "" || len(h.Capabilities) != 0 || h.StreamID != "" || h.Epoch != 0 {
			return errors.New("failed server hello contains negotiated protocol state")
		}
		return h.Error
	}
	if h.NegotiatedVersion != ProtocolVersion || h.Compatibility != CompatibilityCurrent {
		return errors.New("server hello is not the exact current protocol")
	}
	if !containsVersion(client.SupportedVersions, h.NegotiatedVersion) {
		return errors.New("server negotiated a version the client did not offer")
	}
	if hasDuplicateCapabilities(h.Capabilities) {
		return errors.New("server capabilities must be known, non-empty, and unique")
	}
	for _, capability := range h.Capabilities {
		if !containsCapability(client.Capabilities, capability) {
			return errors.New("server capabilities are not a subset of the client offer")
		}
	}
	for _, required := range client.RequiredCapabilities {
		if !containsCapability(h.Capabilities, required) {
			return errors.New("server omitted a required capability")
		}
	}
	return h.StreamIdentity().validate()
}

func (r Request) validate(version uint32) error {
	if r.ProtocolVersion != version || version != ProtocolVersion {
		return errors.New("request protocol_version does not match protocol v2")
	}
	if r.RequestID == "" {
		return errors.New("request_id is required")
	}
	if r.Deadline.IsZero() {
		return errors.New("deadline is required")
	}
	if !r.Command.valid() {
		return errors.New("unknown command")
	}
	if r.Command.mutates() && r.IdempotencyKey == "" {
		return errors.New("idempotency_key is required for mutation commands")
	}
	if !r.Command.mutates() && r.IdempotencyKey != "" {
		return errors.New("idempotency_key is not accepted for read commands")
	}
	if len(r.Payload) == 0 {
		return errors.New("payload is required")
	}
	return nil
}

func (r Response) validate(version uint32) error {
	if r.ProtocolVersion != version || version != ProtocolVersion || r.RequestID == "" {
		return errors.New("response envelope does not match protocol v2")
	}
	if r.Success == (r.Error != nil) {
		return errors.New("response must contain exactly one success or error result")
	}
	if r.Error != nil {
		return r.Error.validate()
	}
	return nil
}

func (r Response) validateForRequest(version uint32, request Request) error {
	if err := r.validate(version); err != nil {
		return err
	}
	if r.RequestID != request.RequestID || r.IdempotencyKey != request.IdempotencyKey {
		return errors.New("response correlation fields do not match request")
	}
	if !request.Command.valid() {
		return nil
	}
	if r.Error != nil {
		if r.Snapshot != nil || (!request.Command.mutates() && r.OperationID != "") {
			return errors.New("error response contains fields invalid for its command")
		}
		return nil
	}
	switch request.Command {
	case CommandConnect, CommandDisconnect, CommandSwitch:
		if r.OperationID == "" || r.Snapshot != nil {
			return errors.New("mutation success requires only operation_id")
		}
	case CommandGetSnapshot:
		if r.OperationID != "" || r.Snapshot == nil {
			return errors.New("get_snapshot success requires only snapshot")
		}
		if err := r.Snapshot.validateReadModel(); err != nil {
			return err
		}
	case CommandSubscribe:
		if r.OperationID != "" || r.Snapshot != nil || r.IdempotencyKey != "" {
			return errors.New("subscribe success contains invalid result fields")
		}
	}
	return nil
}

func (e EventEnvelope) validate(version uint32, requestID string, identity StreamIdentity) error {
	if e.ProtocolVersion != version || version != ProtocolVersion || e.RequestID != requestID {
		return errors.New("event envelope does not match the subscription")
	}
	if e.StreamID != identity.StreamID || e.Epoch != identity.Epoch {
		return &WireError{Code: ErrorCodeResyncRequired, Detail: "event stream identity changed"}
	}
	if (e.Event == nil) == (e.Error == nil) {
		return errors.New("event envelope must contain exactly one event or terminal error")
	}
	if e.Error != nil {
		if err := e.Error.validate(); err != nil {
			return err
		}
		return e.Error
	}
	return e.Event.validate()
}

func (c Command) valid() bool {
	switch c {
	case CommandConnect, CommandDisconnect, CommandSwitch, CommandGetSnapshot, CommandSubscribe:
		return true
	default:
		return false
	}
}

func (c Command) mutates() bool {
	return c == CommandConnect || c == CommandDisconnect || c == CommandSwitch
}

func hasDuplicateCapabilities(capabilities []Capability) bool {
	seen := make(map[Capability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if !validCapability(capability) {
			return true
		}
		if _, exists := seen[capability]; exists {
			return true
		}
		seen[capability] = struct{}{}
	}
	return false
}

func validCapability(capability Capability) bool {
	for _, known := range knownCapabilities {
		if capability == known {
			return true
		}
	}
	return false
}

func containsCapability(capabilities []Capability, target Capability) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

func containsVersion(versions []uint32, target uint32) bool {
	for _, version := range versions {
		if version == target {
			return true
		}
	}
	return false
}

func decodeStrict(data []byte, destination any) error {
	if len(data) == 0 {
		return errors.New("empty JSON payload")
	}
	if !utf8.Valid(data) {
		return errors.New("JSON payload is not valid UTF-8")
	}
	if err := rejectDuplicateObjectKeys(data); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytesReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON payload: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON payload contains more than one value")
		}
		return fmt.Errorf("invalid trailing JSON data: %w", err)
	}
	return nil
}

func rejectDuplicateObjectKeys(data []byte) error {
	decoder := json.NewDecoder(bytesReader(data))
	if err := consumeJSONValue(decoder); err != nil {
		return fmt.Errorf("invalid JSON payload: %w", err)
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("JSON payload contains trailing token %v", token)
		}
		return fmt.Errorf("invalid trailing JSON data: %w", err)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

type byteReader []byte

func bytesReader(data []byte) *byteReader {
	reader := byteReader(data)
	return &reader
}

func (r *byteReader) Read(p []byte) (int, error) {
	if len(*r) == 0 {
		return 0, io.EOF
	}
	n := copy(p, *r)
	*r = (*r)[n:]
	return n, nil
}

func decodePayload[T any](raw json.RawMessage) (T, []byte, error) {
	var payload T
	if err := decodeStrict(raw, &payload); err != nil {
		return payload, nil, err
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return payload, nil, fmt.Errorf("canonicalize payload: %w", err)
	}
	return payload, canonical, nil
}

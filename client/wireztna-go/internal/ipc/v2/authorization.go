package v2

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// IdentityKind identifies the operating-system identity namespace.
type IdentityKind string

const (
	IdentityKindUID IdentityKind = "uid"
	IdentityKindSID IdentityKind = "sid"
)

// PeerIdentity is an authenticated OS peer identity. It is sealed and has no
// public constructor: only transport adapters compiled in this package may
// create the opaque authentication accepted by Server.Serve.
type PeerIdentity interface {
	Kind() IdentityKind
	ID() string
	peerIdentity()
}

type peerIdentity struct {
	kind IdentityKind
	id   string
}

func (p peerIdentity) Kind() IdentityKind { return p.kind }
func (p peerIdentity) ID() string         { return p.id }
func (peerIdentity) peerIdentity()        {}

// peerAuthentication is deliberately unexported. Future build-tagged
// transport adapters in package v2 can create one only after authenticating
// the operating-system peer.
type peerAuthentication interface {
	authenticatedPeerIdentity() PeerIdentity
}

type sealedPeerAuthentication struct {
	peer peerIdentity
}

func (a sealedPeerAuthentication) authenticatedPeerIdentity() PeerIdentity { return a.peer }

func newUIDPeerAuthentication(uid uint64) peerAuthentication {
	return sealedPeerAuthentication{peer: peerIdentity{kind: IdentityKindUID, id: strconv.FormatUint(uid, 10)}}
}

func newSIDPeerAuthentication(sid string) (peerAuthentication, error) {
	normalized, err := normalizeSID(sid)
	if err != nil {
		return nil, err
	}
	return sealedPeerAuthentication{peer: peerIdentity{kind: IdentityKindSID, id: normalized}}, nil
}

// ExpectedOwnerIdentity is configuration, not proof of peer authentication.
// Public constructors are safe because values of this type cannot be passed to
// Server.Serve as authenticated peers.
type ExpectedOwnerIdentity struct {
	kind IdentityKind
	id   string
}

func (i ExpectedOwnerIdentity) Kind() IdentityKind { return i.kind }
func (i ExpectedOwnerIdentity) ID() string         { return i.id }

// NewExpectedUIDOwner configures the expected local UID owner.
func NewExpectedUIDOwner(uid uint64) ExpectedOwnerIdentity {
	return ExpectedOwnerIdentity{kind: IdentityKindUID, id: strconv.FormatUint(uid, 10)}
}

// NewExpectedSIDOwner configures the expected local Windows SID owner.
func NewExpectedSIDOwner(sid string) (ExpectedOwnerIdentity, error) {
	normalized, err := normalizeSID(sid)
	if err != nil {
		return ExpectedOwnerIdentity{}, err
	}
	return ExpectedOwnerIdentity{kind: IdentityKindSID, id: normalized}, nil
}

func normalizeSID(sid string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(sid))
	if normalized == "" {
		return "", errors.New("SID is empty")
	}
	return normalized, nil
}

type identityValue interface {
	Kind() IdentityKind
	ID() string
}

func identityKey(identity identityValue) (string, error) {
	if identity == nil || identity.ID() == "" {
		return "", errors.New("identity is required")
	}
	if identity.Kind() != IdentityKindUID && identity.Kind() != IdentityKindSID {
		return "", errors.New("unknown identity kind")
	}
	return string(identity.Kind()) + ":" + identity.ID(), nil
}

// CommandRegistry classifies only canonical public commands. Unknown commands fail closed.
type CommandRegistry struct {
	classes map[Command]CommandClass
}

// NewCommandRegistry returns the immutable canonical command registry.
func NewCommandRegistry() CommandRegistry {
	return CommandRegistry{classes: map[Command]CommandClass{
		CommandConnect:     CommandClassOwnerControl,
		CommandDisconnect:  CommandClassOwnerControl,
		CommandSwitch:      CommandClassOwnerControl,
		CommandGetSnapshot: CommandClassRead,
		CommandSubscribe:   CommandClassRead,
	}}
}

// Class returns the registered class and false for every unknown command.
func (r CommandRegistry) Class(command Command) (CommandClass, bool) {
	class, ok := r.classes[command]
	return class, ok
}

// AuthorizationPolicy decides access within one command class.
type AuthorizationPolicy interface {
	Allows(PeerIdentity) bool
}

type authorizationPolicyFunc func(PeerIdentity) bool

func (f authorizationPolicyFunc) Allows(identity PeerIdentity) bool { return f(identity) }

// DenyAllPolicy is the fail-closed default.
func DenyAllPolicy() AuthorizationPolicy {
	return authorizationPolicyFunc(func(PeerIdentity) bool { return false })
}

// AuthenticatedPeerPolicy allows any peer authenticated by the injected
// transport adapter. It is suitable only for explicitly public read policy.
func AuthenticatedPeerPolicy() AuthorizationPolicy {
	return authorizationPolicyFunc(func(identity PeerIdentity) bool {
		_, err := identityKey(identity)
		return err == nil
	})
}

// ExactIdentityPolicy allows exactly one configured owner identity.
func ExactIdentityPolicy(owner ExpectedOwnerIdentity) AuthorizationPolicy {
	ownerKey, ownerErr := identityKey(owner)
	return authorizationPolicyFunc(func(identity PeerIdentity) bool {
		if ownerErr != nil {
			return false
		}
		candidate, err := identityKey(identity)
		return err == nil && candidate == ownerKey
	})
}

// IdentitySetPolicy allows a fixed set of configured identities.
func IdentitySetPolicy(identities ...ExpectedOwnerIdentity) AuthorizationPolicy {
	allowed := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if key, err := identityKey(identity); err == nil {
			allowed[key] = struct{}{}
		}
	}
	return authorizationPolicyFunc(func(identity PeerIdentity) bool {
		key, err := identityKey(identity)
		if err != nil {
			return false
		}
		_, ok := allowed[key]
		return ok
	})
}

// Authorizer applies a registry and one explicit policy per command class.
type Authorizer struct {
	registry CommandRegistry
	policies map[CommandClass]AuthorizationPolicy
}

// NewAuthorizer copies supplied policies. Missing and nil policies deny access.
func NewAuthorizer(registry CommandRegistry, policies map[CommandClass]AuthorizationPolicy) *Authorizer {
	copied := make(map[CommandClass]AuthorizationPolicy, len(policies))
	for class, policy := range policies {
		if policy != nil {
			copied[class] = policy
		}
	}
	return &Authorizer{registry: registry, policies: copied}
}

// Authorize rejects unknown commands, missing policies, and unauthorized peers.
func (a *Authorizer) Authorize(identity PeerIdentity, command Command) (CommandClass, error) {
	if a == nil {
		return "", &WireError{Code: ErrorCodeUnauthorized, Detail: "authorization is not configured"}
	}
	class, registered := a.registry.Class(command)
	if !registered {
		return "", &WireError{Code: ErrorCodeUnauthorized, Detail: "command is not registered"}
	}
	policy, configured := a.policies[class]
	if !configured || policy == nil || !policy.Allows(identity) {
		return "", &WireError{Code: ErrorCodeUnauthorized, Detail: fmt.Sprintf("peer is not authorized for %s commands", class)}
	}
	return class, nil
}

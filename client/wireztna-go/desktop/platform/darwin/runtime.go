//go:build darwin

// Package darwin provides the provisional macOS desktop controller adapters.
// The WireGuard backend intentionally owns routes together with the interface:
// wg-quick applies both atomically from one configuration, so Planner emits an
// empty RouteSet and RouteManager only enforces that ownership boundary. Split
// DNS remains a separate controller phase.
package darwin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wireztna/client/desktop/controller"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/tunnel"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	defaultHandshakeMaxAge = 3 * time.Minute
	defaultHealthWait      = 15 * time.Second
	defaultHealthRetry     = 250 * time.Millisecond
)

// TokenSource returns the current token from the explicitly selected owner
// configuration directory. Implementations must not return token material in
// errors.
type TokenSource func(context.Context) (string, error)

// Components contains every platform dependency required by Controller.
type Components struct {
	Sessions        controller.SessionProvider
	Planner         controller.Planner
	TruthGate       controller.TruthGate
	WireGuard       controller.WireGuardDevice
	Routes          controller.RouteManager
	DNS             controller.DNSManager
	NetworkObserver controller.NetworkObserver
}

// NewComponents constructs production adapters while keeping credential values
// in process memory. No legacy RuntimeState is written by this path.
func NewComponents(owner controller.OwnerID, ownerUID uint32, configDir string, cfg *config.ClientConfig, tokens TokenSource) (Components, error) {
	if owner == "" || ownerUID == 0 || configDir == "" || cfg == nil || tokens == nil {
		return Components{}, errors.New("Darwin runtime owner, owner UID, configuration directory, configuration, and token source are required")
	}
	if err := ValidateClientConfig(cfg); err != nil {
		return Components{}, err
	}

	secrets := newMemorySecrets()
	identityRef := controller.SecretRef("identity:" + string(owner))
	secrets.putValue(owner, identityRef, []byte(cfg.PrivateKey))

	provider := &sessionProvider{
		owner:   owner,
		baseURL: cfg.APIURL,
		tokens:  tokens,
		secrets: secrets,
		newClient: func(baseURL, token string) sessionAPI {
			client := api.NewClient(baseURL, api.WithRemoteMutationOwner(configDir, ownerUID))
			client.SetToken(token)
			return client
		},
	}
	planner := &Planner{owner: owner, cfg: cloneClientConfig(cfg), identityRef: identityRef}
	observer := realDarwinObserver{}
	routeStore := realBrokerRouteStore{interfaceName: cfg.Interface}
	orphanRoute, err := routeStore.Load()
	if err != nil {
		return Components{}, fmt.Errorf("load Darwin broker route ownership: %w", err)
	}
	identity, err := identityForConfig(tunnel.Config{OverlayIP: cfg.OverlayIP, BrokerPubKey: cfg.BrokerPublicKey})
	if err != nil {
		return Components{}, err
	}
	wireGuard := &WireGuardDevice{
		owner:           owner,
		cfg:             cloneClientConfig(cfg),
		secrets:         secrets,
		backend:         realTunnelBackend{},
		observer:        observer,
		routeStore:      routeStore,
		brokerRoute:     cloneBrokerRoute(orphanRoute),
		identity:        identity,
		handshakeMaxAge: defaultHandshakeMaxAge,
		healthWait:      defaultHealthWait,
		retryInterval:   defaultHealthRetry,
		now:             time.Now,
	}
	return Components{
		Sessions: provider,
		Planner:  planner,
		TruthGate: &TruthGate{
			owner: owner, brokerOverlayIP: cfg.BrokerOverlayIP, probe: pingBroker,
			verifyTimeout: defaultHealthWait, retryInterval: defaultHealthRetry,
		},
		WireGuard:       wireGuard,
		Routes:          &RouteManager{owner: owner, device: wireGuard, observer: observer, routeStore: routeStore},
		DNS:             &DNSManager{owner: owner, backend: newResolverDNSBackend("/etc/resolver")},
		NetworkObserver: NewNetworkObserver(),
	}, nil
}

func cloneClientConfig(cfg *config.ClientConfig) *config.ClientConfig {
	copy := *cfg
	copy.AllowedIPs = append([]string(nil), cfg.AllowedIPs...)
	return &copy
}

func ValidateClientConfig(cfg *config.ClientConfig) error {
	if strings.TrimSpace(cfg.APIURL) == "" {
		return errors.New("control plane API URL is missing; enrollment is required")
	}
	apiURL, err := url.ParseRequestURI(cfg.APIURL)
	if err != nil || apiURL.Scheme != "https" || apiURL.Host == "" || apiURL.User != nil ||
		apiURL.RawQuery != "" || apiURL.Fragment != "" || (apiURL.Path != "" && apiURL.Path != "/") {
		return errors.New("control plane API URL must be a canonical HTTPS origin")
	}
	if apiURL.Port() == "8443" {
		return errors.New("control plane API URL cannot use the internal service port")
	}
	cfg.APIURL = "https://" + apiURL.Host
	if _, err := wgtypes.ParseKey(cfg.PrivateKey); err != nil {
		return errors.New("WireGuard identity is missing or invalid; enrollment is required")
	}
	if _, err := wgtypes.ParseKey(cfg.BrokerPublicKey); err != nil {
		return errors.New("broker public key is missing or invalid; enrollment is required")
	}
	if _, err := netip.ParsePrefix(withHostPrefix(cfg.OverlayIP)); err != nil {
		return errors.New("overlay address is missing or invalid; enrollment is required")
	}
	endpoint, err := canonicalEndpoint(cfg.BrokerEndpoint)
	if err != nil {
		return errors.New("broker endpoint is missing or invalid; enrollment is required")
	}
	cfg.BrokerEndpoint = endpoint
	if net.ParseIP(cfg.BrokerOverlayIP) == nil {
		return errors.New("broker overlay address is missing or invalid; enrollment is required")
	}
	if cfg.Interface != "" && cfg.Interface != "wg-wireztna" {
		return errors.New("desktop-service requires the fixed wg-wireztna interface")
	}
	cfg.Interface = "wg-wireztna"
	return nil
}

func withHostPrefix(value string) string {
	if strings.Contains(value, "/") {
		return value
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return value
	}
	return fmt.Sprintf("%s/%d", address, address.BitLen())
}

func canonicalEndpoint(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.IndexFunc(value, func(r rune) bool {
		return r <= 0x20 || r == 0x7f
	}) >= 0 {
		return "", errors.New("invalid endpoint")
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil || host == "" || portText == "" {
		return "", errors.New("invalid endpoint")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText {
		return "", errors.New("invalid endpoint port")
	}
	if ip := net.ParseIP(host); ip == nil && !validHostname(host) {
		return "", errors.New("invalid endpoint host")
	}
	return net.JoinHostPort(strings.ToLower(host), portText), nil
}

// canonicalSessionEndpoint accepts the host-only shape emitted by older control
// planes, but it never invents a port: it reuses the validated enrollment port
// and validates the resulting endpoint through the same strict path.
func canonicalSessionEndpoint(value, enrolled string) (string, error) {
	if value == "" {
		return canonicalEndpoint(enrolled)
	}
	if endpoint, err := canonicalEndpoint(value); err == nil {
		return endpoint, nil
	}
	if strings.TrimSpace(value) != value || strings.IndexFunc(value, func(r rune) bool {
		return r <= 0x20 || r == 0x7f
	}) >= 0 {
		return "", errors.New("invalid session endpoint host")
	}
	if ip := net.ParseIP(value); ip == nil && !validHostname(value) {
		return "", errors.New("invalid session endpoint host")
	}
	_, enrolledPort, err := net.SplitHostPort(enrolled)
	if err != nil || enrolledPort == "" {
		return "", errors.New("invalid enrolled endpoint")
	}
	return canonicalEndpoint(net.JoinHostPort(strings.ToLower(value), enrolledPort))
}

func validHostname(host string) bool {
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' {
				continue
			}
			return false
		}
	}
	return true
}

type memorySecrets struct {
	mu     sync.RWMutex
	values map[controller.OwnerID]map[controller.SecretRef][]byte
}

func newMemorySecrets() *memorySecrets {
	return &memorySecrets{values: make(map[controller.OwnerID]map[controller.SecretRef][]byte)}
}

func (s *memorySecrets) putValue(owner controller.OwnerID, ref controller.SecretRef, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values[owner] == nil {
		s.values[owner] = make(map[controller.SecretRef][]byte)
	}
	s.values[owner][ref] = append([]byte(nil), value...)
}

func (s *memorySecrets) Put(ctx context.Context, owner controller.OwnerID, ref controller.SecretRef, value []byte) (controller.Undo, error) {
	if err := validateSecretCall(ctx, owner, ref); err != nil {
		return controller.Undo{}, err
	}
	s.mu.Lock()
	previous, existed := s.values[owner][ref]
	previous = append([]byte(nil), previous...)
	if s.values[owner] == nil {
		s.values[owner] = make(map[controller.SecretRef][]byte)
	}
	s.values[owner][ref] = append([]byte(nil), value...)
	s.mu.Unlock()
	return controller.NewUndo(owner, func(ctx context.Context, undoOwner controller.OwnerID) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if existed {
			s.values[undoOwner][ref] = previous
		} else {
			delete(s.values[undoOwner], ref)
		}
		return nil
	})
}

func (s *memorySecrets) Get(ctx context.Context, owner controller.OwnerID, ref controller.SecretRef) ([]byte, error) {
	if err := validateSecretCall(ctx, owner, ref); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.values[owner][ref]
	if !ok {
		return nil, errors.New("credential reference is unavailable")
	}
	return append([]byte(nil), value...), nil
}

func (s *memorySecrets) Delete(ctx context.Context, owner controller.OwnerID, ref controller.SecretRef) (controller.Undo, error) {
	if err := validateSecretCall(ctx, owner, ref); err != nil {
		return controller.Undo{}, err
	}
	s.mu.Lock()
	previous, existed := s.values[owner][ref]
	previous = append([]byte(nil), previous...)
	delete(s.values[owner], ref)
	s.mu.Unlock()
	return controller.NewUndo(owner, func(ctx context.Context, undoOwner controller.OwnerID) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if existed {
			s.putValue(undoOwner, ref, previous)
		}
		return nil
	})
}

func validateSecretCall(ctx context.Context, owner controller.OwnerID, ref controller.SecretRef) error {
	if ctx == nil {
		return errors.New("credential context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner == "" || ref == "" {
		return errors.New("credential owner and reference are required")
	}
	return nil
}

type sessionAPI interface {
	RenewSessionContext(context.Context, string, ...string) (*api.SessionResponse, error)
	GetDNSZonesContext(context.Context) ([]string, error)
}

type sessionProvider struct {
	owner     controller.OwnerID
	baseURL   string
	tokens    TokenSource
	secrets   *memorySecrets
	newClient func(string, string) sessionAPI
}

type acquiredSession struct {
	owner          controller.OwnerID
	generation     controller.Generation
	expiresAt      time.Time
	credential     controller.SecretRef
	allowed        []string
	zones          []string
	exitNode       bool
	brokerEndpoint string
}

func (s *acquiredSession) Owner() controller.OwnerID         { return s.owner }
func (s *acquiredSession) Generation() controller.Generation { return s.generation }
func (s *acquiredSession) ExpiresAt() time.Time              { return s.expiresAt }

func (p *sessionProvider) Acquire(ctx context.Context, owner controller.OwnerID, desired controller.DesiredState) (controller.Session, error) {
	if ctx == nil {
		return nil, errors.New("session context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if owner != p.owner || !desired.Connected || desired.Generation == 0 || desired.GroupID == "" {
		return nil, &controller.Error{Code: controller.ErrorCodeInvalidArgument, Detail: "connected session intent is invalid"}
	}
	token, err := p.tokens(ctx)
	if err != nil || token == "" || config.IsTokenExpired(token) {
		return nil, reauthRequired()
	}
	client := p.newClient(p.baseURL, token)
	response, err := client.RenewSessionContext(ctx, string(desired.GroupID), string(desired.ExitNodeID))
	if err != nil {
		if isAuthenticationError(err) {
			return nil, reauthRequired()
		}
		return nil, err
	}
	if response == nil || response.PresharedKey == "" || response.SessionID == "" || !response.ExpiresAt.After(time.Now()) {
		return nil, errors.New("control plane returned an incomplete session")
	}
	if _, err := wgtypes.ParseKey(response.PresharedKey); err != nil {
		return nil, errors.New("control plane returned an invalid session credential")
	}

	var zones []string
	exitRequested := desired.ExitNodeID != ""
	if response.IsExitNode != exitRequested {
		return nil, &controller.Error{
			Code:   controller.ErrorCodeServiceUnavailable,
			Detail: "control plane returned a tunnel mode different from the requested mode",
		}
	}
	exitNode := response.IsExitNode
	if !exitNode {
		zones, err = client.GetDNSZonesContext(ctx)
		if err != nil {
			if isAuthenticationError(err) {
				return nil, reauthRequired()
			}
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	credential := controller.SecretRef(fmt.Sprintf("session:%d:psk", desired.Generation))
	p.secrets.putValue(owner, credential, []byte(response.PresharedKey))
	return &acquiredSession{
		owner:          owner,
		generation:     desired.Generation,
		expiresAt:      response.ExpiresAt.Time.UTC(),
		credential:     credential,
		allowed:        append([]string(nil), response.AllowedIPs...),
		zones:          append([]string(nil), zones...),
		exitNode:       exitNode,
		brokerEndpoint: response.BrokerEndpoint,
	}, nil
}

func reauthRequired() error {
	return &controller.Error{Code: controller.ErrorCodeReauthRequired, Detail: "authentication is required"}
}

func isAuthenticationError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "token rejected") || strings.Contains(message, "http 401")
}

// Planner converts a validated API session into a secret-reference-only plan.
type Planner struct {
	owner       controller.OwnerID
	cfg         *config.ClientConfig
	identityRef controller.SecretRef
}

func (p *Planner) Plan(ctx context.Context, owner controller.OwnerID, desired controller.DesiredState, session controller.Session) (controller.Plan, error) {
	if ctx == nil {
		return controller.Plan{}, errors.New("planner context is required")
	}
	if err := ctx.Err(); err != nil {
		return controller.Plan{}, err
	}
	if owner != p.owner || desired.ConfigurationID == "" || desired.Generation == 0 {
		return controller.Plan{}, &controller.Error{Code: controller.ErrorCodeInvalidArgument, Detail: "desired revision is invalid"}
	}
	plan := controller.Plan{
		WireGuard: controller.WireGuardConfig{ConfigurationID: desired.ConfigurationID, Generation: desired.Generation},
		Applied: controller.AppliedState{
			ConfigurationID: desired.ConfigurationID,
			Generation:      desired.Generation,
			GroupID:         desired.GroupID,
			ExitNodeID:      desired.ExitNodeID,
			Connected:       desired.Connected,
		},
	}
	if !desired.Connected {
		if session != nil {
			return controller.Plan{}, errors.New("disconnect plan must not receive a session")
		}
		return plan, nil
	}
	acquired, ok := session.(*acquiredSession)
	if !ok || acquired.owner != owner || acquired.generation != desired.Generation {
		return controller.Plan{}, errors.New("session does not belong to desired revision")
	}
	plan.Applied.ExpiresAt = acquired.expiresAt
	address, err := netip.ParsePrefix(withHostPrefix(p.cfg.OverlayIP))
	if err != nil {
		return controller.Plan{}, errors.New("configured overlay address is invalid")
	}
	allowedValues := acquired.allowed
	if len(allowedValues) == 0 {
		return controller.Plan{}, errors.New("renewed session has no authorized prefixes")
	}
	allowed := make([]netip.Prefix, 0, len(allowedValues))
	for _, value := range allowedValues {
		prefix, parseErr := netip.ParsePrefix(value)
		if parseErr != nil {
			return controller.Plan{}, fmt.Errorf("invalid allowed prefix %q", value)
		}
		allowed = append(allowed, prefix.Masked())
	}
	allowed, err = normalizeAllowedPrefixes(allowed)
	if err != nil || len(allowed) == 0 {
		return controller.Plan{}, errors.New("session has no valid allowed prefixes")
	}
	endpoint := acquired.brokerEndpoint
	if endpoint == "" {
		endpoint = p.cfg.BrokerEndpoint
	}
	endpoint, err = canonicalSessionEndpoint(endpoint, p.cfg.BrokerEndpoint)
	if err != nil {
		return controller.Plan{}, &controller.Error{
			Code:   controller.ErrorCodeServiceUnavailable,
			Detail: "control plane returned an invalid WireGuard endpoint",
		}
	}
	plan.WireGuard = controller.WireGuardConfig{
		ConfigurationID: desired.ConfigurationID,
		Generation:      desired.Generation,
		InterfaceID:     controller.InterfaceID(p.cfg.Interface),
		Addresses:       []netip.Prefix{address},
		Identity:        p.identityRef,
		Peers: []controller.WireGuardPeer{{
			PublicKey:       p.cfg.BrokerPublicKey,
			Credential:      acquired.credential,
			Endpoint:        endpoint,
			AllowedPrefixes: allowed,
			Keepalive:       25 * time.Second,
		}},
	}
	// RouteSet is deliberately empty: wg-quick owns routes for this provisional backend.
	if !acquired.exitNode && len(acquired.zones) > 0 {
		server, parseErr := netip.ParseAddr(p.cfg.TunnelDNS)
		if parseErr != nil {
			return controller.Plan{}, errors.New("split DNS server is invalid")
		}
		plan.DNS = controller.DNSConfig{Servers: []netip.Addr{server}, MatchDomains: append([]string(nil), acquired.zones...)}
	}
	return plan, nil
}

type tunnelHandle interface {
	Up() error
	Down()
}

type tunnelBackend interface {
	New(tunnel.Config) (tunnelHandle, error)
	Down(string, string) error
	Status(string) (*tunnel.StatusInfo, error)
}

type realTunnelBackend struct{}

func (realTunnelBackend) New(cfg tunnel.Config) (tunnelHandle, error) { return tunnel.New(cfg) }
func (realTunnelBackend) Down(name, endpoint string) error {
	return tunnel.DownWithEndpoint(name, endpoint)
}
func (realTunnelBackend) Status(name string) (*tunnel.StatusInfo, error) {
	return tunnel.GetStatus(name)
}

type brokerRouteIdentity struct {
	destination netip.Prefix
	gateway     netip.Addr
	interfaceID string
}

type brokerRouteStore interface {
	Load() (*brokerRouteIdentity, error)
}

type realBrokerRouteStore struct{ interfaceName string }

func (s realBrokerRouteStore) Load() (*brokerRouteIdentity, error) {
	marker, err := tunnel.LoadDarwinBrokerRoute(s.interfaceName)
	if err != nil || marker == nil {
		return nil, err
	}
	destination, err := netip.ParsePrefix(marker.Destination)
	if err != nil {
		return nil, err
	}
	gateway, err := netip.ParseAddr(marker.Gateway)
	if err != nil {
		return nil, err
	}
	return &brokerRouteIdentity{destination: destination.Masked(), gateway: gateway.Unmap(), interfaceID: marker.Interface}, nil
}

func cloneBrokerRoute(route *brokerRouteIdentity) *brokerRouteIdentity {
	if route == nil {
		return nil
	}
	copy := *route
	return &copy
}

func exactBrokerRoute(expected *brokerRouteIdentity, observed []darwinRoute) bool {
	if expected == nil {
		return false
	}
	matches := 0
	for _, route := range observed {
		if route.destination.Masked() == expected.destination && route.gateway.Unmap() == expected.gateway && route.interfaceID == expected.interfaceID {
			matches++
		}
	}
	return matches == 1
}

// WireGuardDevice adapts the existing real wg-quick lifecycle. Calls are
// synchronous; context is checked before and after each legacy operation so no
// adapter-owned goroutine can outlive Apply.
type WireGuardDevice struct {
	mu                    sync.Mutex
	owner                 controller.OwnerID
	cfg                   *config.ClientConfig
	secrets               *memorySecrets
	backend               tunnelBackend
	observer              darwinObserver
	routeStore            brokerRouteStore
	active                *tunnel.Config
	activeAllowedPrefixes []netip.Prefix
	brokerRoute           *brokerRouteIdentity
	ownedInterface        string
	identity              darwinDeviceIdentity
	expectConnected       bool
	fullTunnel            bool
	handshakeMaxAge       time.Duration
	healthWait            time.Duration
	retryInterval         time.Duration
	now                   func() time.Time
}

type wireGuardState struct {
	active          *tunnel.Config
	allowedPrefixes []netip.Prefix
	brokerRoute     *brokerRouteIdentity
	ownedInterface  string
	identity        darwinDeviceIdentity
	connected       bool
	fullTunnel      bool
}

func (d *WireGuardDevice) stateLocked() wireGuardState {
	return wireGuardState{
		active:          cloneTunnelConfig(d.active),
		allowedPrefixes: append([]netip.Prefix(nil), d.activeAllowedPrefixes...),
		brokerRoute:     cloneBrokerRoute(d.brokerRoute),
		ownedInterface:  d.ownedInterface,
		identity:        d.identity,
		connected:       d.expectConnected,
		fullTunnel:      d.fullTunnel,
	}
}

func (d *WireGuardDevice) routeState() wireGuardState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stateLocked()
}

func (d *WireGuardDevice) loadBrokerRoute() (*brokerRouteIdentity, error) {
	if d.routeStore == nil {
		return cloneBrokerRoute(d.brokerRoute), nil
	}
	return d.routeStore.Load()
}

func (d *WireGuardDevice) observeExactBrokerRoute(ctx context.Context, expected *brokerRouteIdentity) (bool, error) {
	if expected == nil {
		return false, nil
	}
	routes, err := d.observer.Routes(ctx)
	if err != nil {
		return false, err
	}
	return exactBrokerRoute(expected, routes), nil
}

func (d *WireGuardDevice) Apply(ctx context.Context, owner controller.OwnerID, wg controller.WireGuardConfig) (controller.Undo, error) {
	if ctx == nil || owner != d.owner || d.observer == nil {
		return controller.Undo{}, errors.New("WireGuard owner, context, or observer is invalid")
	}
	if err := ctx.Err(); err != nil {
		return controller.Undo{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	previous := d.stateLocked()
	undo, err := controller.NewUndo(owner, func(undoCtx context.Context, undoOwner controller.OwnerID) error {
		return d.restore(undoCtx, undoOwner, previous)
	})
	if err != nil {
		return controller.Undo{}, err
	}

	if len(wg.Peers) == 0 {
		markerRoute, err := d.loadBrokerRoute()
		if err != nil {
			return undo, fmt.Errorf("load broker exclusion ownership before disconnect: %w", err)
		}
		if markerRoute != nil {
			previous.brokerRoute = cloneBrokerRoute(markerRoute)
		}
		if previous.identity.overlayIP.IsValid() {
			interfaceID, present, err := d.observer.Device(ctx, previous.identity)
			if err != nil {
				return undo, err
			}
			if present {
				previous.ownedInterface = interfaceID
				routes, err := d.observer.Routes(ctx)
				if err != nil {
					return undo, err
				}
				previous.allowedPrefixes = previous.allowedPrefixes[:0]
				for _, route := range ownedForwardingRoutes(previous, routes) {
					previous.allowedPrefixes = append(previous.allowedPrefixes, route.destination)
				}
			}
		}
		if err := d.backend.Down(d.cfg.Interface, d.cfg.BrokerEndpoint); err != nil {
			return undo, err
		}
		if previous.identity.overlayIP.IsValid() {
			if _, present, err := d.observer.Device(ctx, previous.identity); err != nil {
				return undo, err
			} else if present {
				return undo, errors.New("WireGuard device remained active after disconnect")
			}
		}
		if previous.ownedInterface != "" {
			routes, err := d.observer.Routes(ctx)
			if err != nil {
				return undo, err
			}
			for _, route := range routes {
				if route.interfaceID == previous.ownedInterface {
					return undo, errors.New("WireGuard routes remained active after disconnect")
				}
			}
		}
		remainingMarker, err := d.loadBrokerRoute()
		if err != nil {
			return undo, fmt.Errorf("verify broker exclusion ownership removal: %w", err)
		}
		if remainingMarker != nil {
			return undo, errors.New("broker exclusion marker remained after disconnect")
		}
		if exact, err := d.observeExactBrokerRoute(ctx, previous.brokerRoute); err != nil {
			return undo, err
		} else if exact {
			return undo, errors.New("broker exclusion route remained after disconnect")
		}
		d.active = nil
		d.activeAllowedPrefixes = append([]netip.Prefix(nil), previous.allowedPrefixes...)
		d.brokerRoute = nil
		d.ownedInterface = previous.ownedInterface
		d.identity = previous.identity
		d.expectConnected = false
		d.fullTunnel = false
		if err := ctx.Err(); err != nil {
			return undo, err
		}
		return undo, nil
	}
	candidate, err := d.tunnelConfig(ctx, owner, wg)
	if err != nil {
		return controller.Undo{}, err
	}
	identity, err := identityForConfig(candidate)
	if err != nil {
		return controller.Undo{}, err
	}
	allowed, err := prefixesFromStrings(candidate.AllowedIPs)
	if err != nil {
		return controller.Undo{}, err
	}
	handle, err := d.backend.New(candidate)
	if err != nil {
		return controller.Undo{}, err
	}
	if err := handle.Up(); err != nil {
		return undo, err
	}
	// Record the known side effect before observation so Undo can always tear it down.
	d.active = cloneTunnelConfig(&candidate)
	d.activeAllowedPrefixes = allowed
	d.identity = identity
	d.expectConnected = true
	d.fullTunnel = candidate.IsExitNode
	interfaceID, present, err := d.observer.Device(ctx, identity)
	if err != nil {
		return undo, err
	}
	if !present || interfaceID == "" {
		return undo, errors.New("WireGuard device did not expose an owned utun identity")
	}
	d.ownedInterface = interfaceID
	markerRoute, err := d.loadBrokerRoute()
	if err != nil {
		return undo, fmt.Errorf("load broker exclusion ownership after connect: %w", err)
	}
	if candidate.IsExitNode {
		if markerRoute == nil {
			return undo, errors.New("full tunnel has no broker exclusion ownership marker")
		}
		exact, err := d.observeExactBrokerRoute(ctx, markerRoute)
		if err != nil {
			return undo, err
		}
		if !exact {
			return undo, errors.New("full tunnel broker exclusion route is absent or drifted")
		}
	} else if markerRoute != nil {
		return undo, errors.New("split tunnel unexpectedly owns a broker exclusion route")
	}
	d.brokerRoute = cloneBrokerRoute(markerRoute)
	if err := ctx.Err(); err != nil {
		return undo, err
	}
	return undo, nil
}

func (d *WireGuardDevice) tunnelConfig(ctx context.Context, owner controller.OwnerID, wg controller.WireGuardConfig) (tunnel.Config, error) {
	if len(wg.Peers) != 1 || len(wg.Addresses) != 1 || wg.InterfaceID == "" {
		return tunnel.Config{}, errors.New("Darwin WireGuard plan must contain one address and one peer")
	}
	peer := wg.Peers[0]
	privateKey, err := d.secrets.Get(ctx, owner, wg.Identity)
	if err != nil {
		return tunnel.Config{}, err
	}
	defer clear(privateKey)
	psk, err := d.secrets.Get(ctx, owner, peer.Credential)
	if err != nil {
		return tunnel.Config{}, err
	}
	defer clear(psk)
	allowed := make([]string, 0, len(peer.AllowedPrefixes))
	for _, prefix := range peer.AllowedPrefixes {
		allowed = append(allowed, prefix.String())
	}
	return tunnel.Config{
		InterfaceName:  string(wg.InterfaceID),
		PrivateKey:     string(privateKey),
		OverlayIP:      wg.Addresses[0].String(),
		BrokerPubKey:   peer.PublicKey,
		BrokerEndpoint: peer.Endpoint,
		PresharedKey:   string(psk),
		AllowedIPs:     allowed,
		DNS:            d.cfg.TunnelDNS,
		IsExitNode:     isFullTunnelPrefixes(peer.AllowedPrefixes),
	}, nil
}

func (d *WireGuardDevice) restore(ctx context.Context, owner controller.OwnerID, previous wireGuardState) error {
	if owner != d.owner {
		return errors.New("WireGuard rollback owner mismatch")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	endpoint := d.cfg.BrokerEndpoint
	if d.active != nil {
		endpoint = d.active.BrokerEndpoint
	}
	currentRoute := cloneBrokerRoute(d.brokerRoute)
	if marker, err := d.loadBrokerRoute(); err != nil {
		return err
	} else if marker != nil {
		currentRoute = marker
	}
	if err := d.backend.Down(d.cfg.Interface, endpoint); err != nil {
		return err
	}
	if marker, err := d.loadBrokerRoute(); err != nil {
		return err
	} else if marker != nil {
		return errors.New("WireGuard rollback left broker exclusion ownership behind")
	}
	if exact, err := d.observeExactBrokerRoute(ctx, currentRoute); err != nil {
		return err
	} else if exact {
		return errors.New("WireGuard rollback left the broker exclusion route behind")
	}
	d.active = nil
	d.brokerRoute = nil
	d.expectConnected = false
	d.fullTunnel = false
	if !previous.connected || previous.active == nil {
		if previous.identity.overlayIP.IsValid() {
			if _, present, err := d.observer.Device(ctx, previous.identity); err != nil {
				return err
			} else if present {
				return errors.New("WireGuard rollback could not establish disconnected absence")
			}
		}
		d.activeAllowedPrefixes = append([]netip.Prefix(nil), previous.allowedPrefixes...)
		d.ownedInterface = previous.ownedInterface
		d.identity = previous.identity
		return ctx.Err()
	}
	handle, err := d.backend.New(*previous.active)
	if err != nil {
		return err
	}
	if err := handle.Up(); err != nil {
		return err
	}
	interfaceID, present, err := d.observer.Device(ctx, previous.identity)
	if err != nil {
		return err
	}
	if !present || interfaceID == "" {
		return errors.New("WireGuard rollback did not recreate an owned utun device")
	}
	marker, err := d.loadBrokerRoute()
	if err != nil {
		return err
	}
	if previous.fullTunnel {
		if marker == nil {
			return errors.New("WireGuard rollback did not recreate broker exclusion ownership")
		}
		exact, err := d.observeExactBrokerRoute(ctx, marker)
		if err != nil || !exact {
			return errors.New("WireGuard rollback did not recreate the exact broker exclusion route")
		}
	} else if marker != nil {
		return errors.New("WireGuard rollback recreated unexpected broker exclusion ownership")
	}
	d.active = cloneTunnelConfig(previous.active)
	d.activeAllowedPrefixes = append([]netip.Prefix(nil), previous.allowedPrefixes...)
	d.brokerRoute = cloneBrokerRoute(marker)
	d.ownedInterface = interfaceID
	d.identity = previous.identity
	d.expectConnected = true
	d.fullTunnel = previous.fullTunnel
	return ctx.Err()
}

func cloneTunnelConfig(value *tunnel.Config) *tunnel.Config {
	if value == nil {
		return nil
	}
	copy := *value
	copy.AllowedIPs = append([]string(nil), value.AllowedIPs...)
	return &copy
}

func (d *WireGuardDevice) Health(ctx context.Context, owner controller.OwnerID) (controller.WireGuardHealth, error) {
	if ctx == nil || owner != d.owner || d.observer == nil {
		return controller.WireGuardHealth{}, errors.New("WireGuard health owner, context, or observer is invalid")
	}
	if err := ctx.Err(); err != nil {
		return controller.WireGuardHealth{}, err
	}
	state := d.routeState()
	markerRoute, err := d.loadBrokerRoute()
	if err != nil {
		return controller.WireGuardHealth{Status: controller.HealthUnhealthy, Detail: "broker exclusion marker is invalid"}, nil
	}
	if !state.connected {
		if markerRoute != nil {
			return controller.WireGuardHealth{Status: controller.HealthUnhealthy, Detail: "orphaned broker exclusion marker remained after disconnect"}, nil
		}
		if exact, observeErr := d.observeExactBrokerRoute(ctx, state.brokerRoute); observeErr != nil {
			return controller.WireGuardHealth{}, observeErr
		} else if exact {
			return controller.WireGuardHealth{Status: controller.HealthUnhealthy, Detail: "orphaned broker exclusion route remained after disconnect"}, nil
		}
		if state.identity.overlayIP.IsValid() {
			if _, present, err := d.observer.Device(ctx, state.identity); err != nil {
				return controller.WireGuardHealth{}, err
			} else if present {
				return controller.WireGuardHealth{Status: controller.HealthUnhealthy, Detail: "owned WireGuard device remained active after disconnect"}, nil
			}
		}
		return controller.WireGuardHealth{Status: controller.HealthHealthy, Detail: "owned WireGuard device is absent"}, nil
	}
	interfaceID, present, err := d.observer.Device(ctx, state.identity)
	if err != nil {
		return controller.WireGuardHealth{}, err
	}
	if !present || interfaceID != state.ownedInterface {
		return controller.WireGuardHealth{Status: controller.HealthUnhealthy, Detail: "owned WireGuard utun identity changed or disappeared"}, nil
	}
	if state.fullTunnel {
		if markerRoute == nil {
			return controller.WireGuardHealth{Status: controller.HealthUnhealthy, Detail: "full tunnel broker exclusion marker is absent"}, nil
		}
		exact, observeErr := d.observeExactBrokerRoute(ctx, markerRoute)
		if observeErr != nil {
			return controller.WireGuardHealth{}, observeErr
		}
		if !exact {
			return controller.WireGuardHealth{Status: controller.HealthUnhealthy, Detail: "full tunnel broker exclusion route is absent or drifted"}, nil
		}
	} else if markerRoute != nil {
		return controller.WireGuardHealth{Status: controller.HealthUnhealthy, Detail: "split tunnel has unexpected broker exclusion ownership"}, nil
	}
	wait := d.healthWait
	retry := d.retryInterval
	if wait <= 0 {
		wait = defaultHealthWait
	}
	if retry <= 0 {
		retry = defaultHealthRetry
	}
	now := d.now
	if now == nil {
		now = time.Now
	}
	deadline := time.Now().Add(wait)
	for {
		status, statusErr := d.backend.Status(d.cfg.Interface)
		if statusErr == nil && status != nil && !status.LastHandshake.IsZero() {
			if age := now().Sub(status.LastHandshake); age >= 0 && age <= d.handshakeMaxAge {
				return controller.WireGuardHealth{Status: controller.HealthHealthy, Detail: "owned WireGuard handshake is recent"}, nil
			}
		}
		if !time.Now().Before(deadline) {
			return controller.WireGuardHealth{Status: controller.HealthUnhealthy, Detail: "WireGuard handshake did not converge"}, nil
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return controller.WireGuardHealth{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// RouteManager observes the exact routes created by wg-quick without claiming
// mutation ownership from WireGuardDevice.
type RouteManager struct {
	owner      controller.OwnerID
	device     *WireGuardDevice
	observer   darwinObserver
	routeStore brokerRouteStore
}

func (m *RouteManager) Apply(ctx context.Context, owner controller.OwnerID, routes controller.RouteSet) (controller.Undo, error) {
	if ctx == nil || owner != m.owner || len(routes.Routes) != 0 {
		return controller.Undo{}, errors.New("Darwin provisional route plan must be empty")
	}
	if err := ctx.Err(); err != nil {
		return controller.Undo{}, err
	}
	return controller.NewUndo(owner, func(ctx context.Context, _ controller.OwnerID) error { return ctx.Err() })
}

func (m *RouteManager) Health(ctx context.Context, owner controller.OwnerID) (controller.RouteHealth, error) {
	if ctx == nil || owner != m.owner || m.device == nil || m.observer == nil {
		return controller.RouteHealth{}, errors.New("route health owner, context, device, or observer is invalid")
	}
	if err := ctx.Err(); err != nil {
		return controller.RouteHealth{}, err
	}
	state := m.device.routeState()
	observed, err := m.observer.Routes(ctx)
	if err != nil {
		return controller.RouteHealth{}, err
	}
	markerRoute := cloneBrokerRoute(state.brokerRoute)
	if m.routeStore != nil {
		markerRoute, err = m.routeStore.Load()
		if err != nil {
			return controller.RouteHealth{Status: controller.HealthUnhealthy, Detail: "broker exclusion marker is invalid"}, nil
		}
	}
	if !state.connected {
		if markerRoute != nil {
			return controller.RouteHealth{Status: controller.HealthUnhealthy, Detail: "orphaned broker exclusion ownership remained after restart or disconnect"}, nil
		}
		if exactBrokerRoute(state.brokerRoute, observed) {
			return controller.RouteHealth{Status: controller.HealthUnhealthy, Detail: "orphaned broker exclusion route remained after disconnect"}, nil
		}
		for _, route := range observed {
			if state.ownedInterface != "" && route.interfaceID == state.ownedInterface {
				return controller.RouteHealth{Status: controller.HealthUnhealthy, Detail: "owned wg-quick routes remained after disconnect"}, nil
			}
		}
		return controller.RouteHealth{Status: controller.HealthHealthy, Detail: "owned wg-quick and broker exclusion routes are absent"}, nil
	}
	interfaceID, present, err := m.observer.Device(ctx, state.identity)
	if err != nil {
		return controller.RouteHealth{}, err
	}
	ownedRoutes := ownedForwardingRoutes(state, observed)
	if !present || interfaceID != state.ownedInterface || !exactRoutes(state.allowedPrefixes, state.ownedInterface, ownedRoutes) {
		return controller.RouteHealth{Status: controller.HealthUnhealthy, Detail: "wg-quick routes do not exactly match the owned utun and allowed prefixes"}, nil
	}
	if state.fullTunnel {
		if markerRoute == nil || !exactBrokerRoute(markerRoute, observed) {
			return controller.RouteHealth{Status: controller.HealthUnhealthy, Detail: "full tunnel broker exclusion route is absent or drifted"}, nil
		}
	} else if markerRoute != nil {
		return controller.RouteHealth{Status: controller.HealthUnhealthy, Detail: "split tunnel has unexpected broker exclusion ownership"}, nil
	}
	return controller.RouteHealth{Status: controller.HealthHealthy, Detail: "wg-quick routes and broker exclusion ownership exactly match"}, nil
}

type brokerProbe func(context.Context, string) bool

// TruthGate refuses Connected unless every port is healthy and the broker is
// reachable end-to-end through the just-applied tunnel.
type TruthGate struct {
	owner           controller.OwnerID
	brokerOverlayIP string
	probe           brokerProbe
	verifyTimeout   time.Duration
	retryInterval   time.Duration
}

func (g *TruthGate) Verify(ctx context.Context, owner controller.OwnerID, desired controller.DesiredState, applied controller.AppliedState, observed controller.HealthObservation) (controller.Truth, error) {
	if ctx == nil || owner != g.owner || g.probe == nil {
		return controller.Truth{}, errors.New("truth gate owner or context is invalid")
	}
	if err := ctx.Err(); err != nil {
		return controller.Truth{}, err
	}
	if applied.ConfigurationID != desired.ConfigurationID || applied.Generation != desired.Generation || applied.Connected != desired.Connected {
		return controller.Truth{}, errors.New("truth gate revision mismatch")
	}
	health := controller.Health{
		WireGuard: controller.PortHealth(observed.WireGuard).Status,
		Routes:    controller.PortHealth(observed.Routes).Status,
		DNS:       controller.PortHealth(observed.DNS).Status,
		EndToEnd:  controller.HealthUnhealthy,
	}
	portsHealthy := health.WireGuard == controller.HealthHealthy && health.Routes == controller.HealthHealthy && health.DNS == controller.HealthHealthy
	if !desired.Connected {
		if !portsHealthy {
			return controller.Truth{State: controller.ConnectionStateDegraded, Health: health}, nil
		}
		return controller.Truth{State: controller.ConnectionStateDisconnected, Health: health}, nil
	}
	if !portsHealthy {
		return controller.Truth{State: controller.ConnectionStateDegraded, Health: health}, nil
	}
	timeout := g.verifyTimeout
	if timeout <= 0 {
		timeout = defaultHealthWait
	}
	retry := g.retryInterval
	if retry <= 0 {
		retry = defaultHealthRetry
	}
	verifyContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if g.probe(verifyContext, g.brokerOverlayIP) {
			health.EndToEnd = controller.HealthHealthy
			health.Healthy = health.ConditionsHealthy()
			return controller.Truth{State: controller.ConnectionStateConnected, Health: health}, nil
		}
		timer := time.NewTimer(retry)
		select {
		case <-verifyContext.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return controller.Truth{}, ctx.Err()
			}
			return controller.Truth{State: controller.ConnectionStateDegraded, Health: health}, nil
		case <-timer.C:
		}
	}
}

func pingBroker(ctx context.Context, host string) bool {
	if ctx == nil || net.ParseIP(host) == nil {
		return false
	}
	command := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "2000", host)
	return command.Run() == nil && ctx.Err() == nil
}

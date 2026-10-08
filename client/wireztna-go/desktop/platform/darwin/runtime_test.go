//go:build darwin

package darwin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wireztna/client/desktop/controller"
	"github.com/wireztna/client/internal/api"
	"github.com/wireztna/client/internal/config"
	"github.com/wireztna/client/internal/tunnel"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestPlannerBuildsSecretReferencePlanWithWGQuickOwnedRoutes(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	cfg := testClientConfig(t)
	planner := &Planner{owner: owner, cfg: cfg, identityRef: "identity-ref"}
	desired := controller.DesiredState{ConfigurationID: "cfg-1", Generation: 7, GroupID: "group-1", Connected: true}
	session := &acquiredSession{
		owner: owner, generation: desired.Generation, credential: "psk-ref",
		allowed: []string{"10.20.0.0/16"}, zones: []string{"internal.example"},
	}

	plan, err := planner.Plan(context.Background(), owner, desired, session)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if err := plan.Validate(desired); err != nil {
		t.Fatalf("plan validation failed: %v", err)
	}
	if len(plan.Routes.Routes) != 0 {
		t.Fatalf("routes = %#v, want empty wg-quick-owned RouteSet", plan.Routes.Routes)
	}
	if got := plan.WireGuard.Peers[0].Credential; got != "psk-ref" {
		t.Fatalf("credential = %q, want opaque reference", got)
	}
	if len(plan.DNS.Servers) != 1 || plan.DNS.Servers[0].String() != cfg.TunnelDNS {
		t.Fatalf("DNS = %#v, want split DNS plan", plan.DNS)
	}
	serialized := fmt.Sprintf("%#v", plan)
	if strings.Contains(serialized, cfg.PrivateKey) {
		t.Fatal("plan contains private key material")
	}
}

func TestPlannerPreservesPhysicalDNSForFullTunnel(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	cfg := testClientConfig(t)
	planner := &Planner{owner: owner, cfg: cfg, identityRef: "identity-ref"}
	desired := controller.DesiredState{ConfigurationID: "cfg-2", Generation: 8, GroupID: "group-1", ExitNodeID: "exit-1", Connected: true}
	session := &acquiredSession{owner: owner, generation: 8, credential: "psk-ref", allowed: []string{"0.0.0.0/0"}, exitNode: true}

	plan, err := planner.Plan(context.Background(), owner, desired, session)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.DNS.Servers) != 0 || len(plan.DNS.MatchDomains) != 0 {
		t.Fatalf("full-tunnel split DNS plan = %#v, want empty", plan.DNS)
	}
	got := plan.WireGuard.Peers[0].AllowedPrefixes
	if len(got) != 2 || got[0].String() != "0.0.0.0/1" || got[1].String() != "128.0.0.0/1" {
		t.Fatalf("full-tunnel allowed prefixes = %v, want canonical /1 pair", got)
	}
	if len(plan.Routes.Routes) != 0 {
		t.Fatalf("full-tunnel RouteSet = %#v, want empty wg-quick-owned set", plan.Routes)
	}
}

func TestSessionProviderRenewsAndKeepsPSKInMemory(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	secrets := newMemorySecrets()
	psk := mustKey(t)
	fake := &fakeSessionAPI{response: &api.SessionResponse{
		SessionID: "session-1", PresharedKey: psk, ExpiresAt: api.FlexTime{Time: time.Now().Add(time.Hour)},
		AllowedIPs: []string{"10.20.0.0/16"},
	}, zones: []string{"internal.example"}}
	provider := &sessionProvider{
		owner: owner, baseURL: "https://control.example", secrets: secrets,
		tokens:    func(context.Context) (string, error) { return validToken(), nil },
		newClient: func(_, _ string) sessionAPI { return fake },
	}
	desired := controller.DesiredState{ConfigurationID: "cfg", Generation: 3, GroupID: "group", Connected: true}

	result, err := provider.Acquire(context.Background(), owner, desired)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	acquired := result.(*acquiredSession)
	stored, err := secrets.Get(context.Background(), owner, acquired.credential)
	if err != nil || string(stored) != psk {
		t.Fatalf("stored credential = %q, %v", stored, err)
	}
	if !fake.renewed || !fake.zonesRead {
		t.Fatalf("API calls renewed=%v zones=%v, want both", fake.renewed, fake.zonesRead)
	}
}

func TestSessionProviderAndPlannerAcceptControlPlaneFullTunnelResponse(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	cfg := testClientConfig(t)
	fake := &fakeSessionAPI{response: &api.SessionResponse{
		SessionID:      "session-vpn",
		PresharedKey:   mustKey(t),
		ExpiresAt:      api.FlexTime{Time: time.Now().Add(time.Hour)},
		AllowedIPs:     []string{"10.200.0.1/32", "0.0.0.0/0"},
		IsExitNode:     true,
		BrokerEndpoint: "wg.example.test",
	}}
	provider := &sessionProvider{
		owner: owner, baseURL: "https://control.example", secrets: newMemorySecrets(),
		tokens:    func(context.Context) (string, error) { return validToken(), nil },
		newClient: func(_, _ string) sessionAPI { return fake },
	}
	desired := controller.DesiredState{
		ConfigurationID: "cfg-vpn", Generation: 9, GroupID: "group-1", ExitNodeID: "exit-1", Connected: true,
	}

	session, err := provider.Acquire(context.Background(), owner, desired)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	plan, err := (&Planner{owner: owner, cfg: cfg, identityRef: "identity-ref"}).Plan(context.Background(), owner, desired, session)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if err := plan.Validate(desired); err != nil {
		t.Fatalf("plan validation failed: %v", err)
	}
	if fake.zonesRead {
		t.Fatal("full-tunnel acquisition unexpectedly requested split DNS zones")
	}
	if got := plan.WireGuard.Peers[0].Endpoint; got != "wg.example.test:51820" {
		t.Fatalf("endpoint = %q", got)
	}
	got := plan.WireGuard.Peers[0].AllowedPrefixes
	if len(got) != 3 || got[0].String() != "0.0.0.0/1" || got[1].String() != "10.200.0.1/32" || got[2].String() != "128.0.0.0/1" {
		t.Fatalf("allowed prefixes = %v", got)
	}

	fake.response.IsExitNode = false
	_, err = provider.Acquire(context.Background(), owner, desired)
	structured, ok := controller.AsError(err)
	if !ok || structured.Code != controller.ErrorCodeServiceUnavailable ||
		structured.Detail != "control plane returned a tunnel mode different from the requested mode" {
		t.Fatalf("mismatched VPN response error = %v", err)
	}
}

func TestPlannerRedactsInvalidSessionEndpoint(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	planner := &Planner{owner: owner, cfg: testClientConfig(t), identityRef: "identity-ref"}
	desired := controller.DesiredState{ConfigurationID: "cfg", Generation: 1, GroupID: "group", ExitNodeID: "exit", Connected: true}
	session := &acquiredSession{
		owner: owner, generation: 1, credential: "psk-ref", allowed: []string{"0.0.0.0/0"},
		exitNode: true, brokerEndpoint: "private sensitive endpoint",
	}

	_, err := planner.Plan(context.Background(), owner, desired, session)
	structured, ok := controller.AsError(err)
	if !ok || structured.Code != controller.ErrorCodeServiceUnavailable ||
		structured.Detail != "control plane returned an invalid WireGuard endpoint" ||
		strings.Contains(err.Error(), session.brokerEndpoint) {
		t.Fatalf("Plan() error = %v", err)
	}
}

func TestSessionProviderMapsMissingTokenToReauthRequired(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	provider := &sessionProvider{
		owner: owner, baseURL: "https://control.example", secrets: newMemorySecrets(),
		tokens:    func(context.Context) (string, error) { return "", nil },
		newClient: func(_, _ string) sessionAPI { return &fakeSessionAPI{} },
	}
	_, err := provider.Acquire(context.Background(), owner, controller.DesiredState{Generation: 1, GroupID: "group", Connected: true})
	structured, ok := controller.AsError(err)
	if !ok || structured.Code != controller.ErrorCodeReauthRequired {
		t.Fatalf("Acquire() error = %v, want REAUTH_REQUIRED", err)
	}
}

func TestWireGuardHealthRequiresRecentHandshake(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	now := time.Now()
	backend := &fakeTunnelBackend{status: &tunnel.StatusInfo{LastHandshake: now.Add(-time.Minute)}}
	observer := &fakeDarwinObserver{device: "utun7", present: true}
	device := &WireGuardDevice{
		owner: owner, cfg: testClientConfig(t), backend: backend, secrets: newMemorySecrets(), observer: observer,
		expectConnected: true, ownedInterface: "utun7", handshakeMaxAge: 3 * time.Minute, healthWait: time.Nanosecond,
		retryInterval: time.Nanosecond, now: func() time.Time { return now },
	}
	health, err := device.Health(context.Background(), owner)
	if err != nil || controller.PortHealth(health).Status != controller.HealthHealthy {
		t.Fatalf("recent health = %#v, %v", health, err)
	}
	backend.status.LastHandshake = now.Add(-4 * time.Minute)
	health, err = device.Health(context.Background(), owner)
	if err != nil || controller.PortHealth(health).Status != controller.HealthUnhealthy {
		t.Fatalf("stale health = %#v, %v", health, err)
	}
}

func TestTruthGateRequiresBrokerReachability(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	desired := controller.DesiredState{ConfigurationID: "cfg", Generation: 1, GroupID: "group", Connected: true}
	applied := controller.AppliedState{ConfigurationID: "cfg", Generation: 1, GroupID: "group", Connected: true}
	observation := healthyObservation()
	gate := &TruthGate{
		owner: owner, brokerOverlayIP: "10.200.0.1",
		probe:         func(context.Context, string) bool { return false },
		verifyTimeout: time.Nanosecond, retryInterval: time.Nanosecond,
	}

	truth, err := gate.Verify(context.Background(), owner, desired, applied, observation)
	if err != nil || truth.State != controller.ConnectionStateDegraded || truth.Health.Healthy {
		t.Fatalf("unreachable truth = %#v, %v", truth, err)
	}
	gate.probe = func(context.Context, string) bool { return true }
	truth, err = gate.Verify(context.Background(), owner, desired, applied, observation)
	if err != nil || truth.State != controller.ConnectionStateConnected || !truth.Health.Healthy {
		t.Fatalf("reachable truth = %#v, %v", truth, err)
	}
}

func TestAdaptersApplyAgainstFakesWithoutHostMutation(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	cfg := testClientConfig(t)
	secrets := newMemorySecrets()
	secrets.putValue(owner, "identity", []byte(cfg.PrivateKey))
	secrets.putValue(owner, "psk", []byte(mustKey(t)))
	backend := &fakeTunnelBackend{status: &tunnel.StatusInfo{LastHandshake: time.Now()}}
	observer := &fakeDarwinObserver{device: "utun7", present: true}
	device := &WireGuardDevice{owner: owner, cfg: cfg, secrets: secrets, backend: backend, observer: observer, handshakeMaxAge: time.Minute, now: time.Now}
	wg := controller.WireGuardConfig{
		ConfigurationID: "cfg", Generation: 1, InterfaceID: "wg-wireztna",
		Addresses: []netip.Prefix{netip.MustParsePrefix("10.200.1.2/32")}, Identity: "identity",
		Peers: []controller.WireGuardPeer{{PublicKey: cfg.BrokerPublicKey, Credential: "psk", Endpoint: cfg.BrokerEndpoint, AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}},
	}
	undo, err := device.Apply(context.Background(), owner, wg)
	if err != nil || backend.upCount != 1 || undo.Owner() != owner {
		t.Fatalf("WireGuard Apply() undo=%q up=%d err=%v", undo.Owner(), backend.upCount, err)
	}

	dnsBackend := newResolverDNSBackend(t.TempDir())
	dnsManager := &DNSManager{owner: owner, backend: dnsBackend}
	dnsConfig := controller.DNSConfig{Servers: []netip.Addr{netip.MustParseAddr("10.200.0.1")}, MatchDomains: []string{"internal.example"}}
	if _, err := dnsManager.Apply(context.Background(), owner, dnsConfig); err != nil {
		t.Fatalf("DNS Apply() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dnsBackend.dir, "internal.example"))
	if err != nil || string(data) != string(resolverContent(owner, "internal.example", dnsConfig.Servers[0])) {
		t.Fatalf("resolver content = %q, %v", data, err)
	}
}

func TestResolveServicePathsRejectsUnsafePaths(t *testing.T) {
	uid := uint64(os.Getuid())
	if uid == 0 {
		t.Skip("owner safety test requires a non-root test user")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("api_url: https://control.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveServicePaths(uid, "relative", ""); err == nil {
		t.Fatal("relative config directory was accepted")
	}
	unsafeJournalDir := t.TempDir()
	if err := os.Chmod(unsafeJournalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveServicePaths(uid, dir, filepath.Join(unsafeJournalDir, "journal.json")); err == nil {
		t.Fatal("journal in non-private directory was accepted")
	}
	link := filepath.Join(filepath.Dir(dir), "config-link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveServicePaths(uid, link, ""); err == nil {
		t.Fatal("symlink config directory was accepted")
	}
}

func healthyObservation() controller.HealthObservation {
	return controller.HealthObservation{
		WireGuard: controller.WireGuardHealth{Status: controller.HealthHealthy},
		Routes:    controller.RouteHealth{Status: controller.HealthHealthy},
		DNS:       controller.DNSHealth{Status: controller.HealthHealthy},
	}
}

func testClientConfig(t *testing.T) *config.ClientConfig {
	t.Helper()
	return &config.ClientConfig{
		APIURL: "https://control.example", PrivateKey: mustKey(t), OverlayIP: "10.200.1.2/32",
		BrokerPublicKey: mustKey(t), BrokerEndpoint: "broker.example:51820", BrokerOverlayIP: "10.200.0.1",
		AllowedIPs: []string{"10.20.0.0/16"}, TunnelDNS: "10.200.0.1", Interface: "wg-wireztna",
	}
}

func mustKey(t *testing.T) string {
	t.Helper()
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key.String()
}

func validToken() string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix())))
	return header + "." + payload + ".signature"
}

type fakeSessionAPI struct {
	response  *api.SessionResponse
	zones     []string
	renewErr  error
	zonesErr  error
	renewed   bool
	zonesRead bool
}

func (f *fakeSessionAPI) RenewSessionContext(context.Context, string, ...string) (*api.SessionResponse, error) {
	f.renewed = true
	return f.response, f.renewErr
}
func (f *fakeSessionAPI) GetDNSZonesContext(context.Context) ([]string, error) {
	f.zonesRead = true
	return append([]string(nil), f.zones...), f.zonesErr
}

type fakeTunnelHandle struct{ backend *fakeTunnelBackend }

func (h *fakeTunnelHandle) Up() error { h.backend.upCount++; return h.backend.upErr }
func (h *fakeTunnelHandle) Down()     { h.backend.downCount++ }

type fakeTunnelBackend struct {
	status              *tunnel.StatusInfo
	statusErr, upErr    error
	downErr             error
	upCount, downCount  int
	standaloneDownCount int
	lastConfig          tunnel.Config
}

func (f *fakeTunnelBackend) New(cfg tunnel.Config) (tunnelHandle, error) {
	f.lastConfig = cfg
	return &fakeTunnelHandle{backend: f}, nil
}
func (f *fakeTunnelBackend) Down(string, string) error {
	f.standaloneDownCount++
	return f.downErr
}
func (f *fakeTunnelBackend) Status(string) (*tunnel.StatusInfo, error) {
	return f.status, f.statusErr
}

type fakeDarwinObserver struct {
	device    string
	present   bool
	deviceErr error
	routes    []darwinRoute
	routesErr error
}

type fakeBrokerRouteStore struct {
	route *brokerRouteIdentity
	err   error
}

func (f *fakeBrokerRouteStore) Load() (*brokerRouteIdentity, error) {
	return cloneBrokerRoute(f.route), f.err
}

func (f *fakeDarwinObserver) Device(context.Context, darwinDeviceIdentity) (string, bool, error) {
	return f.device, f.present, f.deviceErr
}
func (f *fakeDarwinObserver) Routes(context.Context) ([]darwinRoute, error) {
	return append([]darwinRoute(nil), f.routes...), f.routesErr
}

func TestBaseConfigRejectsPrivilegedWGQuickInjectionInputs(t *testing.T) {
	for name, mutate := range map[string]func(*config.ClientConfig){
		"multiline endpoint": func(cfg *config.ClientConfig) {
			cfg.BrokerEndpoint = "broker.example:51820\nPreUp = touch /tmp/pwned"
		},
		"nonnumeric port":        func(cfg *config.ClientConfig) { cfg.BrokerEndpoint = "broker.example:wireguard" },
		"interface traversal":    func(cfg *config.ClientConfig) { cfg.Interface = "../victim" },
		"missing broker overlay": func(cfg *config.ClientConfig) { cfg.BrokerOverlayIP = "" },
		"CIDR broker overlay":    func(cfg *config.ClientConfig) { cfg.BrokerOverlayIP = "10.200.0.1/32" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testClientConfig(t)
			mutate(cfg)
			if err := ValidateClientConfig(cfg); err == nil {
				t.Fatal("unsafe privileged configuration was accepted")
			}
		})
	}
}

func TestPlannerRejectsEmptyRenewedPolicyInsteadOfFallingBack(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	cfg := testClientConfig(t)
	planner := &Planner{owner: owner, cfg: cfg, identityRef: "identity-ref"}
	desired := controller.DesiredState{ConfigurationID: "cfg", Generation: 9, GroupID: "group", Connected: true}
	session := &acquiredSession{owner: owner, generation: 9, credential: "psk-ref"}
	if _, err := planner.Plan(context.Background(), owner, desired, session); err == nil {
		t.Fatal("planner accepted an empty authoritative session policy")
	}
}

func TestTruthGateRefusesDisconnectedWhenResourcesRemain(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	desired := controller.DesiredState{ConfigurationID: "cfg", Generation: 1, Connected: false}
	applied := controller.AppliedState{ConfigurationID: "cfg", Generation: 1, Connected: false}
	observation := healthyObservation()
	observation.WireGuard = controller.WireGuardHealth{Status: controller.HealthUnhealthy}
	gate := &TruthGate{owner: owner, brokerOverlayIP: "10.200.0.1", probe: func(context.Context, string) bool { return false }}
	truth, err := gate.Verify(context.Background(), owner, desired, applied, observation)
	if err != nil || truth.State != controller.ConnectionStateDegraded {
		t.Fatalf("truth = %#v, %v, want degraded", truth, err)
	}
}

func TestTruthGateRetriesBrokerProbeUntilConverged(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	calls := 0
	gate := &TruthGate{
		owner: owner, brokerOverlayIP: "10.200.0.1", verifyTimeout: time.Second, retryInterval: time.Millisecond,
		probe: func(context.Context, string) bool { calls++; return calls == 2 },
	}
	desired := controller.DesiredState{ConfigurationID: "cfg", Generation: 1, GroupID: "group", Connected: true}
	applied := controller.AppliedState{ConfigurationID: "cfg", Generation: 1, GroupID: "group", Connected: true}
	truth, err := gate.Verify(context.Background(), owner, desired, applied, healthyObservation())
	if err != nil || truth.State != controller.ConnectionStateConnected || calls != 2 {
		t.Fatalf("truth=%#v calls=%d err=%v", truth, calls, err)
	}
}

func TestRouteManagerObservesExactOwnedWGQuickRoutes(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	prefixes := []netip.Prefix{
		netip.MustParsePrefix("10.20.0.0/16"),
		netip.MustParsePrefix("10.30.0.0/16"),
	}
	observer := &fakeDarwinObserver{device: "utun7", present: true}
	device := &WireGuardDevice{
		owner: owner, observer: observer, expectConnected: true,
		ownedInterface: "utun7", activeAllowedPrefixes: prefixes,
	}
	manager := &RouteManager{owner: owner, device: device, observer: observer}

	cases := []struct {
		name   string
		routes []darwinRoute
		want   controller.HealthStatus
	}{
		{
			name: "exact",
			routes: []darwinRoute{
				{destination: prefixes[0], interfaceID: "utun7"},
				{destination: prefixes[1], interfaceID: "utun7"},
			},
			want: controller.HealthHealthy,
		},
		{
			name:   "missing",
			routes: []darwinRoute{{destination: prefixes[0], interfaceID: "utun7"}},
			want:   controller.HealthUnhealthy,
		},
		{
			name: "foreign interface",
			routes: []darwinRoute{
				{destination: prefixes[0], interfaceID: "utun7"},
				{destination: prefixes[1], interfaceID: "utun9"},
			},
			want: controller.HealthUnhealthy,
		},
		{
			name: "unexpected owned route",
			routes: []darwinRoute{
				{destination: prefixes[0], interfaceID: "utun7"},
				{destination: prefixes[1], interfaceID: "utun7"},
				{destination: netip.MustParsePrefix("10.40.0.0/16"), interfaceID: "utun7"},
			},
			want: controller.HealthUnhealthy,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			observer.routes = test.routes
			health, err := manager.Health(context.Background(), owner)
			if err != nil || controller.PortHealth(health).Status != test.want {
				t.Fatalf("Health() = %#v, %v, want %s", health, err, test.want)
			}
		})
	}
}

func TestRouteManagerVerifiesDisconnectedRouteAbsence(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	prefix := netip.MustParsePrefix("10.20.0.0/16")
	observer := &fakeDarwinObserver{
		present: false,
		routes:  []darwinRoute{{destination: prefix, interfaceID: "utun7"}},
	}
	device := &WireGuardDevice{
		owner: owner, observer: observer, ownedInterface: "utun7",
		activeAllowedPrefixes: []netip.Prefix{prefix}, expectConnected: false,
	}
	manager := &RouteManager{owner: owner, device: device, observer: observer}
	health, err := manager.Health(context.Background(), owner)
	if err != nil || controller.PortHealth(health).Status != controller.HealthUnhealthy {
		t.Fatalf("residual route health = %#v, %v", health, err)
	}
	observer.routes = nil
	health, err = manager.Health(context.Background(), owner)
	if err != nil || controller.PortHealth(health).Status != controller.HealthHealthy {
		t.Fatalf("clean disconnect health = %#v, %v", health, err)
	}
}

func TestWireGuardApplyStoresOwnedUtunAndDisconnectRequiresAbsence(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	cfg := testClientConfig(t)
	secrets := newMemorySecrets()
	secrets.putValue(owner, "identity", []byte(cfg.PrivateKey))
	secrets.putValue(owner, "psk", []byte(mustKey(t)))
	observer := &fakeDarwinObserver{device: "utun12", present: true}
	backend := &fakeTunnelBackend{}
	device := &WireGuardDevice{owner: owner, cfg: cfg, secrets: secrets, backend: backend, observer: observer}
	wg := controller.WireGuardConfig{
		InterfaceID: "wg-wireztna", Addresses: []netip.Prefix{netip.MustParsePrefix(cfg.OverlayIP)}, Identity: "identity",
		Peers: []controller.WireGuardPeer{{PublicKey: cfg.BrokerPublicKey, Credential: "psk", Endpoint: cfg.BrokerEndpoint, AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}},
	}
	if _, err := device.Apply(context.Background(), owner, wg); err != nil {
		t.Fatalf("connected Apply() error = %v", err)
	}
	state := device.routeState()
	if state.ownedInterface != "utun12" || len(state.allowedPrefixes) != 1 || !state.connected {
		t.Fatalf("active state = %#v", state)
	}
	if undo, err := device.Apply(context.Background(), owner, controller.WireGuardConfig{}); err == nil || undo.Owner() != owner {
		t.Fatalf("disconnect with present device undo=%q err=%v", undo.Owner(), err)
	}
	if !device.routeState().connected {
		t.Fatal("failed disconnect erased connected truth")
	}
	observer.present = false
	if _, err := device.Apply(context.Background(), owner, controller.WireGuardConfig{}); err != nil {
		t.Fatalf("disconnect Apply() error = %v", err)
	}
	if device.routeState().connected {
		t.Fatal("successful disconnect retained connected truth")
	}
}

func TestDNSOwnerIsolationExactHealthAndDisconnectedCleanup(t *testing.T) {
	dir := t.TempDir()
	owner := controller.OwnerID("uid:501")
	foreignOwner := controller.OwnerID("uid:777")
	server := netip.MustParseAddr("10.200.0.1")
	foreignPath := filepath.Join(dir, "foreign.example")
	foreignContent := resolverContent(foreignOwner, "foreign.example", server)
	if err := os.WriteFile(foreignPath, foreignContent, 0o644); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(dir, "legacy.example")
	legacyContent := []byte("# wireztna-managed\nnameserver 192.0.2.53\ntimeout 2\n")
	if err := os.WriteFile(legacyPath, legacyContent, 0o644); err != nil {
		t.Fatal(err)
	}
	backend := newResolverDNSBackend(dir)
	manager := &DNSManager{owner: owner, backend: backend}
	desired := controller.DNSConfig{Servers: []netip.Addr{server}, MatchDomains: []string{"Internal.Example."}}
	if _, err := manager.Apply(context.Background(), owner, desired); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	for path, want := range map[string][]byte{foreignPath: foreignContent, legacyPath: legacyContent} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("foreign resolver %s changed: %q, %v", path, got, err)
		}
	}
	health, err := manager.Health(context.Background(), owner)
	if err != nil || controller.PortHealth(health).Status != controller.HealthHealthy {
		t.Fatalf("exact DNS health = %#v, %v", health, err)
	}
	extra := filepath.Join(dir, "extra.example")
	if err := os.WriteFile(extra, resolverContent(owner, "extra.example", server), 0o644); err != nil {
		t.Fatal(err)
	}
	health, err = manager.Health(context.Background(), owner)
	if err != nil || controller.PortHealth(health).Status != controller.HealthUnhealthy {
		t.Fatalf("extra owner file health = %#v, %v", health, err)
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(context.Background(), owner, controller.DNSConfig{}); err != nil {
		t.Fatalf("disconnected DNS cleanup error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal.example")); !os.IsNotExist(err) {
		t.Fatalf("owner resolver remained after disconnect: %v", err)
	}
	if got, err := os.ReadFile(foreignPath); err != nil || string(got) != string(foreignContent) {
		t.Fatalf("foreign owner resolver removed or changed: %q, %v", got, err)
	}
}

func TestDNSPartialFailureRollsBackExactPriorState(t *testing.T) {
	dir := t.TempDir()
	owner := controller.OwnerID("uid:501")
	server := netip.MustParseAddr("10.200.0.1")
	backend := newResolverDNSBackend(dir)
	manager := &DNSManager{owner: owner, backend: backend}
	prior := controller.DNSConfig{Servers: []netip.Addr{server}, MatchDomains: []string{"prior.example"}}
	if _, err := manager.Apply(context.Background(), owner, prior); err != nil {
		t.Fatal(err)
	}
	priorBytes, err := os.ReadFile(filepath.Join(dir, "prior.example"))
	if err != nil {
		t.Fatal(err)
	}
	write := backend.writeFile
	calls := 0
	backend.writeFile = func(path string, data []byte, mode os.FileMode) error {
		calls++
		if calls == 2 {
			return fmt.Errorf("injected write failure")
		}
		return write(path, data, mode)
	}
	desired := controller.DNSConfig{Servers: []netip.Addr{server}, MatchDomains: []string{"new-a.example", "new-b.example"}}
	undo, err := manager.Apply(context.Background(), owner, desired)
	if err == nil || undo.Owner() != owner {
		t.Fatalf("partial Apply() undo=%q err=%v", undo.Owner(), err)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, "prior.example"))
	if readErr != nil || string(got) != string(priorBytes) {
		t.Fatalf("prior state was not restored: %q, %v", got, readErr)
	}
	for _, name := range []string{"new-a.example", "new-b.example"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("partial resolver %q remained: %v", name, statErr)
		}
	}
	if err := undo.Revert(context.Background()); err != nil {
		t.Fatalf("Undo.Revert() error = %v", err)
	}
}

func TestDarwinNetstatObserverPreservesGatewayAndPhysicalInterface(t *testing.T) {
	route, ok := parseDarwinNetstatLine("192.0.2.10/32 192.0.2.1 UGHS  en0", false)
	if !ok {
		t.Fatal("physical netstat route was not parsed")
	}
	if route.destination.String() != "192.0.2.10/32" || route.gateway.String() != "192.0.2.1" || route.interfaceID != "en0" {
		t.Fatalf("parsed route = %#v", route)
	}
}

func TestDarwinNetstatObserverInfersAbbreviatedBSDPrefixes(t *testing.T) {
	lines := []struct {
		line string
		want string
	}{
		{line: "10  utun4  USc  utun4", want: "10.0.0.0/8"},
		{line: "172.20  utun4  USc  utun4", want: "172.20.0.0/16"},
		{line: "192.168.1  utun4  USc  utun4", want: "192.168.1.0/24"},
		{line: "10.200.1.8  10.200.1.8  UH  utun4", want: "10.200.1.8/32"},
		{line: "192.168.1/24  utun4  USc  utun4", want: "192.168.1.0/24"},
	}
	for _, test := range lines {
		route, ok := parseDarwinNetstatLine(test.line, false)
		if !ok || route.destination.String() != test.want {
			t.Fatalf("parseDarwinNetstatLine(%q) = %#v, %v; want %s", test.line, route, ok, test.want)
		}
	}
}

func TestRouteManagerAcceptsCapturedAbbreviatedBSDRouteSet(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	observed := make([]darwinRoute, 0, 3)
	for _, line := range []string{
		"10.200.0.1/32  utun4  USc  utun4",
		"10.200.1.8  10.200.1.8  UH  utun4",
		"192.168.1  utun4  USc  utun4",
	} {
		route, ok := parseDarwinNetstatLine(line, false)
		if !ok {
			t.Fatalf("captured netstat route was not parsed: %q", line)
		}
		observed = append(observed, route)
	}
	observer := &fakeDarwinObserver{device: "utun4", present: true, routes: observed}
	device := &WireGuardDevice{
		owner: owner, observer: observer, expectConnected: true, ownedInterface: "utun4",
		identity: darwinDeviceIdentity{overlayIP: netip.MustParseAddr("10.200.1.8")},
		activeAllowedPrefixes: []netip.Prefix{
			netip.MustParsePrefix("10.200.0.1/32"),
			netip.MustParsePrefix("192.168.1.0/24"),
		},
	}
	manager := &RouteManager{owner: owner, device: device, observer: observer}
	health, err := manager.Health(context.Background(), owner)
	if err != nil || controller.PortHealth(health).Status != controller.HealthHealthy {
		t.Fatalf("captured route health = %#v, %v; want healthy", health, err)
	}
}

func TestFullTunnelHealthRequiresExactBrokerExclusionAndDetectsRestartOrphan(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	brokerRoute := &brokerRouteIdentity{
		destination: netip.MustParsePrefix("192.0.2.10/32"),
		gateway:     netip.MustParseAddr("192.0.2.1"),
		interfaceID: "en0",
	}
	store := &fakeBrokerRouteStore{route: brokerRoute}
	prefixes := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/1"),
		netip.MustParsePrefix("128.0.0.0/1"),
	}
	observer := &fakeDarwinObserver{
		device: "utun7", present: true,
		routes: []darwinRoute{
			{destination: prefixes[0], interfaceID: "utun7"},
			{destination: prefixes[1], interfaceID: "utun7"},
			{destination: brokerRoute.destination, gateway: brokerRoute.gateway, interfaceID: brokerRoute.interfaceID},
		},
	}
	backend := &fakeTunnelBackend{status: &tunnel.StatusInfo{LastHandshake: time.Now()}}
	device := &WireGuardDevice{
		owner: owner, cfg: testClientConfig(t), backend: backend, observer: observer, routeStore: store,
		expectConnected: true, fullTunnel: true, ownedInterface: "utun7", activeAllowedPrefixes: prefixes,
		brokerRoute: brokerRoute, handshakeMaxAge: time.Minute, healthWait: time.Nanosecond, retryInterval: time.Nanosecond, now: time.Now,
	}
	manager := &RouteManager{owner: owner, device: device, observer: observer, routeStore: store}
	wgHealth, err := device.Health(context.Background(), owner)
	if err != nil || controller.PortHealth(wgHealth).Status != controller.HealthHealthy {
		t.Fatalf("exact full-tunnel WireGuard health = %#v, %v", wgHealth, err)
	}
	routeHealth, err := manager.Health(context.Background(), owner)
	if err != nil || controller.PortHealth(routeHealth).Status != controller.HealthHealthy {
		t.Fatalf("exact full-tunnel route health = %#v, %v", routeHealth, err)
	}

	observer.routes[2].gateway = netip.MustParseAddr("192.0.2.254")
	routeHealth, err = manager.Health(context.Background(), owner)
	if err != nil || controller.PortHealth(routeHealth).Status != controller.HealthUnhealthy {
		t.Fatalf("drifted broker route health = %#v, %v", routeHealth, err)
	}

	restarted := &WireGuardDevice{owner: owner, cfg: testClientConfig(t), observer: observer, routeStore: store, brokerRoute: brokerRoute}
	restartManager := &RouteManager{owner: owner, device: restarted, observer: observer, routeStore: store}
	routeHealth, err = restartManager.Health(context.Background(), owner)
	if err != nil || controller.PortHealth(routeHealth).Status != controller.HealthUnhealthy {
		t.Fatalf("restart orphan health = %#v, %v", routeHealth, err)
	}
}

func TestWireGuardApplyFullTunnelFailsClosedWithoutExactBrokerRoute(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	cfg := testClientConfig(t)
	secrets := newMemorySecrets()
	secrets.putValue(owner, "identity", []byte(cfg.PrivateKey))
	secrets.putValue(owner, "psk", []byte(mustKey(t)))
	observer := &fakeDarwinObserver{device: "utun12", present: true}
	store := &fakeBrokerRouteStore{}
	device := &WireGuardDevice{owner: owner, cfg: cfg, secrets: secrets, backend: &fakeTunnelBackend{}, observer: observer, routeStore: store}
	wg := controller.WireGuardConfig{
		InterfaceID: "wg-wireztna", Addresses: []netip.Prefix{netip.MustParsePrefix(cfg.OverlayIP)}, Identity: "identity",
		Peers: []controller.WireGuardPeer{{
			PublicKey: cfg.BrokerPublicKey, Credential: "psk", Endpoint: cfg.BrokerEndpoint,
			AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")},
		}},
	}
	undo, err := device.Apply(context.Background(), owner, wg)
	if err == nil || undo.Owner() != owner {
		t.Fatalf("full-tunnel Apply() undo=%q err=%v, want fail-closed", undo.Owner(), err)
	}
}

func TestSessionProviderDoesNotMapPolicyForbiddenToReauth(t *testing.T) {
	owner := controller.OwnerID("uid:501")
	fake := &fakeSessionAPI{renewErr: errors.New("HTTP 403: policy rejected")}
	provider := &sessionProvider{
		owner: owner, baseURL: "https://control.example", secrets: newMemorySecrets(),
		tokens: func(context.Context) (string, error) {
			return catalogTestJWT(t, time.Now().Add(time.Hour)), nil
		},
		newClient: func(_, _ string) sessionAPI { return fake },
	}
	_, err := provider.Acquire(context.Background(), owner, controller.DesiredState{Generation: 1, GroupID: "group", Connected: true})
	if err == nil {
		t.Fatal("Acquire() unexpectedly succeeded")
	}
	if structured, ok := controller.AsError(err); ok && structured.Code == controller.ErrorCodeReauthRequired {
		t.Fatalf("policy rejection was misclassified as REAUTH_REQUIRED: %v", err)
	}
}

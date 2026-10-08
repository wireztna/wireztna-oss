package tunnel

import (
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParseLinuxRoutesPreservesFamiliesAndRejectsDuplicateSingletons(t *testing.T) {
	t.Parallel()

	routes, err := ParseLinuxRoutes(readTunnelFixture(t, "standard.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 4 || routes[0].Destination != netip.MustParsePrefix("0.0.0.0/0") || routes[0].Table != 254 || !routes[3].OnLink {
		t.Fatalf("IPv4 routes = %#v", routes)
	}
	ipv6, err := ParseLinuxRoutesForFamily(readTunnelFixture(t, "ipv6.txt"), LinuxIPv6)
	if err != nil {
		t.Fatal(err)
	}
	if ipv6[0].Destination != netip.MustParsePrefix("::/0") || ipv6[0].Gateway != netip.MustParseAddr("2001:db8::1") {
		t.Fatalf("IPv6 default = %#v", ipv6[0])
	}

	for _, test := range []struct {
		name string
		text string
		want string
	}{
		{name: "via", text: "10.0.0.0/8 via 192.0.2.1 via 192.0.2.2", want: `duplicate route attribute "via"`},
		{name: "dev", text: "10.0.0.0/8 dev eth0 dev eth1", want: `duplicate route attribute "dev"`},
		{name: "table", text: "10.0.0.0/8 table 100 table 200", want: `duplicate route attribute "table"`},
		{name: "metric zero", text: "10.0.0.0/8 metric 0 metric 1", want: `duplicate route attribute "metric"`},
		{name: "flag", text: "10.0.0.0/8 dev eth0 onlink onlink", want: `duplicate route attribute "onlink"`},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseLinuxRoutes(test.text)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseLinuxRulesUsesExplicitFamilyAndExecutableIdentity(t *testing.T) {
	t.Parallel()

	v4, err := ParseLinuxRulesForFamily(readTunnelFixture(t, "rules-v4.txt"), LinuxIPv4)
	if err != nil {
		t.Fatal(err)
	}
	v6, err := ParseLinuxRulesForFamily(readTunnelFixture(t, "rules-v6.txt"), LinuxIPv6)
	if err != nil {
		t.Fatal(err)
	}
	if len(v4) != 5 || len(v6) != 4 {
		t.Fatalf("rule counts v4=%d v6=%d", len(v4), len(v6))
	}
	if v4[1].Family != LinuxIPv4 || v6[1].Family != LinuxIPv6 || v4[1].Priority != v6[1].Priority {
		t.Fatalf("families/priorities not preserved: v4=%#v v6=%#v", v4[1], v6[1])
	}
	if linuxRuleIdentity(v4[1]) == linuxRuleIdentity(v6[1]) {
		t.Fatal("IPv4 and IPv6 identities collided")
	}
	if v4[1].Priority != v4[2].Priority || linuxRuleIdentity(v4[1]) == linuxRuleIdentity(v4[2]) {
		t.Fatal("repeated preference was not preserved as distinct executable rules")
	}
	if !v4[2].SuppressPrefixLengthSet || v4[2].SuppressPrefixLength != 0 || !v4[2].FromSet || !v4[0].FromSet {
		t.Fatalf("zero/presence flags lost: %#v", v4)
	}
	if v4[1].Action != (LinuxRuleAction{Kind: LinuxRuleLookup, Table: 51820}) {
		t.Fatalf("lookup action = %#v", v4[1].Action)
	}
}

func TestParseLinuxRulesFailsClosed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		family LinuxAddressFamily
		text   string
		want   string
	}{
		{name: "without action", family: LinuxIPv4, text: "100: from all", want: "exactly one"},
		{name: "two actions", family: LinuxIPv4, text: "100: from all lookup main table 100", want: `duplicate rule attribute "lookup"`},
		{name: "duplicate from", family: LinuxIPv4, text: "100: from all from 10.0.0.0/8 lookup main", want: `duplicate rule attribute "from"`},
		{name: "duplicate not", family: LinuxIPv4, text: "100: not not from all lookup main", want: `duplicate rule attribute "not"`},
		{name: "duplicate fwmark", family: LinuxIPv4, text: "100: fwmark 0 fwmark 1 lookup main", want: `duplicate rule attribute "fwmark"`},
		{name: "wrong selector family", family: LinuxIPv4, text: "100: from 2001:db8::/32 lookup main", want: "address family"},
		{name: "IPv4 suppress overflow", family: LinuxIPv4, text: "100: from all lookup main suppress_prefixlength 33", want: "for IPv4"},
		{name: "IPv6 suppress overflow", family: LinuxIPv6, text: "100: from all lookup main suppress_prefixlength 129", want: "for IPv6"},
		{name: "unsupported", family: LinuxIPv4, text: "100: uidrange 1000-2000 lookup main", want: `unsupported token "uidrange"`},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseLinuxRulesForFamily(test.text, test.family)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	zero, err := ParseLinuxRulesForFamily("100: from all fwmark 0/0 lookup unspec suppress_prefixlength 0", LinuxIPv4)
	if err != nil {
		t.Fatal(err)
	}
	if !zero[0].FWMarkSet || zero[0].FWMark != 0 || zero[0].FWMask != 0 || zero[0].Action.Table != 0 || !zero[0].SuppressPrefixLengthSet {
		t.Fatalf("significant zero values lost: %#v", zero[0])
	}
}

func TestPlanLinuxTunnelValidatesRuleLookupTables(t *testing.T) {
	t.Parallel()

	intent := splitTunnelIntent()
	intent.DesiredRules = []LinuxRule{lookupRule(LinuxIPv4, 1000, 51820)}
	plan, err := PlanLinuxTunnel(intent, LinuxTunnelTopology{})
	if err != nil || len(plan.Journal) != 2 {
		t.Fatalf("populated table plan = %#v, error = %v", plan, err)
	}

	empty := splitTunnelIntent()
	empty.RouteTables = nil
	empty.DesiredRules = []LinuxRule{lookupRule(LinuxIPv4, 1000, 51820)}
	if plan, err := PlanLinuxTunnel(empty, LinuxTunnelTopology{}); err == nil || !strings.Contains(err.Error(), "neither populated nor observed safe") || len(plan.Journal) != 0 {
		t.Fatalf("empty-table plan = %#v, error = %v", plan, err)
	}

	observedSafe := LinuxTunnelTopology{SafeTables: []LinuxObservedTable{{Family: LinuxIPv4, Table: 51820, Fingerprint: "route-snapshot:v7"}}}
	plan, err = PlanLinuxTunnel(empty, observedSafe)
	if err != nil || len(plan.Journal) != 1 || plan.Journal[0].Apply.Resource != LinuxPlanRule {
		t.Fatalf("safe observed table plan = %#v, error = %v", plan, err)
	}
	if guards := plan.Journal[0].Apply.Guards; len(guards) != 1 || guards[0].Identity != linuxObservedTableIdentity(LinuxIPv4, 51820) ||
		guards[0].ExpectedFingerprint != "route-snapshot:v7" {
		t.Fatalf("safe table CAS guard = %#v", guards)
	}
}

func TestPlanLinuxTunnelSupportsRepeatedPrioritiesWithoutRuleReplace(t *testing.T) {
	t.Parallel()

	oldRule := lookupRule(LinuxIPv4, 1000, 51820)
	oldRule.FWMark, oldRule.FWMask, oldRule.FWMarkSet = 1, ^uint32(0), true
	newRule := lookupRule(LinuxIPv4, 1000, 51820)
	newRule.FWMark, newRule.FWMask, newRule.FWMarkSet = 2, ^uint32(0), true
	intent := splitTunnelIntent()
	intent.DesiredRules = []LinuxRule{newRule, lookupRule(LinuxIPv4, 1000, 51820)}
	topology := LinuxTunnelTopology{Rules: []LinuxObservedRule{{Rule: oldRule, Owner: intent.Owner}}}
	plan, err := PlanLinuxTunnel(intent, topology)
	if err != nil {
		t.Fatal(err)
	}
	var addCount, deleteCount int
	for _, entry := range plan.Journal {
		if entry.Apply.Resource != LinuxPlanRule {
			continue
		}
		if entry.Apply.Action == LinuxPlanReplace {
			t.Fatalf("planner emitted impossible rule replace: %#v", entry.Apply)
		}
		if entry.Apply.Action == LinuxPlanAdd {
			addCount++
		}
		if entry.Apply.Action == LinuxPlanDelete {
			deleteCount++
		}
	}
	if addCount != 2 || deleteCount != 1 {
		t.Fatalf("rule mutations add=%d delete=%d plan=%#v", addCount, deleteCount, plan)
	}
}

func TestPlanLinuxTunnelFullTunnelConsumesEffectiveLookup(t *testing.T) {
	t.Parallel()

	intent, topology := fullTunnelInputs()
	plan, err := PlanLinuxTunnel(intent, topology)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Journal) != 4 {
		t.Fatalf("journal = %#v", plan.Journal)
	}
	exclusion := plan.Journal[0].Apply
	if topology.BrokerLookup.Table != 254 || exclusion.Role != LinuxRouteRoleBrokerExclusion || exclusion.Phase != LinuxPhasePrepareExclusion || exclusion.Route.Table != 51820 ||
		exclusion.Route.Destination != netip.MustParsePrefix("203.0.113.9/32") || exclusion.Route.Device != "wlan0" || exclusion.Route.Gateway != netip.MustParseAddr("198.51.100.1") {
		t.Fatalf("exclusion did not decouple effective source lookup from install table: lookup=%#v operation=%#v", topology.BrokerLookup, exclusion)
	}
	if len(exclusion.Guards) != 1 || exclusion.Guards[0].ExpectedFingerprint != topology.BrokerLookup.Fingerprint {
		t.Fatalf("broker lookup CAS guard = %#v", exclusion.Guards)
	}
	for _, entry := range plan.Journal {
		if entry.Apply.Resource == LinuxPlanRule && len(entry.Apply.DependsOn) < 2 {
			t.Fatalf("rule does not depend on populated routes: %#v", entry.Apply)
		}
	}

	cases := []struct {
		name   string
		mutate func(*LinuxTunnelIntent, *LinuxTunnelTopology)
		want   string
	}{
		{name: "missing observation", mutate: func(_ *LinuxTunnelIntent, topology *LinuxTunnelTopology) { topology.BrokerLookup = nil }, want: "explicit effective broker lookup"},
		{name: "wrong netns", mutate: func(_ *LinuxTunnelIntent, topology *LinuxTunnelTopology) { topology.BrokerLookup.NetNS = "other" }, want: "namespace"},
		{name: "missing lookup fingerprint", mutate: func(_ *LinuxTunnelIntent, topology *LinuxTunnelTopology) {
			topology.BrokerLookup.Fingerprint = ""
		}, want: "requires observation fingerprint"},
		{name: "stale lookup fingerprint", mutate: func(_ *LinuxTunnelIntent, topology *LinuxTunnelTopology) {
			topology.BrokerLookup.Route.Gateway = netip.MustParseAddr("192.0.2.1")
		}, want: "fingerprint does not match"},
		{name: "unauthorized uplink", mutate: func(intent *LinuxTunnelIntent, _ *LinuxTunnelTopology) { intent.AuthorizedUplinks = nil }, want: "not authorized"},
		{name: "VPN uplink", mutate: func(intent *LinuxTunnelIntent, topology *LinuxTunnelTopology) {
			topology.BrokerLookup.Uplink.Kind = "wireguard"
			intent.AuthorizedUplinks = []LinuxUplinkIdentity{topology.BrokerLookup.Uplink}
		}, want: "VPN or virtual"},
		{name: "VRF master", mutate: func(intent *LinuxTunnelIntent, topology *LinuxTunnelTopology) {
			topology.BrokerLookup.Uplink.Master = "vrf-blue"
			intent.AuthorizedUplinks = []LinuxUplinkIdentity{topology.BrokerLookup.Uplink}
		}, want: "not safe"},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			intent, topology := fullTunnelInputs()
			test.mutate(&intent, &topology)
			plan, err := PlanLinuxTunnel(intent, topology)
			if err == nil || !strings.Contains(err.Error(), test.want) || len(plan.Journal) != 0 {
				t.Fatalf("plan = %#v error = %v, want %q", plan, err, test.want)
			}
		})
	}
}

func TestPlanLinuxTunnelDAGOrdersActivationWithdrawalAndRollback(t *testing.T) {
	t.Parallel()

	intent := splitTunnelIntent()
	rule := lookupRule(LinuxIPv4, 1000, 51820)
	intent.DesiredRules = []LinuxRule{rule}
	activation, err := PlanLinuxTunnel(intent, LinuxTunnelTopology{})
	if err != nil {
		t.Fatal(err)
	}
	if activation.Journal[0].Apply.Resource != LinuxPlanRoute || activation.Journal[1].Apply.Resource != LinuxPlanRule ||
		!contains(activation.Journal[1].Apply.DependsOn, activation.Journal[0].Apply.ID) {
		t.Fatalf("activation dependency missing: %#v", activation.Journal)
	}
	if activation.Rollback[0].Resource != LinuxPlanRule || activation.Rollback[1].Resource != LinuxPlanRoute ||
		!contains(activation.Rollback[1].DependsOn, activation.Rollback[0].ID) {
		t.Fatalf("activation rollback dependency missing: %#v", activation.Rollback)
	}

	route := activation.Journal[0].Apply.Route
	withdrawIntent := splitTunnelIntent()
	withdrawIntent.RouteTables = nil
	withdrawal, err := PlanLinuxTunnel(withdrawIntent, LinuxTunnelTopology{
		Routes: []LinuxObservedRoute{{Route: route, Owner: withdrawIntent.Owner}},
		Rules:  []LinuxObservedRule{{Rule: rule, Owner: withdrawIntent.Owner}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if withdrawal.Journal[0].Apply.Resource != LinuxPlanRule || withdrawal.Journal[0].Apply.Phase != LinuxPhaseWithdrawRules ||
		withdrawal.Journal[1].Apply.Resource != LinuxPlanRoute || withdrawal.Journal[1].Apply.Phase != LinuxPhaseWithdrawRoutes ||
		!contains(withdrawal.Journal[1].Apply.DependsOn, withdrawal.Journal[0].Apply.ID) {
		t.Fatalf("withdrawal dependency missing: %#v", withdrawal.Journal)
	}
	if withdrawal.Rollback[0].Action != LinuxPlanAdd || withdrawal.Rollback[0].Resource != LinuxPlanRoute ||
		withdrawal.Rollback[1].Action != LinuxPlanAdd || withdrawal.Rollback[1].Resource != LinuxPlanRule ||
		!contains(withdrawal.Rollback[1].DependsOn, withdrawal.Rollback[0].ID) {
		t.Fatalf("withdrawal rollback dependency missing: %#v", withdrawal.Rollback)
	}
}

func TestPlanLinuxTunnelOperationsCarryStableCASAndLedgerContract(t *testing.T) {
	t.Parallel()

	intent := splitTunnelIntent()
	first, err := PlanLinuxTunnel(intent, LinuxTunnelTopology{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanLinuxTunnel(intent, LinuxTunnelTopology{})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("plans differ: first=%#v second=%#v error=%v", first, second, err)
	}
	if !first.LedgerRequired || len(first.Journal) != 1 {
		t.Fatalf("ledger/journal = %#v", first)
	}
	apply, rollback := first.Journal[0].Apply, first.Journal[0].Rollback
	if apply.ID == "" || apply.Identity == "" || !apply.Ledger.Required || apply.Precondition.Identity != apply.Identity ||
		apply.Precondition.MustExist || apply.ResultFingerprint == "" || apply.Ledger.AfterFingerprint != apply.ResultFingerprint {
		t.Fatalf("apply CAS contract = %#v", apply)
	}
	if !rollback.Precondition.MustExist || rollback.Precondition.ExpectedFingerprint != apply.ResultFingerprint ||
		rollback.Precondition.ExpectedOwner != intent.Owner || rollback.Ledger.BeforeFingerprint != apply.ResultFingerprint {
		t.Fatalf("rollback CAS contract = %#v", rollback)
	}
	concurrent := apply.Route
	concurrent.Device = "other0"
	if linuxRouteFingerprint(concurrent) == rollback.Precondition.ExpectedFingerprint {
		t.Fatal("concurrent route mutation would not be detected by rollback fingerprint")
	}
}

func TestPlanLinuxTunnelIdempotenceAndForeignEquivalentState(t *testing.T) {
	t.Parallel()

	intent := splitTunnelIntent()
	initial, err := PlanLinuxTunnel(intent, LinuxTunnelTopology{})
	if err != nil {
		t.Fatal(err)
	}
	desired := initial.Journal[0].Apply.Route
	for _, test := range []struct {
		owner string
		owned bool
	}{
		{owner: intent.Owner, owned: true},
		{owner: "other-vpn", owned: false},
	} {
		plan, err := PlanLinuxTunnel(intent, LinuxTunnelTopology{Routes: []LinuxObservedRoute{{Route: desired, Owner: test.owner}}})
		if err != nil || len(plan.Journal) != 0 || len(plan.Satisfied) != 1 || plan.Satisfied[0].Owned != test.owned {
			t.Fatalf("owner %q plan=%#v error=%v", test.owner, plan, err)
		}
	}
}

func splitTunnelIntent() LinuxTunnelIntent {
	return LinuxTunnelIntent{
		Owner: "session-7", InterfaceName: "zt-corp0", Broker: netip.MustParseAddr("203.0.113.9"),
		RouteTables: []LinuxRouteTableIntent{{Family: LinuxIPv4, Table: 51820, Prefixes: []netip.Prefix{netip.MustParsePrefix("10.50.0.0/16")}}},
	}
}

func fullTunnelInputs() (LinuxTunnelIntent, LinuxTunnelTopology) {
	uplink := LinuxUplinkIdentity{IfIndex: 7, InterfaceName: "wlan0", Kind: "wifi", StableID: "pci-0000:03:00.0"}
	intent := LinuxTunnelIntent{
		Owner: "session-7", InterfaceName: "zt-corp0", Broker: netip.MustParseAddr("203.0.113.9"), NetworkNamespace: "root@boot-a",
		RouteTables: []LinuxRouteTableIntent{{Family: LinuxIPv4, Table: 51820, Prefixes: []netip.Prefix{
			netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("10.50.0.0/16"),
		}}},
		FullTunnel: true, DesiredRules: []LinuxRule{lookupRule(LinuxIPv4, 1000, 51820)}, AuthorizedUplinks: []LinuxUplinkIdentity{uplink},
	}
	lookup := &LinuxBrokerLookup{
		Broker: intent.Broker, Family: LinuxIPv4, Table: 254, NetNS: intent.NetworkNamespace, Uplink: uplink,
		Route: LinuxRoute{Type: "unicast", Destination: netip.MustParsePrefix("203.0.113.0/24"), Gateway: netip.MustParseAddr("198.51.100.1"), Device: "wlan0", Table: 254, Protocol: "dhcp", Scope: "global", Metric: 20, MetricSet: true},
	}
	lookup.Fingerprint = linuxBrokerLookupFingerprint(*lookup)
	topology := LinuxTunnelTopology{BrokerLookup: lookup}
	return intent, topology
}

func lookupRule(family LinuxAddressFamily, priority, table uint32) LinuxRule {
	return LinuxRule{Family: family, Priority: priority, FromSet: true, Action: LinuxRuleAction{Kind: LinuxRuleLookup, Table: table}}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func readTunnelFixture(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile("testdata/ip-route/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(contents)
}

func TestPlanLinuxTunnelRejectsMultipleBrokerFamilyDefaultTables(t *testing.T) {
	t.Parallel()

	intent, topology := fullTunnelInputs()
	intent.RouteTables = append(intent.RouteTables, LinuxRouteTableIntent{
		Family: LinuxIPv4, Table: 100, Prefixes: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
	})
	plan, err := PlanLinuxTunnel(intent, topology)
	if err == nil || !strings.Contains(err.Error(), "multiple default tables") || len(plan.Journal) != 0 {
		t.Fatalf("ambiguous defaults plan=%#v error=%v", plan, err)
	}
}

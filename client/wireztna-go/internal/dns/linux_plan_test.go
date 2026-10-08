package dns

import (
	"encoding/json"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestPlanLinuxDNSUsesOneCompleteSnapshotOperation(t *testing.T) {
	t.Parallel()

	topology := readDNSTopologyFixture(t, "resolved-empty.json")
	intent := LinuxDNSIntent{
		Owner: "session-7", Link: resolvedIdentity(),
		Servers:       []netip.Addr{netip.MustParseAddr("2001:db8::53"), netip.MustParseAddr("10.200.0.1")},
		SearchDomains: []string{"Corp.Example."}, MatchDomains: []string{"apps.corp.example"}, DefaultRoute: true,
	}
	before := cloneDNSTopology(topology)
	first, err := PlanLinuxDNS(intent, topology)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanLinuxDNS(intent, topology)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("plans differ: first=%#v second=%#v error=%v", first, second, err)
	}
	if !reflect.DeepEqual(topology, before) {
		t.Fatal("planner mutated topology")
	}
	if len(first.Journal) != 1 || len(first.Rollback) != 1 || !first.LedgerRequired {
		t.Fatalf("plan = %#v", first)
	}
	apply := first.Journal[0].Apply
	if apply.Action != LinuxDNSSetLinkState || apply.Identity != resolvedIdentity() || !apply.State.DefaultRoute {
		t.Fatalf("apply operation = %#v", apply)
	}
	if got := apply.State.Servers; !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("10.200.0.1"), netip.MustParseAddr("2001:db8::53")}) {
		t.Fatalf("servers = %#v", got)
	}
	if got := apply.State.Domains; !reflect.DeepEqual(got, []LinuxDNSDomain{{Name: "apps.corp.example", RouteOnly: true}, {Name: "corp.example", RouteOnly: false}}) {
		t.Fatalf("domains = %#v", got)
	}
	rollback := first.Journal[0].Rollback
	if rollback.Action != LinuxDNSRestoreLinkState || len(rollback.State.Servers) != 0 || len(rollback.State.Domains) != 0 || rollback.State.DefaultRoute || rollback.ResultOwner != "" {
		t.Fatalf("rollback snapshot = %#v", rollback)
	}
}

func TestPlanLinuxDNSExitNodeUsesSemanticRootRouteOnly(t *testing.T) {
	t.Parallel()

	plan, err := PlanLinuxDNS(LinuxDNSIntent{
		Owner: "session-exit", Link: resolvedIdentity(), Servers: []netip.Addr{netip.MustParseAddr("10.200.0.1")},
		MatchDomains: []string{"."}, DefaultRoute: true,
	}, readDNSTopologyFixture(t, "resolved-empty.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Journal) != 1 || !reflect.DeepEqual(plan.Journal[0].Apply.State.Domains, []LinuxDNSDomain{{Name: ".", RouteOnly: true}}) ||
		!plan.Journal[0].Apply.State.DefaultRoute {
		t.Fatalf("exit-node plan = %#v", plan)
	}
	if strings.Contains(linuxDNSStateKey(plan.Journal[0].Apply.State), "~.") {
		t.Fatal("planner leaked backend-specific ~. rendering")
	}
}

func TestPlanLinuxDNSRejectsRootSearchAndBackendSyntax(t *testing.T) {
	t.Parallel()

	topology := readDNSTopologyFixture(t, "resolved-empty.json")
	for _, test := range []struct {
		name   string
		search []string
		match  []string
		want   string
	}{
		{name: "root search", search: []string{"."}, want: "only as RouteOnly"},
		{name: "resolved route syntax", match: []string{"~."}, want: "backend routing prefixes"},
		{name: "route prefix", match: []string{"~corp.example"}, want: "backend routing prefixes"},
		{name: "duplicate normalized", search: []string{"Corp.Example"}, match: []string{"corp.example."}, want: "duplicate DNS domain"},
		{name: "traversal", match: []string{"../corp.example"}, want: "traversal"},
		{name: "IP literal", match: []string{"10.200.0.1"}, want: "IP literals"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan, err := PlanLinuxDNS(LinuxDNSIntent{
				Owner: "session-7", Link: resolvedIdentity(), Servers: []netip.Addr{netip.MustParseAddr("10.200.0.1")},
				SearchDomains: test.search, MatchDomains: test.match,
			}, topology)
			if err == nil || !strings.Contains(err.Error(), test.want) || len(plan.Journal) != 0 {
				t.Fatalf("plan=%#v error=%v want=%q", plan, err, test.want)
			}
		})
	}
}

func TestPlanLinuxDNSRequiresBackendStableIdentity(t *testing.T) {
	t.Parallel()

	resolved := readDNSTopologyFixture(t, "resolved-empty.json")
	intent := baseDNSIntent(resolvedIdentity())
	wrongIfIndex := intent
	wrongIfIndex.Link.IfIndex = 43
	if plan, err := PlanLinuxDNS(wrongIfIndex, resolved); err == nil || !strings.Contains(err.Error(), "no supported link") || len(plan.Journal) != 0 {
		t.Fatalf("reused-name resolved plan=%#v error=%v", plan, err)
	}
	renamedResolved := cloneDNSTopology(resolved)
	renamedResolved.Links[1].Identity.InterfaceName = "zt-renamed"
	plan, err := PlanLinuxDNS(intent, renamedResolved)
	if err != nil || plan.Identity.InterfaceName != "zt-renamed" || plan.Identity.IfIndex != resolvedIdentity().IfIndex {
		t.Fatalf("resolved stable ifindex did not survive rename: plan=%#v error=%v", plan, err)
	}

	nmTopology := readDNSTopologyFixture(t, "network-manager-empty.json")
	nmIdentity := nmTopology.Links[0].Identity
	plan, err = PlanLinuxDNS(baseDNSIntent(nmIdentity), nmTopology)
	if err != nil || len(plan.Journal) != 1 || plan.Journal[0].Apply.Identity.ConnectionUUID != nmIdentity.ConnectionUUID {
		t.Fatalf("NetworkManager plan=%#v error=%v", plan, err)
	}
	renamedNM := cloneDNSTopology(nmTopology)
	renamedNM.Links[0].Identity.InterfaceName = "nm-renamed"
	renamedNM.Links[0].Identity.IfIndex = 77
	plan, err = PlanLinuxDNS(baseDNSIntent(nmIdentity), renamedNM)
	if err != nil || plan.Identity.InterfaceName != "nm-renamed" || plan.Identity.ConnectionUUID != nmIdentity.ConnectionUUID {
		t.Fatalf("NetworkManager stable UUID did not survive incidental attributes: plan=%#v error=%v", plan, err)
	}
	wrongUUID := baseDNSIntent(nmIdentity)
	wrongUUID.Link.ConnectionUUID = "a73b6b21-613c-42dd-8555-e3eec453474f"
	if plan, err := PlanLinuxDNS(wrongUUID, nmTopology); err == nil || !strings.Contains(err.Error(), "no supported link") || len(plan.Journal) != 0 {
		t.Fatalf("wrong UUID plan=%#v error=%v", plan, err)
	}

	invalidResolved := resolvedIdentity()
	invalidResolved.IfIndex = 0
	if plan, err := PlanLinuxDNS(baseDNSIntent(invalidResolved), resolved); err == nil || !strings.Contains(err.Error(), "requires ifindex") || len(plan.Journal) != 0 {
		t.Fatalf("missing ifindex plan=%#v error=%v", plan, err)
	}
	invalidNM := nmIdentity
	invalidNM.ConnectionUUID = ""
	if plan, err := PlanLinuxDNS(baseDNSIntent(invalidNM), nmTopology); err == nil || !strings.Contains(err.Error(), "requires connection UUID") || len(plan.Journal) != 0 {
		t.Fatalf("missing UUID plan=%#v error=%v", plan, err)
	}
}

func TestPlanLinuxDNSCASAndLedgerGuardApplyAndRollback(t *testing.T) {
	t.Parallel()

	plan, err := PlanLinuxDNS(baseDNSIntent(resolvedIdentity()), readDNSTopologyFixture(t, "resolved-empty.json"))
	if err != nil {
		t.Fatal(err)
	}
	apply, rollback := plan.Journal[0].Apply, plan.Journal[0].Rollback
	if apply.ID == "" || apply.Precondition.Identity == "" || apply.Precondition.ExpectedFingerprint == "" ||
		apply.ResultFingerprint == "" || !apply.Ledger.Required || apply.Ledger.BeforeFingerprint != apply.Precondition.ExpectedFingerprint ||
		apply.Ledger.AfterFingerprint != apply.ResultFingerprint {
		t.Fatalf("apply contract = %#v", apply)
	}
	if rollback.Precondition.Identity != apply.Precondition.Identity || rollback.Precondition.ExpectedOwner != apply.ResultOwner ||
		rollback.Precondition.ExpectedFingerprint != apply.ResultFingerprint || !rollback.Ledger.Required {
		t.Fatalf("rollback contract = %#v", rollback)
	}
	concurrent := apply.State
	concurrent.DefaultRoute = !concurrent.DefaultRoute
	if linuxDNSStateFingerprint(concurrent) == rollback.Precondition.ExpectedFingerprint {
		t.Fatal("concurrent DefaultRoute mutation would evade rollback CAS")
	}
}

func TestPlanLinuxDNSIdempotenceAndForeignOwnership(t *testing.T) {
	t.Parallel()

	ownedTopology := readDNSTopologyFixture(t, "owned-existing.json")
	ownedIntent := LinuxDNSIntent{
		Owner: "session-7", Link: resolvedIdentity(), Servers: []netip.Addr{netip.MustParseAddr("10.200.0.1")},
		SearchDomains: []string{"corp.example"}, MatchDomains: []string{"apps.corp.example"},
	}
	plan, err := PlanLinuxDNS(ownedIntent, ownedTopology)
	if err != nil || len(plan.Journal) != 0 || len(plan.Satisfied) != 1 || !plan.Satisfied[0].Owned {
		t.Fatalf("owned plan=%#v error=%v", plan, err)
	}

	foreignTopology := readDNSTopologyFixture(t, "foreign.json")
	foreignIntent := LinuxDNSIntent{
		Owner: "session-7", Link: resolvedIdentity(), Servers: []netip.Addr{netip.MustParseAddr("203.0.113.53")},
		MatchDomains: []string{"foreign.example"}, DefaultRoute: true,
	}
	plan, err = PlanLinuxDNS(foreignIntent, foreignTopology)
	if err != nil || len(plan.Journal) != 0 || len(plan.Satisfied) != 1 || plan.Satisfied[0].Owned || plan.Satisfied[0].Owner != "other-vpn" {
		t.Fatalf("foreign equivalent plan=%#v error=%v", plan, err)
	}
	foreignIntent.Servers = []netip.Addr{netip.MustParseAddr("10.200.0.1")}
	if plan, err := PlanLinuxDNS(foreignIntent, foreignTopology); err == nil || !strings.Contains(err.Error(), `owned by "other-vpn"`) || len(plan.Journal) != 0 {
		t.Fatalf("foreign conflict plan=%#v error=%v", plan, err)
	}
}

func TestPlanLinuxDNSRejectsInvalidFingerprintAndDuplicateIdentity(t *testing.T) {
	t.Parallel()

	topology := readDNSTopologyFixture(t, "resolved-empty.json")
	topology.Links[1].Fingerprint = "sha256:stale"
	if plan, err := PlanLinuxDNS(baseDNSIntent(resolvedIdentity()), topology); err == nil || !strings.Contains(err.Error(), "fingerprint") || len(plan.Journal) != 0 {
		t.Fatalf("stale fingerprint plan=%#v error=%v", plan, err)
	}

	duplicate := readDNSTopologyFixture(t, "resolved-empty.json")
	duplicate.Links = append(duplicate.Links, duplicate.Links[1])
	if plan, err := PlanLinuxDNS(baseDNSIntent(resolvedIdentity()), duplicate); err == nil || !strings.Contains(err.Error(), "duplicate stable identity") || len(plan.Journal) != 0 {
		t.Fatalf("duplicate identity plan=%#v error=%v", plan, err)
	}
}

func TestPlanLinuxDNSRejectsInvalidServers(t *testing.T) {
	t.Parallel()

	topology := readDNSTopologyFixture(t, "resolved-empty.json")
	for _, test := range []struct {
		name    string
		servers []netip.Addr
		want    string
	}{
		{name: "none", servers: nil, want: "at least one"},
		{name: "unspecified", servers: []netip.Addr{netip.MustParseAddr("0.0.0.0")}, want: "valid unicast"},
		{name: "multicast", servers: []netip.Addr{netip.MustParseAddr("ff02::1")}, want: "valid unicast"},
		{name: "duplicate mapped", servers: []netip.Addr{netip.MustParseAddr("10.200.0.1"), netip.MustParseAddr("::ffff:10.200.0.1")}, want: "duplicate"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			intent := baseDNSIntent(resolvedIdentity())
			intent.Servers = test.servers
			plan, err := PlanLinuxDNS(intent, topology)
			if err == nil || !strings.Contains(err.Error(), test.want) || len(plan.Journal) != 0 {
				t.Fatalf("plan=%#v error=%v want=%q", plan, err, test.want)
			}
		})
	}
}

func resolvedIdentity() LinuxDNSLinkIdentity {
	return LinuxDNSLinkIdentity{InterfaceName: "zt-corp0", Backend: LinuxDNSBackendResolved, IfIndex: 42}
}

func baseDNSIntent(identity LinuxDNSLinkIdentity) LinuxDNSIntent {
	return LinuxDNSIntent{
		Owner: "session-7", Link: identity, Servers: []netip.Addr{netip.MustParseAddr("10.200.0.1")},
		MatchDomains: []string{"corp.example"}, DefaultRoute: false,
	}
}

func readDNSTopologyFixture(t *testing.T, name string) LinuxDNSTopology {
	t.Helper()
	contents, err := os.ReadFile("testdata/linux-topology/" + name)
	if err != nil {
		t.Fatalf("read topology fixture %s: %v", name, err)
	}
	var topology LinuxDNSTopology
	if err := json.Unmarshal(contents, &topology); err != nil {
		t.Fatalf("decode topology fixture %s: %v", name, err)
	}
	return topology
}

func cloneDNSTopology(input LinuxDNSTopology) LinuxDNSTopology {
	output := LinuxDNSTopology{Links: make([]LinuxDNSLink, len(input.Links))}
	for index, link := range input.Links {
		output.Links[index] = link
		output.Links[index].State = cloneLinuxDNSState(link.State)
	}
	return output
}

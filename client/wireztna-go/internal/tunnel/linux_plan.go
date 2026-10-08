package tunnel

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// LinuxAddressFamily identifies the iproute2 address-family snapshot that
// produced a route or rule. Direct defaults and "from all" are ambiguous
// without this caller-supplied context.
type LinuxAddressFamily uint8

const (
	LinuxIPv4 LinuxAddressFamily = 4
	LinuxIPv6 LinuxAddressFamily = 6
)

// LinuxRoute is the supported, deterministic subset of an iproute2 route.
type LinuxRoute struct {
	Type        string
	Destination netip.Prefix
	Gateway     netip.Addr
	Device      string
	Table       uint32
	Protocol    string
	Scope       string
	Source      netip.Addr
	Metric      uint32
	MetricSet   bool
	OnLink      bool
	LinkDown    bool
}

type LinuxRuleActionKind string

const LinuxRuleLookup LinuxRuleActionKind = "lookup"

// LinuxRuleAction is discriminated so lookup table zero remains distinct from
// a missing action. Only actions that the future executor can render belong
// here.
type LinuxRuleAction struct {
	Kind  LinuxRuleActionKind
	Table uint32
}

// LinuxRule has an executable identity: family, preference, selectors and
// action. Priority is ordering metadata, not a unique slot; repeated
// priorities are valid and are reconciled by exact identity.
type LinuxRule struct {
	Family                  LinuxAddressFamily
	Priority                uint32
	Not                     bool
	From                    netip.Prefix
	FromSet                 bool
	To                      netip.Prefix
	ToSet                   bool
	FWMark                  uint32
	FWMask                  uint32
	FWMarkSet               bool
	InputInterface          string
	OutputInterface         string
	Action                  LinuxRuleAction
	SuppressPrefixLength    uint8
	SuppressPrefixLengthSet bool
}

type LinuxObservedRoute struct {
	Route LinuxRoute
	Owner string
}

type LinuxObservedRule struct {
	Rule  LinuxRule
	Owner string
}

// LinuxObservedTable declares external evidence that a lookup table is safe
// and populated even when this owner does not manage any route in it.
type LinuxObservedTable struct {
	Family      LinuxAddressFamily
	Table       uint32
	Fingerprint string
}

// LinuxUplinkIdentity is supplied by host observation. StableID is a durable
// caller-defined identity (for example PCI path or connection UUID), not a
// display name.
type LinuxUplinkIdentity struct {
	IfIndex       uint32
	InterfaceName string
	Kind          string
	StableID      string
	Master        string
}

// LinuxBrokerLookup is the effective, caller-observed lookup for Broker under
// the policy context that full-tunnel activation will use. The planner never
// runs ip route get or inspects a namespace.
type LinuxBrokerLookup struct {
	Broker      netip.Addr
	Family      LinuxAddressFamily
	Table       uint32
	NetNS       string
	Route       LinuxRoute
	Uplink      LinuxUplinkIdentity
	Fingerprint string
}

type LinuxTunnelTopology struct {
	Routes       []LinuxObservedRoute
	Rules        []LinuxObservedRule
	SafeTables   []LinuxObservedTable
	BrokerLookup *LinuxBrokerLookup
}

// LinuxRouteTableIntent binds desired prefixes to the table that will contain
// them. Rules may only reference a desired populated table or a separately
// observed safe table.
type LinuxRouteTableIntent struct {
	Family   LinuxAddressFamily
	Table    uint32
	Prefixes []netip.Prefix
}

type LinuxTunnelIntent struct {
	Owner             string
	InterfaceName     string
	Broker            netip.Addr
	NetworkNamespace  string
	RouteTables       []LinuxRouteTableIntent
	FullTunnel        bool
	DesiredRules      []LinuxRule
	AuthorizedUplinks []LinuxUplinkIdentity
}

type LinuxPlanAction string
type LinuxPlanResource string
type LinuxPlanPhase uint8
type LinuxRouteRole string

const (
	LinuxPlanAdd     LinuxPlanAction = "add"
	LinuxPlanReplace LinuxPlanAction = "replace"
	LinuxPlanDelete  LinuxPlanAction = "delete"

	LinuxPlanRoute LinuxPlanResource = "route"
	LinuxPlanRule  LinuxPlanResource = "rule"

	LinuxPhasePrepareExclusion LinuxPlanPhase = 10
	LinuxPhaseActivateRoutes   LinuxPlanPhase = 20
	LinuxPhaseActivateRules    LinuxPlanPhase = 30
	LinuxPhaseWithdrawRules    LinuxPlanPhase = 40
	LinuxPhaseWithdrawRoutes   LinuxPlanPhase = 50

	LinuxRouteRoleManaged         LinuxRouteRole = "managed"
	LinuxRouteRoleBrokerExclusion LinuxRouteRole = "broker-exclusion"
)

// LinuxCASPrecondition is a declarative compare-and-swap guard. A future
// executor MUST re-observe this identity and reject divergence before apply or
// rollback.
type LinuxCASPrecondition struct {
	Identity            string
	MustExist           bool
	ExpectedFingerprint string
	ExpectedOwner       string
}

// LinuxLedgerRequirement is a contract only. A future executor MUST persist
// this transition durably and atomically with its logical mutation. This pure
// planner intentionally implements neither a ledger nor host I/O.
type LinuxLedgerRequirement struct {
	Required          bool
	ResourceIdentity  string
	BeforeFingerprint string
	AfterFingerprint  string
}

// LinuxPlanOperation is a semantic DAG node, never a command invocation.
type LinuxPlanOperation struct {
	ID                string
	Phase             LinuxPlanPhase
	DependsOn         []string
	Action            LinuxPlanAction
	Resource          LinuxPlanResource
	Role              LinuxRouteRole
	Owner             string
	ResultOwner       string
	Identity          string
	Precondition      LinuxCASPrecondition
	Guards            []LinuxCASPrecondition
	ResultFingerprint string
	Ledger            LinuxLedgerRequirement
	Route             LinuxRoute
	Rule              LinuxRule
}

type LinuxPlanJournalEntry struct {
	Apply    LinuxPlanOperation
	Rollback LinuxPlanOperation
}

type LinuxPlanSatisfaction struct {
	Resource LinuxPlanResource
	Key      string
	Owner    string
	Owned    bool
}

// LinuxTunnelPlan is topologically ordered. Rollback is reverse-topological;
// its DependsOn edges are the exact reverse of Apply edges.
type LinuxTunnelPlan struct {
	Owner          string
	Journal        []LinuxPlanJournalEntry
	Rollback       []LinuxPlanOperation
	Satisfied      []LinuxPlanSatisfaction
	LedgerRequired bool
}

func ParseLinuxRoutes(text string) ([]LinuxRoute, error) {
	return ParseLinuxRoutesForFamily(text, LinuxIPv4)
}

func ParseLinuxRoutesForFamily(text string, family LinuxAddressFamily) ([]LinuxRoute, error) {
	defaultDestination, err := defaultPrefix(family)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(strings.NewReader(text))
	var routes []LinuxRoute
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		route, parseErr := parseLinuxRouteLine(line, defaultDestination)
		if parseErr != nil {
			return nil, fmt.Errorf("ip route line %d: %w", lineNumber, parseErr)
		}
		routes = append(routes, route)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan ip route output: %w", err)
	}
	return routes, nil
}

func parseLinuxRouteLine(line string, defaultDestination netip.Prefix) (LinuxRoute, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return LinuxRoute{}, errors.New("empty route")
	}
	route := LinuxRoute{Type: "unicast", Table: 254}
	index := 0
	switch fields[0] {
	case "unicast", "local", "broadcast", "blackhole", "unreachable", "prohibit", "throw":
		route.Type = fields[0]
		index++
	}
	if index >= len(fields) {
		return LinuxRoute{}, errors.New("destination is required")
	}
	if fields[index] == "default" {
		route.Destination = defaultDestination
	} else {
		destination, err := netip.ParsePrefix(fields[index])
		if err != nil {
			return LinuxRoute{}, fmt.Errorf("invalid destination %q: %w", fields[index], err)
		}
		route.Destination = destination.Masked()
		if route.Destination.Addr().Is4() != defaultDestination.Addr().Is4() {
			return LinuxRoute{}, fmt.Errorf("destination %q does not match parser address family", fields[index])
		}
	}
	index++
	seen := map[string]bool{}
	for index < len(fields) {
		token := fields[index]
		index++
		if seen[token] {
			return LinuxRoute{}, fmt.Errorf("duplicate route attribute %q", token)
		}
		switch token {
		case "via", "dev", "table", "proto", "scope", "src", "metric", "onlink", "linkdown":
			seen[token] = true
		default:
			return LinuxRoute{}, fmt.Errorf("unsupported token %q", token)
		}
		var value string
		if token != "onlink" && token != "linkdown" {
			var err error
			value, index, err = routeValue(fields, index, token)
			if err != nil {
				return LinuxRoute{}, err
			}
		}
		var err error
		switch token {
		case "via":
			route.Gateway, err = netip.ParseAddr(value)
			if err != nil {
				return LinuxRoute{}, fmt.Errorf("invalid gateway %q: %w", value, err)
			}
		case "dev":
			route.Device = value
		case "table":
			route.Table, err = parseLinuxTable(value)
		case "proto":
			route.Protocol = value
		case "scope":
			route.Scope = value
		case "src":
			route.Source, err = netip.ParseAddr(value)
			if err != nil {
				return LinuxRoute{}, fmt.Errorf("invalid source %q: %w", value, err)
			}
		case "metric":
			route.Metric, err = parseUint32(value, "metric")
			route.MetricSet = err == nil
		case "onlink":
			route.OnLink = true
		case "linkdown":
			route.LinkDown = true
		}
		if err != nil {
			return LinuxRoute{}, err
		}
	}
	if route.Gateway.IsValid() && route.Gateway.Is4() != route.Destination.Addr().Is4() {
		return LinuxRoute{}, errors.New("gateway and destination address families differ")
	}
	if route.Source.IsValid() && route.Source.Is4() != route.Destination.Addr().Is4() {
		return LinuxRoute{}, errors.New("source and destination address families differ")
	}
	return route, nil
}

func ParseLinuxRules(text string) ([]LinuxRule, error) {
	return ParseLinuxRulesForFamily(text, LinuxIPv4)
}

// ParseLinuxRulesForFamily parses output from an explicit ip -4/-6 rule
// snapshot. Family is never inferred from selectors.
func ParseLinuxRulesForFamily(text string, family LinuxAddressFamily) ([]LinuxRule, error) {
	if _, err := defaultPrefix(family); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(strings.NewReader(text))
	var rules []LinuxRule
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		rule, err := parseLinuxRuleLine(line, family)
		if err != nil {
			return nil, fmt.Errorf("ip rule line %d: %w", lineNumber, err)
		}
		rules = append(rules, rule)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan ip rule output: %w", err)
	}
	return rules, nil
}

func parseLinuxRuleLine(line string, family LinuxAddressFamily) (LinuxRule, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 || !strings.HasSuffix(fields[0], ":") {
		return LinuxRule{}, errors.New("priority prefix is required")
	}
	priority, err := parseUint32(strings.TrimSuffix(fields[0], ":"), "priority")
	if err != nil {
		return LinuxRule{}, err
	}
	rule := LinuxRule{Family: family, Priority: priority}
	seen := map[string]bool{}
	for index := 1; index < len(fields); {
		token := fields[index]
		index++
		key := token
		if token == "table" {
			key = "lookup"
		}
		if seen[key] {
			return LinuxRule{}, fmt.Errorf("duplicate rule attribute %q", key)
		}
		switch key {
		case "not", "from", "to", "fwmark", "iif", "oif", "lookup", "suppress_prefixlength":
			seen[key] = true
		default:
			return LinuxRule{}, fmt.Errorf("unsupported token %q", token)
		}
		if token == "not" {
			rule.Not = true
			continue
		}
		value, next, valueErr := routeValue(fields, index, token)
		if valueErr != nil {
			return LinuxRule{}, valueErr
		}
		index = next
		switch token {
		case "from", "to":
			prefix, err := parseRulePrefix(value, family)
			if err != nil {
				return LinuxRule{}, fmt.Errorf("invalid %s selector %q: %w", token, value, err)
			}
			if token == "from" {
				rule.From, rule.FromSet = prefix, true
			} else {
				rule.To, rule.ToSet = prefix, true
			}
		case "fwmark":
			parts := strings.Split(value, "/")
			if len(parts) > 2 {
				return LinuxRule{}, fmt.Errorf("invalid fwmark %q", value)
			}
			rule.FWMark, valueErr = parseUint32AutoBase(parts[0], "fwmark")
			rule.FWMask = ^uint32(0)
			if valueErr == nil && len(parts) == 2 {
				rule.FWMask, valueErr = parseUint32AutoBase(parts[1], "fwmark mask")
			}
			if valueErr != nil {
				return LinuxRule{}, valueErr
			}
			rule.FWMarkSet = true
		case "iif":
			rule.InputInterface = value
		case "oif":
			rule.OutputInterface = value
		case "lookup", "table":
			rule.Action.Table, valueErr = parseLinuxTable(value)
			if valueErr != nil {
				return LinuxRule{}, valueErr
			}
			rule.Action.Kind = LinuxRuleLookup
		case "suppress_prefixlength":
			maximum := uint64(128)
			if family == LinuxIPv4 {
				maximum = 32
			}
			length, parseErr := strconv.ParseUint(value, 10, 8)
			if parseErr != nil || length > maximum {
				return LinuxRule{}, fmt.Errorf("invalid suppress_prefixlength %q for IPv%d", value, family)
			}
			rule.SuppressPrefixLength = uint8(length)
			rule.SuppressPrefixLengthSet = true
		}
	}
	if rule.Action.Kind == "" {
		return LinuxRule{}, errors.New("rule must contain exactly one supported action")
	}
	if err := validateLinuxRule(rule); err != nil {
		return LinuxRule{}, err
	}
	return rule, nil
}

func PlanLinuxTunnel(intent LinuxTunnelIntent, topology LinuxTunnelTopology) (LinuxTunnelPlan, error) {
	if strings.TrimSpace(intent.Owner) == "" {
		return LinuxTunnelPlan{}, errors.New("route owner is required")
	}
	if err := validateLinuxInterfaceName(intent.InterfaceName); err != nil {
		return LinuxTunnelPlan{}, err
	}
	if !intent.Broker.IsValid() || intent.Broker.IsUnspecified() || intent.Broker.IsMulticast() {
		return LinuxTunnelPlan{}, errors.New("broker must be a valid unicast address")
	}

	desired, tableKeys, fullTable, err := desiredLinuxRoutes(intent, topology)
	if err != nil {
		return LinuxTunnelPlan{}, err
	}
	safeTables, err := validatedSafeTables(topology.SafeTables)
	if err != nil {
		return LinuxTunnelPlan{}, err
	}
	externalTableGuards := make(map[string]string, len(safeTables))
	for key, observedFingerprint := range safeTables {
		externalTableGuards[key] = observedFingerprint
	}
	for key := range tableKeys {
		safeTables[key] = ""
		delete(externalTableGuards, key)
	}
	desiredRules := append([]LinuxRule(nil), intent.DesiredRules...)
	for index, rule := range desiredRules {
		if err := validateLinuxRule(rule); err != nil {
			return LinuxTunnelPlan{}, fmt.Errorf("desired rule %d: %w", index, err)
		}
		if _, ok := safeTables[linuxTableKey(rule.Family, rule.Action.Table)]; !ok {
			return LinuxTunnelPlan{}, fmt.Errorf("desired rule %d lookup table %d for IPv%d is neither populated nor observed safe", index, rule.Action.Table, rule.Family)
		}
	}
	sort.Slice(desiredRules, func(i, j int) bool { return linuxRuleKey(desiredRules[i]) < linuxRuleKey(desiredRules[j]) })
	if err := rejectDuplicateRuleIdentities(desiredRules); err != nil {
		return LinuxTunnelPlan{}, err
	}

	plan := LinuxTunnelPlan{Owner: intent.Owner, LedgerRequired: true}
	if err := planRoutes(&plan, intent.Owner, desired, topology.Routes); err != nil {
		return LinuxTunnelPlan{}, err
	}
	if err := planRules(&plan, intent.Owner, desiredRules, topology.Rules, externalTableGuards); err != nil {
		return LinuxTunnelPlan{}, err
	}
	finalizeLinuxPlan(&plan, fullTable)
	sort.Slice(plan.Satisfied, func(i, j int) bool {
		if plan.Satisfied[i].Resource != plan.Satisfied[j].Resource {
			return plan.Satisfied[i].Resource < plan.Satisfied[j].Resource
		}
		return plan.Satisfied[i].Key < plan.Satisfied[j].Key
	})
	return plan, nil
}

type desiredLinuxRoute struct {
	Route  LinuxRoute
	Role   LinuxRouteRole
	Guards []LinuxCASPrecondition
}

func desiredLinuxRoutes(intent LinuxTunnelIntent, topology LinuxTunnelTopology) ([]desiredLinuxRoute, map[string]struct{}, string, error) {
	seenTables := map[string]struct{}{}
	seenRoutes := map[string]struct{}{}
	var desired []desiredLinuxRoute
	hasDefault := false
	fullTable := ""
	var fullTableNumber uint32
	for tableIndex, table := range intent.RouteTables {
		if _, err := defaultPrefix(table.Family); err != nil {
			return nil, nil, "", fmt.Errorf("route table %d: %w", tableIndex, err)
		}
		key := linuxTableKey(table.Family, table.Table)
		if _, duplicate := seenTables[key]; duplicate {
			return nil, nil, "", fmt.Errorf("duplicate desired route table %s", key)
		}
		seenTables[key] = struct{}{}
		if len(table.Prefixes) == 0 {
			return nil, nil, "", fmt.Errorf("desired route table %s is empty", key)
		}
		for prefixIndex, prefix := range table.Prefixes {
			if !prefix.IsValid() {
				return nil, nil, "", fmt.Errorf("route table %d prefix %d is invalid", tableIndex, prefixIndex)
			}
			prefix = prefix.Masked()
			if addressFamily(prefix.Addr()) != table.Family {
				return nil, nil, "", fmt.Errorf("route table %d prefix %q has wrong family", tableIndex, prefix)
			}
			if prefix.Bits() == 0 {
				hasDefault = true
				if table.Family == addressFamily(intent.Broker) {
					if fullTable != "" && fullTable != key {
						return nil, nil, "", fmt.Errorf("FullTunnel has multiple default tables for broker IPv%d family", table.Family)
					}
					fullTable = key
					fullTableNumber = table.Table
				}
			}
			route := LinuxRoute{Type: "unicast", Destination: prefix, Device: intent.InterfaceName, Table: table.Table, Protocol: "static", Scope: "link"}
			identity := linuxRouteIdentity(route)
			if _, duplicate := seenRoutes[identity]; duplicate {
				continue
			}
			seenRoutes[identity] = struct{}{}
			desired = append(desired, desiredLinuxRoute{Route: route, Role: LinuxRouteRoleManaged})
		}
	}
	if hasDefault && !intent.FullTunnel {
		return nil, nil, "", errors.New("a default route requires FullTunnel so the broker exclusion is planned")
	}
	if !intent.FullTunnel {
		sortDesiredRoutes(desired)
		return desired, seenTables, "", nil
	}
	if fullTable == "" {
		return nil, nil, "", errors.New("FullTunnel requires a default route for the broker address family")
	}
	lookup, err := validateBrokerLookup(intent, topology.BrokerLookup)
	if err != nil {
		return nil, nil, "", err
	}
	bits := 128
	if intent.Broker.Is4() {
		bits = 32
	}
	exclusion := LinuxRoute{
		Type: "unicast", Destination: netip.PrefixFrom(intent.Broker, bits), Gateway: lookup.Route.Gateway,
		Device: lookup.Uplink.InterfaceName, Table: fullTableNumber, Protocol: "static", Scope: lookup.Route.Scope,
		Source: lookup.Route.Source, Metric: lookup.Route.Metric, MetricSet: lookup.Route.MetricSet, OnLink: lookup.Route.OnLink,
	}
	lookupGuard := LinuxCASPrecondition{
		Identity: linuxBrokerLookupIdentity(lookup), MustExist: true, ExpectedFingerprint: lookup.Fingerprint,
	}
	for index := range desired {
		route := desired[index].Route
		if linuxTableKey(addressFamily(route.Destination.Addr()), route.Table) == fullTable {
			desired[index].Guards = []LinuxCASPrecondition{lookupGuard}
		}
	}
	desired = append(desired, desiredLinuxRoute{Route: exclusion, Role: LinuxRouteRoleBrokerExclusion, Guards: []LinuxCASPrecondition{lookupGuard}})
	if _, duplicate := seenRoutes[linuxRouteIdentity(exclusion)]; duplicate {
		return nil, nil, "", errors.New("broker exclusion collides with a desired route")
	}
	seenRoutes[linuxRouteIdentity(exclusion)] = struct{}{}
	sortDesiredRoutes(desired)
	return desired, seenTables, fullTable, nil
}

func validateBrokerLookup(intent LinuxTunnelIntent, lookup *LinuxBrokerLookup) (LinuxBrokerLookup, error) {
	if lookup == nil {
		return LinuxBrokerLookup{}, errors.New("FullTunnel requires an explicit effective broker lookup observation")
	}
	if lookup.Broker != intent.Broker || lookup.Family != addressFamily(intent.Broker) {
		return LinuxBrokerLookup{}, errors.New("broker lookup address or family does not match intent")
	}
	if intent.NetworkNamespace == "" || lookup.NetNS != intent.NetworkNamespace {
		return LinuxBrokerLookup{}, errors.New("broker lookup network namespace does not match intent")
	}
	if err := validateLinuxRoute(lookup.Route); err != nil {
		return LinuxBrokerLookup{}, fmt.Errorf("effective broker route: %w", err)
	}
	if lookup.Route.Table != lookup.Table || addressFamily(lookup.Route.Destination.Addr()) != lookup.Family || !lookup.Route.Destination.Contains(intent.Broker) {
		return LinuxBrokerLookup{}, errors.New("effective broker route does not match lookup family, table and destination")
	}
	if lookup.Route.Type != "unicast" || lookup.Route.LinkDown || lookup.Route.Device != lookup.Uplink.InterfaceName {
		return LinuxBrokerLookup{}, errors.New("effective broker route is not usable through the observed uplink")
	}
	if err := validateLinuxUplink(lookup.Uplink); err != nil {
		return LinuxBrokerLookup{}, err
	}
	authorized := false
	for _, candidate := range intent.AuthorizedUplinks {
		if linuxUplinkKey(candidate) == linuxUplinkKey(lookup.Uplink) {
			authorized = true
			break
		}
	}
	if !authorized {
		return LinuxBrokerLookup{}, fmt.Errorf("effective broker uplink %q is not authorized", lookup.Uplink.InterfaceName)
	}
	expectedFingerprint := linuxBrokerLookupFingerprint(*lookup)
	if strings.TrimSpace(lookup.Fingerprint) == "" {
		return LinuxBrokerLookup{}, errors.New("effective broker lookup requires observation fingerprint")
	}
	if lookup.Fingerprint != expectedFingerprint {
		return LinuxBrokerLookup{}, errors.New("effective broker lookup fingerprint does not match its snapshot")
	}
	return *lookup, nil
}

func validateLinuxUplink(uplink LinuxUplinkIdentity) error {
	if uplink.IfIndex == 0 || uplink.StableID == "" {
		return errors.New("effective broker uplink requires ifindex and stable identity")
	}
	if err := validateLinuxInterfaceName(uplink.InterfaceName); err != nil {
		return fmt.Errorf("effective broker uplink: %w", err)
	}
	if uplink.Master != "" {
		return fmt.Errorf("effective broker uplink %q is enslaved to %q and is not safe", uplink.InterfaceName, uplink.Master)
	}
	switch strings.ToLower(uplink.Kind) {
	case "ether", "ethernet", "wifi", "wireless", "ppp":
		return nil
	case "wireguard", "tun", "tap", "vti", "vrf", "vpn":
		return fmt.Errorf("effective broker uplink kind %q is a VPN or virtual routing link", uplink.Kind)
	default:
		return fmt.Errorf("effective broker uplink kind %q is not explicitly safe", uplink.Kind)
	}
}

func validatedSafeTables(input []LinuxObservedTable) (map[string]string, error) {
	result := make(map[string]string, len(input))
	for index, table := range input {
		if _, err := defaultPrefix(table.Family); err != nil {
			return nil, fmt.Errorf("safe table %d: %w", index, err)
		}
		if strings.TrimSpace(table.Fingerprint) == "" {
			return nil, fmt.Errorf("safe table %d requires observation fingerprint", index)
		}
		key := linuxTableKey(table.Family, table.Table)
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate safe table %s", key)
		}
		result[key] = table.Fingerprint
	}
	return result, nil
}

func planRoutes(plan *LinuxTunnelPlan, owner string, desired []desiredLinuxRoute, observed []LinuxObservedRoute) error {
	ordered := append([]LinuxObservedRoute(nil), observed...)
	sort.Slice(ordered, func(i, j int) bool {
		return linuxRouteIdentity(ordered[i].Route) < linuxRouteIdentity(ordered[j].Route)
	})
	byIdentity := make(map[string]LinuxObservedRoute, len(ordered))
	for _, item := range ordered {
		if err := validateLinuxRoute(item.Route); err != nil {
			return fmt.Errorf("observed route: %w", err)
		}
		identity := linuxRouteIdentity(item.Route)
		if _, exists := byIdentity[identity]; exists {
			return fmt.Errorf("route topology has duplicate identity %s", identity)
		}
		byIdentity[identity] = item
	}
	desiredIdentities := make(map[string]struct{}, len(desired))
	for _, wanted := range desired {
		route := wanted.Route
		identity := linuxRouteIdentity(route)
		desiredIdentities[identity] = struct{}{}
		current, exists := byIdentity[identity]
		if !exists {
			apply := newRouteOperation(LinuxPlanAdd, owner, "", route, wanted.Role)
			apply.Guards = append([]LinuxCASPrecondition(nil), wanted.Guards...)
			appendLinuxMutation(plan, apply, newRouteOperation(LinuxPlanDelete, owner, owner, route, wanted.Role))
			continue
		}
		if linuxRouteFingerprint(current.Route) == linuxRouteFingerprint(route) {
			plan.Satisfied = append(plan.Satisfied, LinuxPlanSatisfaction{Resource: LinuxPlanRoute, Key: identity, Owner: current.Owner, Owned: current.Owner == owner})
			continue
		}
		if current.Owner != owner {
			return fmt.Errorf("route conflict in identity %s is owned by %q", identity, current.Owner)
		}
		apply := newRouteReplaceOperation(owner, current.Route, route, wanted.Role)
		apply.Guards = append([]LinuxCASPrecondition(nil), wanted.Guards...)
		appendLinuxMutation(plan, apply, newRouteReplaceOperation(owner, route, current.Route, wanted.Role))
	}
	for _, item := range ordered {
		identity := linuxRouteIdentity(item.Route)
		if item.Owner != owner {
			continue
		}
		if _, retained := desiredIdentities[identity]; retained {
			continue
		}
		appendLinuxMutation(plan, newRouteOperation(LinuxPlanDelete, owner, owner, item.Route, LinuxRouteRoleManaged), newRouteOperation(LinuxPlanAdd, owner, "", item.Route, LinuxRouteRoleManaged))
	}
	return nil
}

func planRules(plan *LinuxTunnelPlan, owner string, desired []LinuxRule, observed []LinuxObservedRule, externalTableGuards map[string]string) error {
	ordered := append([]LinuxObservedRule(nil), observed...)
	sort.Slice(ordered, func(i, j int) bool { return linuxRuleKey(ordered[i].Rule) < linuxRuleKey(ordered[j].Rule) })
	byIdentity := make(map[string]LinuxObservedRule, len(ordered))
	for _, item := range ordered {
		if err := validateLinuxRule(item.Rule); err != nil {
			return fmt.Errorf("observed rule: %w", err)
		}
		identity := linuxRuleIdentity(item.Rule)
		if _, duplicate := byIdentity[identity]; duplicate {
			return fmt.Errorf("rule topology has duplicate executable identity %s", identity)
		}
		byIdentity[identity] = item
	}
	desiredIdentities := make(map[string]struct{}, len(desired))
	for _, rule := range desired {
		identity := linuxRuleIdentity(rule)
		desiredIdentities[identity] = struct{}{}
		if current, exists := byIdentity[identity]; exists {
			plan.Satisfied = append(plan.Satisfied, LinuxPlanSatisfaction{Resource: LinuxPlanRule, Key: identity, Owner: current.Owner, Owned: current.Owner == owner})
			continue
		}
		apply := newRuleOperation(LinuxPlanAdd, owner, "", rule)
		if observedFingerprint := externalTableGuards[linuxTableKey(rule.Family, rule.Action.Table)]; observedFingerprint != "" {
			apply.Guards = []LinuxCASPrecondition{{
				Identity: linuxObservedTableIdentity(rule.Family, rule.Action.Table), MustExist: true, ExpectedFingerprint: observedFingerprint,
			}}
		}
		appendLinuxMutation(plan, apply, newRuleOperation(LinuxPlanDelete, owner, owner, rule))
	}
	for _, item := range ordered {
		identity := linuxRuleIdentity(item.Rule)
		if item.Owner != owner {
			continue
		}
		if _, retained := desiredIdentities[identity]; retained {
			continue
		}
		appendLinuxMutation(plan, newRuleOperation(LinuxPlanDelete, owner, owner, item.Rule), newRuleOperation(LinuxPlanAdd, owner, "", item.Rule))
	}
	return nil
}

func appendLinuxMutation(plan *LinuxTunnelPlan, apply, rollback LinuxPlanOperation) {
	apply.ID = linuxOperationID("apply", apply)
	rollback.ID = linuxOperationID("rollback", rollback)
	plan.Journal = append(plan.Journal, LinuxPlanJournalEntry{Apply: apply, Rollback: rollback})
}

func newRouteOperation(action LinuxPlanAction, actor, expectedOwner string, route LinuxRoute, role LinuxRouteRole) LinuxPlanOperation {
	identity := linuxRouteIdentity(route)
	fingerprint := linuxRouteFingerprint(route)
	operation := LinuxPlanOperation{Action: action, Resource: LinuxPlanRoute, Role: role, Owner: actor, Identity: identity, Route: route}
	if action == LinuxPlanAdd {
		operation.Phase = LinuxPhaseActivateRoutes
		if role == LinuxRouteRoleBrokerExclusion {
			operation.Phase = LinuxPhasePrepareExclusion
		}
		operation.ResultOwner = actor
		operation.Precondition = LinuxCASPrecondition{Identity: identity, MustExist: false}
		operation.ResultFingerprint = fingerprint
	} else {
		operation.Phase = LinuxPhaseWithdrawRoutes
		operation.Precondition = LinuxCASPrecondition{Identity: identity, MustExist: true, ExpectedFingerprint: fingerprint, ExpectedOwner: expectedOwner}
	}
	operation.Ledger = LinuxLedgerRequirement{Required: true, ResourceIdentity: identity, BeforeFingerprint: operation.Precondition.ExpectedFingerprint, AfterFingerprint: operation.ResultFingerprint}
	return operation
}

func newRouteReplaceOperation(actor string, before, after LinuxRoute, role LinuxRouteRole) LinuxPlanOperation {
	identity := linuxRouteIdentity(after)
	operation := LinuxPlanOperation{
		Action: LinuxPlanReplace, Resource: LinuxPlanRoute, Role: role, Owner: actor, ResultOwner: actor, Identity: identity, Route: after,
		Phase:             LinuxPhaseActivateRoutes,
		Precondition:      LinuxCASPrecondition{Identity: identity, MustExist: true, ExpectedFingerprint: linuxRouteFingerprint(before), ExpectedOwner: actor},
		ResultFingerprint: linuxRouteFingerprint(after),
	}
	if role == LinuxRouteRoleBrokerExclusion {
		operation.Phase = LinuxPhasePrepareExclusion
	}
	operation.Ledger = LinuxLedgerRequirement{Required: true, ResourceIdentity: identity, BeforeFingerprint: operation.Precondition.ExpectedFingerprint, AfterFingerprint: operation.ResultFingerprint}
	return operation
}

func newRuleOperation(action LinuxPlanAction, actor, expectedOwner string, rule LinuxRule) LinuxPlanOperation {
	identity := linuxRuleIdentity(rule)
	fingerprint := linuxRuleFingerprint(rule)
	operation := LinuxPlanOperation{Action: action, Resource: LinuxPlanRule, Owner: actor, Identity: identity, Rule: rule}
	if action == LinuxPlanAdd {
		operation.Phase = LinuxPhaseActivateRules
		operation.ResultOwner = actor
		operation.Precondition = LinuxCASPrecondition{Identity: identity, MustExist: false}
		operation.ResultFingerprint = fingerprint
	} else {
		operation.Phase = LinuxPhaseWithdrawRules
		operation.Precondition = LinuxCASPrecondition{Identity: identity, MustExist: true, ExpectedFingerprint: fingerprint, ExpectedOwner: expectedOwner}
	}
	operation.Ledger = LinuxLedgerRequirement{Required: true, ResourceIdentity: identity, BeforeFingerprint: operation.Precondition.ExpectedFingerprint, AfterFingerprint: operation.ResultFingerprint}
	return operation
}

func finalizeLinuxPlan(plan *LinuxTunnelPlan, fullTable string) {
	routeAdds := map[string][]string{}
	ruleDeletes := map[string][]string{}
	exclusions := map[string]string{}
	for _, entry := range plan.Journal {
		op := entry.Apply
		if op.Resource == LinuxPlanRoute && (op.Action == LinuxPlanAdd || op.Action == LinuxPlanReplace) {
			key := linuxTableKey(addressFamily(op.Route.Destination.Addr()), op.Route.Table)
			routeAdds[key] = append(routeAdds[key], op.ID)
			if op.Role == LinuxRouteRoleBrokerExclusion {
				exclusions[key] = op.ID
			}
		}
		if op.Resource == LinuxPlanRule && op.Action == LinuxPlanDelete {
			key := linuxTableKey(op.Rule.Family, op.Rule.Action.Table)
			ruleDeletes[key] = append(ruleDeletes[key], op.ID)
		}
	}
	for index := range plan.Journal {
		op := &plan.Journal[index].Apply
		switch {
		case op.Resource == LinuxPlanRule && op.Action == LinuxPlanAdd:
			op.DependsOn = append(op.DependsOn, routeAdds[linuxTableKey(op.Rule.Family, op.Rule.Action.Table)]...)
		case op.Resource == LinuxPlanRoute && op.Action == LinuxPlanDelete:
			op.DependsOn = append(op.DependsOn, ruleDeletes[linuxTableKey(addressFamily(op.Route.Destination.Addr()), op.Route.Table)]...)
		case op.Resource == LinuxPlanRoute && (op.Action == LinuxPlanAdd || op.Action == LinuxPlanReplace) && fullTable != "" && op.Role != LinuxRouteRoleBrokerExclusion:
			if dependency := exclusions[linuxTableKey(addressFamily(op.Route.Destination.Addr()), op.Route.Table)]; dependency != "" {
				op.DependsOn = append(op.DependsOn, dependency)
			}
		}
		sort.Strings(op.DependsOn)
	}
	sort.Slice(plan.Journal, func(i, j int) bool {
		if plan.Journal[i].Apply.Phase != plan.Journal[j].Apply.Phase {
			return plan.Journal[i].Apply.Phase < plan.Journal[j].Apply.Phase
		}
		return plan.Journal[i].Apply.ID < plan.Journal[j].Apply.ID
	})
	rollbackID := make(map[string]string, len(plan.Journal))
	for _, entry := range plan.Journal {
		rollbackID[entry.Apply.ID] = entry.Rollback.ID
	}
	byApplyID := make(map[string]int, len(plan.Journal))
	for index := range plan.Journal {
		byApplyID[plan.Journal[index].Apply.ID] = index
	}
	for _, entry := range plan.Journal {
		for _, dependency := range entry.Apply.DependsOn {
			dependencyIndex := byApplyID[dependency]
			plan.Journal[dependencyIndex].Rollback.DependsOn = append(plan.Journal[dependencyIndex].Rollback.DependsOn, rollbackID[entry.Apply.ID])
		}
	}
	for index := range plan.Journal {
		sort.Strings(plan.Journal[index].Rollback.DependsOn)
	}
	for index := len(plan.Journal) - 1; index >= 0; index-- {
		plan.Rollback = append(plan.Rollback, plan.Journal[index].Rollback)
	}
}

func rejectDuplicateRuleIdentities(rules []LinuxRule) error {
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		identity := linuxRuleIdentity(rule)
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("duplicate desired rule identity %s", identity)
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func validateLinuxRoute(route LinuxRoute) error {
	if !route.Destination.IsValid() {
		return errors.New("route destination is required")
	}
	if route.Destination != route.Destination.Masked() {
		return errors.New("route destination must be canonical")
	}
	if route.Type == "" {
		return errors.New("route type is required")
	}
	if route.Gateway.IsValid() && route.Gateway.Is4() != route.Destination.Addr().Is4() {
		return errors.New("route gateway family differs from destination")
	}
	if route.Source.IsValid() && route.Source.Is4() != route.Destination.Addr().Is4() {
		return errors.New("route source family differs from destination")
	}
	return nil
}

func validateLinuxRule(rule LinuxRule) error {
	if _, err := defaultPrefix(rule.Family); err != nil {
		return err
	}
	if rule.Action.Kind != LinuxRuleLookup {
		return errors.New("rule must contain exactly one supported lookup action")
	}
	if rule.FromSet {
		if rule.From.IsValid() && (rule.From != rule.From.Masked() || addressFamily(rule.From.Addr()) != rule.Family) {
			return errors.New("rule from selector is non-canonical or has wrong family")
		}
	} else if rule.From.IsValid() {
		return errors.New("rule from selector requires FromSet")
	}
	if rule.ToSet {
		if rule.To.IsValid() && (rule.To != rule.To.Masked() || addressFamily(rule.To.Addr()) != rule.Family) {
			return errors.New("rule to selector is non-canonical or has wrong family")
		}
	} else if rule.To.IsValid() {
		return errors.New("rule to selector requires ToSet")
	}
	maximum := uint8(128)
	if rule.Family == LinuxIPv4 {
		maximum = 32
	}
	if rule.SuppressPrefixLengthSet && rule.SuppressPrefixLength > maximum {
		return fmt.Errorf("rule suppress prefix length exceeds %d for IPv%d", maximum, rule.Family)
	}
	return nil
}

func validateLinuxInterfaceName(name string) error {
	if name == "" || len(name) > 15 {
		return errors.New("Linux interface name must contain 1 to 15 characters")
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("_.-", character) {
			continue
		}
		return fmt.Errorf("Linux interface name contains invalid character %q", character)
	}
	return nil
}

func parseRulePrefix(value string, family LinuxAddressFamily) (netip.Prefix, error) {
	if value == "all" {
		return netip.Prefix{}, nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		address, addressErr := netip.ParseAddr(value)
		if addressErr != nil {
			return netip.Prefix{}, err
		}
		bits := 128
		if address.Is4() {
			bits = 32
		}
		prefix = netip.PrefixFrom(address, bits)
	}
	prefix = prefix.Masked()
	if addressFamily(prefix.Addr()) != family {
		return netip.Prefix{}, errors.New("selector does not match parser address family")
	}
	return prefix, nil
}

func defaultPrefix(family LinuxAddressFamily) (netip.Prefix, error) {
	switch family {
	case LinuxIPv4:
		return netip.MustParsePrefix("0.0.0.0/0"), nil
	case LinuxIPv6:
		return netip.MustParsePrefix("::/0"), nil
	default:
		return netip.Prefix{}, fmt.Errorf("unsupported Linux address family %d", family)
	}
}

func addressFamily(address netip.Addr) LinuxAddressFamily {
	if address.Is4() {
		return LinuxIPv4
	}
	return LinuxIPv6
}

func linuxRouteIdentity(route LinuxRoute) string {
	metric := "-"
	if route.MetricSet {
		metric = strconv.FormatUint(uint64(route.Metric), 10)
	}
	return fmt.Sprintf("route|IPv%d|table=%d|dst=%s|metric=%s", addressFamily(route.Destination.Addr()), route.Table, route.Destination, metric)
}

func linuxRouteKey(route LinuxRoute) string {
	return fmt.Sprintf("%s|%s|via=%s|dev=%s|table=%d|proto=%s|scope=%s|src=%s|metric=%d:%t|onlink=%t|linkdown=%t",
		route.Type, route.Destination, route.Gateway, route.Device, route.Table, route.Protocol,
		route.Scope, route.Source, route.Metric, route.MetricSet, route.OnLink, route.LinkDown)
}

func linuxRouteFingerprint(route LinuxRoute) string { return fingerprint(linuxRouteKey(route)) }

func linuxRuleIdentity(rule LinuxRule) string { return "rule|" + linuxRuleKey(rule) }

func linuxRuleKey(rule LinuxRule) string {
	return fmt.Sprintf("IPv%d|priority=%d|not=%t|from=%s:%t|to=%s:%t|fwmark=%d/%d:%t|iif=%s|oif=%s|action=%s:%d|suppress=%d:%t",
		rule.Family, rule.Priority, rule.Not, rule.From, rule.FromSet, rule.To, rule.ToSet, rule.FWMark, rule.FWMask, rule.FWMarkSet,
		rule.InputInterface, rule.OutputInterface, rule.Action.Kind, rule.Action.Table, rule.SuppressPrefixLength, rule.SuppressPrefixLengthSet)
}

func linuxRuleFingerprint(rule LinuxRule) string { return fingerprint(linuxRuleKey(rule)) }

func linuxTableKey(family LinuxAddressFamily, table uint32) string {
	return fmt.Sprintf("IPv%d/table=%d", family, table)
}

func linuxUplinkKey(uplink LinuxUplinkIdentity) string {
	return fmt.Sprintf("ifindex=%d|name=%s|kind=%s|stable=%s|master=%s", uplink.IfIndex, uplink.InterfaceName, strings.ToLower(uplink.Kind), uplink.StableID, uplink.Master)
}

func linuxObservedTableIdentity(family LinuxAddressFamily, table uint32) string {
	return "observed-table|" + linuxTableKey(family, table)
}

func linuxBrokerLookupIdentity(lookup LinuxBrokerLookup) string {
	return fmt.Sprintf("broker-lookup|netns=%s|IPv%d|broker=%s", lookup.NetNS, lookup.Family, lookup.Broker)
}

func linuxBrokerLookupFingerprint(lookup LinuxBrokerLookup) string {
	return fingerprint(fmt.Sprintf("%s|table=%d|route=%s|uplink=%s", linuxBrokerLookupIdentity(lookup), lookup.Table, linuxRouteKey(lookup.Route), linuxUplinkKey(lookup.Uplink)))
}

func linuxOperationID(direction string, operation LinuxPlanOperation) string {
	return direction + "-" + fingerprint(fmt.Sprintf("%s|%s|%s|%s|%s|%s|guards=%s", operation.Action, operation.Resource, operation.Identity,
		operation.Precondition.ExpectedFingerprint, operation.ResultFingerprint, operation.Role, linuxCASGuardsKey(operation.Guards)))[:16]
}

func linuxCASGuardsKey(guards []LinuxCASPrecondition) string {
	parts := make([]string, len(guards))
	for index, guard := range guards {
		parts[index] = fmt.Sprintf("%s:%t:%s:%s", guard.Identity, guard.MustExist, guard.ExpectedFingerprint, guard.ExpectedOwner)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func sortDesiredRoutes(routes []desiredLinuxRoute) {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Role != routes[j].Role {
			return routes[i].Role == LinuxRouteRoleBrokerExclusion
		}
		return linuxRouteKey(routes[i].Route) < linuxRouteKey(routes[j].Route)
	})
}

func routeValue(fields []string, index int, token string) (string, int, error) {
	if index >= len(fields) {
		return "", index, fmt.Errorf("%s value is required", token)
	}
	return fields[index], index + 1, nil
}

func parseLinuxTable(value string) (uint32, error) {
	switch value {
	case "unspec":
		return 0, nil
	case "default":
		return 253, nil
	case "main":
		return 254, nil
	case "local":
		return 255, nil
	default:
		return parseUint32(value, "table")
	}
}

func parseUint32(value, field string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", field, value, err)
	}
	return uint32(parsed), nil
}

func parseUint32AutoBase(value, field string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 0, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", field, value, err)
	}
	return uint32(parsed), nil
}

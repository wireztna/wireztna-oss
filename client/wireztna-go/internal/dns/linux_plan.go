package dns

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

type LinuxDNSBackend string

const (
	LinuxDNSBackendResolved       LinuxDNSBackend = "systemd-resolved"
	LinuxDNSBackendNetworkManager LinuxDNSBackend = "network-manager"
)

type LinuxDNSDomain struct {
	Name      string `json:"name"`
	RouteOnly bool   `json:"route_only"`
}

// LinuxDNSLinkIdentity addresses the backend resource, not merely its display
// name. resolved requires IfIndex; NetworkManager requires ConnectionUUID.
type LinuxDNSLinkIdentity struct {
	InterfaceName  string          `json:"interface_name"`
	Backend        LinuxDNSBackend `json:"backend"`
	IfIndex        uint32          `json:"ifindex,omitempty"`
	ConnectionUUID string          `json:"connection_uuid,omitempty"`
}

// LinuxDNSLinkState is the indivisible per-link snapshot. A backend adapter
// may need multiple host calls, but it must expose them as one fail-closed
// logical transaction with compensation.
type LinuxDNSLinkState struct {
	Servers      []netip.Addr     `json:"servers"`
	Domains      []LinuxDNSDomain `json:"domains"`
	DefaultRoute bool             `json:"default_route"`
}

type LinuxDNSLink struct {
	Identity    LinuxDNSLinkIdentity `json:"identity"`
	State       LinuxDNSLinkState    `json:"state"`
	Owner       string               `json:"owner"`
	Fingerprint string               `json:"fingerprint,omitempty"`
}

type LinuxDNSTopology struct {
	Links []LinuxDNSLink `json:"links"`
}

type LinuxDNSIntent struct {
	Owner         string
	Link          LinuxDNSLinkIdentity
	Servers       []netip.Addr
	SearchDomains []string
	MatchDomains  []string
	DefaultRoute  bool
}

type LinuxDNSAction string

const (
	LinuxDNSSetLinkState     LinuxDNSAction = "set-link-state"
	LinuxDNSRestoreLinkState LinuxDNSAction = "restore-link-state"
)

// LinuxDNSCASPrecondition is data for a future executor. It MUST revalidate
// stable identity, owner and fingerprint immediately before apply/rollback.
type LinuxDNSCASPrecondition struct {
	Identity            string
	ExpectedOwner       string
	ExpectedFingerprint string
}

// LinuxDNSLedgerRequirement is deliberately not implemented here. A future
// executor must durably persist this transition as part of the same logical
// transaction and abort rather than overwrite divergent state.
type LinuxDNSLedgerRequirement struct {
	Required          bool
	ResourceIdentity  string
	BeforeFingerprint string
	AfterFingerprint  string
}

// LinuxDNSOperation always replaces one complete link snapshot. Servers,
// domains and DefaultRoute can never be planned as independent mutations.
type LinuxDNSOperation struct {
	ID                string
	Action            LinuxDNSAction
	Owner             string
	ResultOwner       string
	Identity          LinuxDNSLinkIdentity
	State             LinuxDNSLinkState
	Precondition      LinuxDNSCASPrecondition
	ResultFingerprint string
	Ledger            LinuxDNSLedgerRequirement
}

type LinuxDNSJournalEntry struct {
	Apply    LinuxDNSOperation
	Rollback LinuxDNSOperation
}

type LinuxDNSSatisfaction struct {
	Identity string
	Owner    string
	Owned    bool
}

type LinuxDNSPlan struct {
	Owner          string
	Identity       LinuxDNSLinkIdentity
	Journal        []LinuxDNSJournalEntry
	Rollback       []LinuxDNSOperation
	Satisfied      []LinuxDNSSatisfaction
	LedgerRequired bool
}

// PlanLinuxDNS is pure: it discovers no links, calls no backend and persists no
// ledger. It emits the complete transaction contract a future executor needs.
func PlanLinuxDNS(intent LinuxDNSIntent, topology LinuxDNSTopology) (LinuxDNSPlan, error) {
	if strings.TrimSpace(intent.Owner) == "" {
		return LinuxDNSPlan{}, errors.New("DNS owner is required")
	}
	if err := validateLinuxDNSLinkIdentity(intent.Link); err != nil {
		return LinuxDNSPlan{}, err
	}
	servers, err := normalizeLinuxDNSServers(intent.Servers)
	if err != nil {
		return LinuxDNSPlan{}, err
	}
	domains, err := normalizeLinuxDNSDomains(intent.SearchDomains, intent.MatchDomains)
	if err != nil {
		return LinuxDNSPlan{}, err
	}
	desired := LinuxDNSLinkState{Servers: servers, Domains: domains, DefaultRoute: intent.DefaultRoute}

	link, err := findLinuxDNSLink(intent.Link, topology)
	if err != nil {
		return LinuxDNSPlan{}, err
	}
	current, err := normalizeObservedLinkState(link.State)
	if err != nil {
		return LinuxDNSPlan{}, fmt.Errorf("observed DNS link state: %w", err)
	}
	currentFingerprint := linuxDNSStateFingerprint(current)
	if link.Fingerprint != "" && link.Fingerprint != currentFingerprint {
		return LinuxDNSPlan{}, errors.New("observed DNS link fingerprint does not match its complete snapshot")
	}

	plan := LinuxDNSPlan{Owner: intent.Owner, Identity: link.Identity, LedgerRequired: true}
	identity := linuxDNSIdentityKey(link.Identity)
	if equalLinuxDNSLinkState(current, desired) {
		plan.Satisfied = []LinuxDNSSatisfaction{{Identity: identity, Owner: link.Owner, Owned: link.Owner == intent.Owner}}
		return plan, nil
	}
	if link.Owner != "" && link.Owner != intent.Owner {
		return LinuxDNSPlan{}, fmt.Errorf("DNS link %s is owned by %q", intent.Link.InterfaceName, link.Owner)
	}
	if link.Owner == "" && !linuxDNSStateEmpty(current) {
		return LinuxDNSPlan{}, fmt.Errorf("DNS link %s has foreign state and cannot be replaced", intent.Link.InterfaceName)
	}

	desiredFingerprint := linuxDNSStateFingerprint(desired)
	apply := newLinuxDNSOperation(LinuxDNSSetLinkState, intent.Owner, intent.Owner, link.Identity, desired, link.Owner, currentFingerprint)
	rollback := newLinuxDNSOperation(LinuxDNSRestoreLinkState, intent.Owner, link.Owner, link.Identity, current, intent.Owner, desiredFingerprint)
	plan.Journal = []LinuxDNSJournalEntry{{Apply: apply, Rollback: rollback}}
	plan.Rollback = []LinuxDNSOperation{rollback}
	return plan, nil
}

func newLinuxDNSOperation(action LinuxDNSAction, actor, resultOwner string, identity LinuxDNSLinkIdentity, state LinuxDNSLinkState, expectedOwner, expectedFingerprint string) LinuxDNSOperation {
	resultFingerprint := linuxDNSStateFingerprint(state)
	operation := LinuxDNSOperation{
		Action: action, Owner: actor, ResultOwner: resultOwner, Identity: identity,
		State:             cloneLinuxDNSState(state),
		Precondition:      LinuxDNSCASPrecondition{Identity: linuxDNSIdentityKey(identity), ExpectedOwner: expectedOwner, ExpectedFingerprint: expectedFingerprint},
		ResultFingerprint: resultFingerprint,
	}
	operation.Ledger = LinuxDNSLedgerRequirement{Required: true, ResourceIdentity: operation.Precondition.Identity, BeforeFingerprint: expectedFingerprint, AfterFingerprint: resultFingerprint}
	operation.ID = "dns-" + linuxDNSFingerprint(fmt.Sprintf("%s|%s|%s|%s", action, operation.Precondition.Identity, expectedFingerprint, resultFingerprint))[:16]
	return operation
}

func findLinuxDNSLink(identity LinuxDNSLinkIdentity, topology LinuxDNSTopology) (LinuxDNSLink, error) {
	key := linuxDNSIdentityKey(identity)
	seen := map[string]struct{}{}
	var match *LinuxDNSLink
	for index := range topology.Links {
		link := topology.Links[index]
		if err := validateLinuxDNSLinkIdentity(link.Identity); err != nil {
			return LinuxDNSLink{}, fmt.Errorf("topology link %d: %w", index, err)
		}
		linkKey := linuxDNSIdentityKey(link.Identity)
		if _, duplicate := seen[linkKey]; duplicate {
			return LinuxDNSLink{}, fmt.Errorf("DNS topology contains duplicate stable identity %q", linkKey)
		}
		seen[linkKey] = struct{}{}
		if linkKey == key {
			copy := link
			match = &copy
		}
	}
	if match == nil {
		return LinuxDNSLink{}, fmt.Errorf("DNS topology has no supported link for stable identity %q", key)
	}
	return *match, nil
}

func validateLinuxDNSLinkIdentity(identity LinuxDNSLinkIdentity) error {
	if err := validateLinuxDNSInterfaceName(identity.InterfaceName); err != nil {
		return err
	}
	switch identity.Backend {
	case LinuxDNSBackendResolved:
		if identity.IfIndex == 0 {
			return errors.New("systemd-resolved link identity requires ifindex")
		}
		if identity.ConnectionUUID != "" {
			return errors.New("systemd-resolved link identity must not contain a connection UUID")
		}
	case LinuxDNSBackendNetworkManager:
		if strings.TrimSpace(identity.ConnectionUUID) == "" {
			return errors.New("NetworkManager link identity requires connection UUID")
		}
	case "":
		return errors.New("Linux DNS backend is required")
	default:
		return fmt.Errorf("unsupported Linux DNS backend %q", identity.Backend)
	}
	return nil
}

func normalizeLinuxDNSServers(input []netip.Addr) ([]netip.Addr, error) {
	if len(input) == 0 {
		return nil, errors.New("at least one DNS server is required")
	}
	servers := append([]netip.Addr(nil), input...)
	seen := make(map[string]struct{}, len(servers))
	for index, server := range servers {
		if !server.IsValid() || server.IsUnspecified() || server.IsMulticast() {
			return nil, fmt.Errorf("DNS server %d must be a valid unicast address", index)
		}
		server = server.Unmap()
		key := server.String()
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate DNS server %q", key)
		}
		seen[key] = struct{}{}
		servers[index] = server
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Compare(servers[j]) < 0 })
	return servers, nil
}

func normalizeObservedLinkState(input LinuxDNSLinkState) (LinuxDNSLinkState, error) {
	output := LinuxDNSLinkState{DefaultRoute: input.DefaultRoute}
	var err error
	if len(input.Servers) != 0 {
		output.Servers, err = normalizeLinuxDNSServers(input.Servers)
		if err != nil {
			return LinuxDNSLinkState{}, err
		}
	}
	if len(input.Domains) != 0 {
		search := make([]string, 0, len(input.Domains))
		match := make([]string, 0, len(input.Domains))
		for _, domain := range input.Domains {
			if domain.RouteOnly {
				match = append(match, domain.Name)
			} else {
				search = append(search, domain.Name)
			}
		}
		output.Domains, err = normalizeLinuxDNSDomains(search, match)
		if err != nil {
			return LinuxDNSLinkState{}, err
		}
	}
	return output, nil
}

func normalizeLinuxDNSDomains(searchDomains, matchDomains []string) ([]LinuxDNSDomain, error) {
	domains := make([]LinuxDNSDomain, 0, len(searchDomains)+len(matchDomains))
	seen := make(map[string]struct{}, cap(domains))
	appendDomains := func(values []string, routeOnly bool) error {
		for index, value := range values {
			normalized, err := normalizeLinuxDNSDomain(value, routeOnly)
			if err != nil {
				kind := "search"
				if routeOnly {
					kind = "match"
				}
				return fmt.Errorf("%s domain %d: %w", kind, index, err)
			}
			if _, duplicate := seen[normalized]; duplicate {
				return fmt.Errorf("duplicate DNS domain %q", normalized)
			}
			seen[normalized] = struct{}{}
			domains = append(domains, LinuxDNSDomain{Name: normalized, RouteOnly: routeOnly})
		}
		return nil
	}
	if err := appendDomains(searchDomains, false); err != nil {
		return nil, err
	}
	if err := appendDomains(matchDomains, true); err != nil {
		return nil, err
	}
	if len(domains) == 0 {
		return nil, errors.New("at least one DNS domain is required")
	}
	sort.Slice(domains, func(i, j int) bool {
		if domains[i].Name != domains[j].Name {
			return domains[i].Name < domains[j].Name
		}
		return !domains[i].RouteOnly && domains[j].RouteOnly
	})
	return domains, nil
}

func normalizeLinuxDNSDomain(value string, routeOnly bool) (string, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", errors.New("domain must be non-empty without surrounding whitespace")
	}
	if value == "." {
		if !routeOnly {
			return "", errors.New("root domain '.' is allowed only as RouteOnly")
		}
		return ".", nil
	}
	if strings.ContainsAny(value, "/\\") || strings.Contains(value, "..") {
		return "", errors.New("domain traversal is not allowed")
	}
	if strings.HasPrefix(value, "~") || strings.Contains(value, "*") {
		return "", errors.New("backend routing prefixes and wildcards are not allowed")
	}
	value = strings.ToLower(strings.TrimSuffix(value, "."))
	if value == "" || len(value) > 253 {
		return "", errors.New("domain length is invalid")
	}
	if address, err := netip.ParseAddr(value); err == nil && address.IsValid() {
		return "", errors.New("IP literals are not DNS domains")
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 {
			return "", errors.New("domain contains an empty or oversized label")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("domain label %q starts or ends with '-'", label)
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return "", fmt.Errorf("domain label %q contains invalid character %q", label, character)
		}
	}
	return value, nil
}

func validateLinuxDNSInterfaceName(name string) error {
	if name == "" || len(name) > 15 {
		return errors.New("Linux DNS interface name must contain 1 to 15 characters")
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("_.-", character) {
			continue
		}
		return fmt.Errorf("Linux DNS interface name contains invalid character %q", character)
	}
	return nil
}

func linuxDNSIdentityKey(identity LinuxDNSLinkIdentity) string {
	switch identity.Backend {
	case LinuxDNSBackendResolved:
		return fmt.Sprintf("backend=%s|ifindex=%d", identity.Backend, identity.IfIndex)
	case LinuxDNSBackendNetworkManager:
		return fmt.Sprintf("backend=%s|connection=%s", identity.Backend, identity.ConnectionUUID)
	default:
		return fmt.Sprintf("backend=%s|invalid", identity.Backend)
	}
}

func linuxDNSStateKey(state LinuxDNSLinkState) string {
	servers := make([]string, len(state.Servers))
	for index, server := range state.Servers {
		servers[index] = server.String()
	}
	domains := make([]string, len(state.Domains))
	for index, domain := range state.Domains {
		domains[index] = fmt.Sprintf("%s:%t", domain.Name, domain.RouteOnly)
	}
	return fmt.Sprintf("servers=%s|domains=%s|default-route=%t", strings.Join(servers, ","), strings.Join(domains, ","), state.DefaultRoute)
}

func linuxDNSStateFingerprint(state LinuxDNSLinkState) string {
	return linuxDNSFingerprint(linuxDNSStateKey(state))
}

func linuxDNSFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func equalLinuxDNSLinkState(left, right LinuxDNSLinkState) bool {
	return linuxDNSStateKey(left) == linuxDNSStateKey(right)
}

func linuxDNSStateEmpty(state LinuxDNSLinkState) bool {
	return len(state.Servers) == 0 && len(state.Domains) == 0 && !state.DefaultRoute
}

func cloneLinuxDNSState(input LinuxDNSLinkState) LinuxDNSLinkState {
	output := LinuxDNSLinkState{DefaultRoute: input.DefaultRoute}
	if input.Servers != nil {
		output.Servers = append([]netip.Addr{}, input.Servers...)
	}
	if input.Domains != nil {
		output.Domains = append([]LinuxDNSDomain{}, input.Domains...)
	}
	return output
}

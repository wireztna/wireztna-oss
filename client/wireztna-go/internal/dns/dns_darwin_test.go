//go:build darwin

package dns

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlanDarwinDNSIsPureAndDeterministic(t *testing.T) {
	plan, err := planDarwinDNS(legacyDarwinOwner, "10.200.0.53", []string{"Corp.Example.", "dev.example"}, nil)
	if err != nil {
		t.Fatalf("planDarwinDNS() error = %v", err)
	}

	want := darwinDNSPlan{
		OwnerID: legacyDarwinOwner,
		Writes: []darwinResolverWrite{
			{
				Zone:    "corp.example",
				Path:    "/etc/resolver/corp.example",
				Content: "# wireztna-managed owner=wireztna-legacy-darwin zone=corp.example\nnameserver 10.200.0.53\ntimeout 2\n",
				Command: darwinCommandPlan{
					Name:  "sudo",
					Args:  []string{"tee", "/etc/resolver/corp.example"},
					Stdin: "# wireztna-managed owner=wireztna-legacy-darwin zone=corp.example\nnameserver 10.200.0.53\ntimeout 2\n",
				},
			},
			{
				Zone:    "dev.example",
				Path:    "/etc/resolver/dev.example",
				Content: "# wireztna-managed owner=wireztna-legacy-darwin zone=dev.example\nnameserver 10.200.0.53\ntimeout 2\n",
				Command: darwinCommandPlan{
					Name:  "sudo",
					Args:  []string{"tee", "/etc/resolver/dev.example"},
					Stdin: "# wireztna-managed owner=wireztna-legacy-darwin zone=dev.example\nnameserver 10.200.0.53\ntimeout 2\n",
				},
			},
		},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("planDarwinDNS() mismatch\nwant: %#v\ngot:  %#v", want, plan)
	}

	again, err := planDarwinDNS(legacyDarwinOwner, "10.200.0.53", []string{"Corp.Example.", "dev.example"}, nil)
	if err != nil {
		t.Fatalf("second planDarwinDNS() error = %v", err)
	}
	if !reflect.DeepEqual(plan, again) {
		t.Fatalf("identical inputs produced different plans\nfirst:  %#v\nsecond: %#v", plan, again)
	}
}

func TestPlanDarwinDNSRejectsUnsafeOrAmbiguousInputsAtomically(t *testing.T) {
	tests := []struct {
		name      string
		ownerID   string
		tunnelDNS string
		zones     []string
	}{
		{name: "missing owner", ownerID: "", tunnelDNS: "10.200.0.53", zones: []string{"corp.example"}},
		{name: "invalid resolver", ownerID: legacyDarwinOwner, tunnelDNS: "resolver.example", zones: []string{"corp.example"}},
		{name: "no domains", ownerID: legacyDarwinOwner, tunnelDNS: "10.200.0.53"},
		{name: "root domain", ownerID: legacyDarwinOwner, tunnelDNS: "10.200.0.53", zones: []string{"."}},
		{name: "parent traversal", ownerID: legacyDarwinOwner, tunnelDNS: "10.200.0.53", zones: []string{"../escape"}},
		{name: "path separator", ownerID: legacyDarwinOwner, tunnelDNS: "10.200.0.53", zones: []string{"corp/example"}},
		{name: "empty domain", ownerID: legacyDarwinOwner, tunnelDNS: "10.200.0.53", zones: []string{""}},
		{name: "surrounding whitespace", ownerID: legacyDarwinOwner, tunnelDNS: "10.200.0.53", zones: []string{" corp.example"}},
		{name: "empty label", ownerID: legacyDarwinOwner, tunnelDNS: "10.200.0.53", zones: []string{"corp..example"}},
		{name: "leading hyphen", ownerID: legacyDarwinOwner, tunnelDNS: "10.200.0.53", zones: []string{"-corp.example"}},
		{name: "invalid character", ownerID: legacyDarwinOwner, tunnelDNS: "10.200.0.53", zones: []string{"corp_.example"}},
		{name: "IP domain", ownerID: legacyDarwinOwner, tunnelDNS: "10.200.0.53", zones: []string{"192.0.2.1"}},
		{name: "canonical duplicate", ownerID: legacyDarwinOwner, tunnelDNS: "10.200.0.53", zones: []string{"corp.example", "CORP.EXAMPLE."}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planDarwinDNS(test.ownerID, test.tunnelDNS, test.zones, nil)
			if err == nil {
				t.Fatalf("planDarwinDNS() unexpectedly accepted unsafe input: %#v", plan)
			}
			if len(plan.Writes) != 0 {
				t.Fatalf("rejected input returned a partial write plan: %#v", plan.Writes)
			}
		})
	}
}

func TestPlanDarwinDNSRefusesUnownedOrNonRegularDestinations(t *testing.T) {
	foreign := darwinResolverEntry{
		Name:    "corp.example",
		Kind:    darwinResolverRegular,
		Content: "nameserver 192.0.2.53\n",
	}
	tests := []struct {
		name     string
		existing darwinResolverEntry
	}{
		{name: "foreign regular file", existing: foreign},
		{name: "directory", existing: darwinResolverEntry{Name: "corp.example", Kind: darwinResolverDirectory}},
		{name: "symlink or special file", existing: darwinResolverEntry{Name: "corp.example", Kind: darwinResolverOther}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planDarwinDNS(
				legacyDarwinOwner,
				"10.200.0.53",
				[]string{"safe.example", "corp.example"},
				map[string]darwinResolverEntry{"corp.example": test.existing},
			)
			if err == nil {
				t.Fatalf("planDarwinDNS() unexpectedly accepted collision: %#v", plan)
			}
			if len(plan.Writes) != 0 {
				t.Fatalf("collision returned a partial write plan: %#v", plan.Writes)
			}
		})
	}
}

func TestPlanDarwinDNSAcceptsOwnedAndExactLegacyDestinations(t *testing.T) {
	owned := darwinResolverEntry{
		Name:    "corp.example",
		Kind:    darwinResolverRegular,
		Content: darwinManagedTag(legacyDarwinOwner, "corp.example") + "\nnameserver 10.200.0.53\ntimeout 2\n",
	}
	legacy := darwinResolverEntry{
		Name:    "corp.example",
		Kind:    darwinResolverRegular,
		Content: "# wireztna-managed\nnameserver 10.200.0.53\ntimeout 2\n",
	}

	for name, existing := range map[string]darwinResolverEntry{"owned": owned, "exact legacy": legacy} {
		t.Run(name, func(t *testing.T) {
			plan, err := planDarwinDNS(
				legacyDarwinOwner,
				"10.200.0.53",
				[]string{"corp.example"},
				map[string]darwinResolverEntry{"corp.example": existing},
			)
			if err != nil {
				t.Fatalf("planDarwinDNS() error = %v", err)
			}
			if len(plan.Writes) != 1 {
				t.Fatalf("planDarwinDNS() writes = %d, want 1", len(plan.Writes))
			}
		})
	}
}

func TestPlanDarwinDNSCleanupOnlyRemovesExactlyOwnedResolvers(t *testing.T) {
	ownedContent := "# wireztna-managed owner=wireztna-legacy-darwin zone=corp.example\nnameserver 10.200.0.53\ntimeout 2\n"
	entries := []darwinResolverEntry{
		{Name: "corp.example", Content: ownedContent},
		{Name: "old.example", Content: "# wireztna-managed\nnameserver 10.200.0.53\ntimeout 2\n"},
		{Name: "malformed-old.example", Content: "prefix # wireztna-managed\nnameserver invalid\ntimeout 2\n"},
		{Name: "foreign.example", Content: "# wireztna-managed owner=another-owner zone=foreign.example\nnameserver 10.200.0.53\ntimeout 2\n"},
		{Name: "mismatch.example", Content: ownedContent},
		{Name: "Corp.Example", Content: ownedContent},
		{Name: "directory.example", Kind: darwinResolverDirectory, Content: darwinManagedTag(legacyDarwinOwner, "directory.example")},
		{Name: "symlink.example", Kind: darwinResolverOther, Content: darwinManagedTag(legacyDarwinOwner, "symlink.example")},
		{Name: "../escape", Content: darwinManagedTag(legacyDarwinOwner, "../escape")},
	}

	plan := planDarwinDNSCleanup(legacyDarwinOwner, entries)
	want := darwinDNSCleanupPlan{
		OwnerID: legacyDarwinOwner,
		Removes: []darwinCommandPlan{
			{Name: "sudo", Args: []string{"rm", "-f", "/etc/resolver/corp.example"}},
			{Name: "sudo", Args: []string{"rm", "-f", "/etc/resolver/old.example"}},
		},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("planDarwinDNSCleanup() mismatch\nwant: %#v\ngot:  %#v", want, plan)
	}
	for _, remove := range plan.Removes {
		for _, arg := range remove.Args {
			if strings.ContainsAny(arg, "*?[") {
				t.Fatalf("cleanup plan contains a global/glob target: %#v", remove)
			}
		}
	}
}

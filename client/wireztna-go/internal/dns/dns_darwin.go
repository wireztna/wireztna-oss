//go:build darwin

package dns

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	macResolverDir    = "/etc/resolver"
	legacyDarwinOwner = "wireztna-legacy-darwin"
	managedTagPrefix  = "# wireztna-managed"
)

type darwinCommandPlan struct {
	Name  string
	Args  []string
	Stdin string
}

func (p darwinCommandPlan) command() *exec.Cmd {
	cmd := exec.Command(p.Name, p.Args...)
	if p.Stdin != "" {
		cmd.Stdin = strings.NewReader(p.Stdin)
	}
	return cmd
}

type darwinResolverWrite struct {
	Zone    string
	Path    string
	Content string
	Command darwinCommandPlan
}

type darwinDNSPlan struct {
	OwnerID string
	Writes  []darwinResolverWrite
}

type darwinResolverKind uint8

const (
	darwinResolverRegular darwinResolverKind = iota
	darwinResolverDirectory
	darwinResolverOther
)

type darwinResolverEntry struct {
	Name    string
	Kind    darwinResolverKind
	Content string
}

type darwinDNSCleanupPlan struct {
	OwnerID string
	Removes []darwinCommandPlan
}

func darwinManagedTag(ownerID, zone string) string {
	return fmt.Sprintf("%s owner=%s zone=%s", managedTagPrefix, ownerID, zone)
}

func normalizeDarwinZone(zone string) (string, error) {
	if zone == "." {
		return "", fmt.Errorf("root DNS domain is not supported")
	}
	if zone == "" {
		return "", fmt.Errorf("DNS domain is empty")
	}
	if strings.TrimSpace(zone) != zone {
		return "", fmt.Errorf("DNS domain contains surrounding whitespace")
	}
	if strings.ContainsAny(zone, `/\\`) || filepath.IsAbs(zone) {
		return "", fmt.Errorf("DNS domain contains a path separator")
	}

	canonical := strings.ToLower(strings.TrimSuffix(zone, "."))
	if canonical == "" {
		return "", fmt.Errorf("root DNS domain is not supported")
	}
	if len(canonical) > 253 {
		return "", fmt.Errorf("DNS domain exceeds 253 characters")
	}
	if net.ParseIP(canonical) != nil {
		return "", fmt.Errorf("DNS domain must not be an IP address")
	}

	for _, label := range strings.Split(canonical, ".") {
		if len(label) == 0 || len(label) > 63 {
			return "", fmt.Errorf("DNS domain contains an invalid label")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("DNS label must not start or end with a hyphen")
		}
		for _, char := range label {
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
				continue
			}
			return "", fmt.Errorf("DNS domain contains an invalid character")
		}
	}
	return canonical, nil
}

func isOwnedDarwinResolver(ownerID, zone, content string) bool {
	firstLine := strings.SplitN(content, "\n", 2)[0]
	return firstLine == darwinManagedTag(ownerID, zone)
}

func isExactLegacyDarwinResolver(content string) bool {
	lines := strings.Split(content, "\n")
	if len(lines) != 4 || lines[0] != managedTagPrefix || lines[2] != "timeout 2" || lines[3] != "" {
		return false
	}
	const nameserverPrefix = "nameserver "
	if !strings.HasPrefix(lines[1], nameserverPrefix) {
		return false
	}
	return net.ParseIP(strings.TrimPrefix(lines[1], nameserverPrefix)) != nil
}

func planDarwinDNS(ownerID, tunnelDNS string, zones []string, existing map[string]darwinResolverEntry) (darwinDNSPlan, error) {
	plan := darwinDNSPlan{OwnerID: ownerID}
	if ownerID == "" {
		return plan, fmt.Errorf("DNS owner is required")
	}
	if net.ParseIP(tunnelDNS) == nil {
		return plan, fmt.Errorf("tunnel DNS must be an IP address")
	}
	if len(zones) == 0 {
		return plan, fmt.Errorf("at least one DNS domain is required")
	}

	seen := make(map[string]struct{}, len(zones))
	plan.Writes = make([]darwinResolverWrite, 0, len(zones))
	for _, zone := range zones {
		canonical, err := normalizeDarwinZone(zone)
		if err != nil {
			return darwinDNSPlan{OwnerID: ownerID}, fmt.Errorf("invalid DNS domain %q: %w", zone, err)
		}
		if _, duplicate := seen[canonical]; duplicate {
			return darwinDNSPlan{OwnerID: ownerID}, fmt.Errorf("duplicate DNS domain %q", zone)
		}
		seen[canonical] = struct{}{}

		if current, exists := existing[canonical]; exists {
			if current.Kind != darwinResolverRegular {
				return darwinDNSPlan{OwnerID: ownerID}, fmt.Errorf("resolver destination for %q is not a regular file", zone)
			}
			if !isOwnedDarwinResolver(ownerID, canonical, current.Content) && !isExactLegacyDarwinResolver(current.Content) {
				return darwinDNSPlan{OwnerID: ownerID}, fmt.Errorf("resolver destination for %q is not owned by WireZTNA", zone)
			}
		}

		resolverPath := filepath.Join(macResolverDir, canonical)
		if filepath.Dir(resolverPath) != macResolverDir {
			return darwinDNSPlan{OwnerID: ownerID}, fmt.Errorf("DNS domain %q escapes resolver directory", zone)
		}
		content := fmt.Sprintf("%s\nnameserver %s\ntimeout 2\n", darwinManagedTag(ownerID, canonical), tunnelDNS)
		command := darwinCommandPlan{
			Name:  "sudo",
			Args:  []string{"tee", resolverPath},
			Stdin: content,
		}
		plan.Writes = append(plan.Writes, darwinResolverWrite{
			Zone:    canonical,
			Path:    resolverPath,
			Content: content,
			Command: command,
		})
	}
	return plan, nil
}

func planDarwinDNSCleanup(ownerID string, entries []darwinResolverEntry) darwinDNSCleanupPlan {
	plan := darwinDNSCleanupPlan{OwnerID: ownerID}
	for _, entry := range entries {
		if entry.Kind != darwinResolverRegular {
			continue
		}
		canonical, err := normalizeDarwinZone(entry.Name)
		if err != nil || canonical != entry.Name {
			continue
		}
		if !isOwnedDarwinResolver(ownerID, canonical, entry.Content) && !isExactLegacyDarwinResolver(entry.Content) {
			continue
		}
		plan.Removes = append(plan.Removes, darwinCommandPlan{
			Name: "sudo",
			Args: []string{"rm", "-f", filepath.Join(macResolverDir, canonical)},
		})
	}
	return plan
}

func darwinResolverKindForMode(mode os.FileMode) darwinResolverKind {
	switch {
	case mode.IsRegular():
		return darwinResolverRegular
	case mode.IsDir():
		return darwinResolverDirectory
	default:
		return darwinResolverOther
	}
}

func observeDarwinResolverDestinations(writes []darwinResolverWrite) (map[string]darwinResolverEntry, error) {
	observed := make(map[string]darwinResolverEntry, len(writes))
	for _, write := range writes {
		info, err := os.Lstat(write.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect resolver destination %s: %w", write.Path, err)
		}

		entry := darwinResolverEntry{Name: write.Zone, Kind: darwinResolverKindForMode(info.Mode())}
		if entry.Kind == darwinResolverRegular {
			content, readErr := os.ReadFile(write.Path)
			if readErr != nil {
				return nil, fmt.Errorf("read resolver destination %s: %w", write.Path, readErr)
			}
			entry.Content = string(content)
		}
		observed[write.Zone] = entry
	}
	return observed, nil
}

// configureOS sets up split DNS on macOS.
func (m *Manager) configureOS() error {
	return m.configureMacOS()
}

// cleanupOS removes split DNS configuration on macOS.
func (m *Manager) cleanupOS() error {
	return m.cleanupMacOS()
}

func (m *Manager) configureMacOS() error {
	candidate, err := planDarwinDNS(legacyDarwinOwner, m.tunnelDNS, m.zones, nil)
	if err != nil {
		return err
	}
	observed, err := observeDarwinResolverDestinations(candidate.Writes)
	if err != nil {
		return err
	}
	plan, err := planDarwinDNS(legacyDarwinOwner, m.tunnelDNS, m.zones, observed)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(macResolverDir, 0755); err != nil {
		if err := exec.Command("sudo", "mkdir", "-p", macResolverDir).Run(); err != nil {
			return fmt.Errorf("cannot create %s: %w", macResolverDir, err)
		}
	}

	for _, write := range plan.Writes {
		cmd := write.Command.command()
		cmd.Stdout = nil
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to write %s: %w", write.Path, err)
		}
	}
	return nil
}

func (m *Manager) cleanupMacOS() error {
	entries, err := os.ReadDir(macResolverDir)
	if err != nil {
		return nil
	}

	observed := make([]darwinResolverEntry, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		observation := darwinResolverEntry{Name: entry.Name(), Kind: darwinResolverKindForMode(info.Mode())}
		if observation.Kind == darwinResolverRegular {
			content, readErr := os.ReadFile(filepath.Join(macResolverDir, entry.Name()))
			if readErr != nil {
				continue
			}
			observation.Content = string(content)
		}
		observed = append(observed, observation)
	}

	plan := planDarwinDNSCleanup(legacyDarwinOwner, observed)
	for _, remove := range plan.Removes {
		_ = remove.command().Run()
	}
	return nil
}

//go:build darwin

package darwin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/wireztna/client/desktop/controller"
	"golang.org/x/sys/unix"
)

const resolverTagPrefix = "# wireztna-managed"

type dnsFileState struct {
	content []byte
	mode    os.FileMode
}

type dnsSnapshot map[string]dnsFileState

type dnsBackend interface {
	Snapshot(controller.OwnerID) (dnsSnapshot, error)
	Reconcile(controller.OwnerID, controller.DNSConfig) error
	Restore(controller.OwnerID, dnsSnapshot) error
	Healthy(controller.OwnerID, controller.DNSConfig) (bool, error)
}

type resolverDNSBackend struct {
	dir       string
	writeFile func(string, []byte, os.FileMode) error
}

func newResolverDNSBackend(dir string) *resolverDNSBackend {
	backend := &resolverDNSBackend{dir: dir}
	backend.writeFile = backend.atomicWrite
	return backend
}

func resolverTag(owner controller.OwnerID, zone string) string {
	return fmt.Sprintf("%s owner=%s zone=%s", resolverTagPrefix, owner, zone)
}

func resolverContent(owner controller.OwnerID, zone string, server netip.Addr) []byte {
	return []byte(fmt.Sprintf("%s\nnameserver %s\ntimeout 2\n", resolverTag(owner, zone), server))
}

func validateDNSOwner(owner controller.OwnerID) error {
	value := string(owner)
	if value == "" || strings.TrimSpace(value) != value || strings.IndexFunc(value, func(r rune) bool {
		return r <= 0x20 || r == 0x7f
	}) >= 0 {
		return errors.New("DNS owner is invalid")
	}
	return nil
}

func canonicalDNSConfig(value controller.DNSConfig) (controller.DNSConfig, error) {
	if len(value.Servers) == 0 && len(value.MatchDomains) == 0 && len(value.SearchDomains) == 0 {
		return controller.DNSConfig{}, nil
	}
	if len(value.Servers) != 1 || !value.Servers[0].IsValid() || len(value.MatchDomains) == 0 || len(value.SearchDomains) != 0 {
		return controller.DNSConfig{}, errors.New("Darwin split DNS requires one server and at least one match domain")
	}
	seen := make(map[string]struct{}, len(value.MatchDomains))
	zones := make([]string, 0, len(value.MatchDomains))
	for _, zone := range value.MatchDomains {
		canonical, err := canonicalDNSZone(zone)
		if err != nil {
			return controller.DNSConfig{}, fmt.Errorf("invalid Darwin split DNS domain %q: %w", zone, err)
		}
		if _, duplicate := seen[canonical]; duplicate {
			return controller.DNSConfig{}, fmt.Errorf("duplicate Darwin split DNS domain %q", zone)
		}
		seen[canonical] = struct{}{}
		zones = append(zones, canonical)
	}
	sort.Strings(zones)
	return controller.DNSConfig{Servers: []netip.Addr{value.Servers[0]}, MatchDomains: zones}, nil
}

func canonicalDNSZone(zone string) (string, error) {
	if zone == "" || strings.TrimSpace(zone) != zone || zone == "." || strings.ContainsAny(zone, `/\\`) {
		return "", errors.New("domain is empty, root, contains whitespace, or contains a path separator")
	}
	canonical := strings.ToLower(strings.TrimSuffix(zone, "."))
	if canonical == "" || len(canonical) > 253 || net.ParseIP(canonical) != nil {
		return "", errors.New("domain is root, too long, or an IP address")
	}
	for _, label := range strings.Split(canonical, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("domain contains an invalid label")
		}
		for _, char := range label {
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
				continue
			}
			return "", errors.New("domain contains an invalid character")
		}
	}
	return canonical, nil
}

type resolverObject struct {
	content []byte
	stat    unix.Stat_t
}

type quarantinedResolver struct {
	name   string
	object resolverObject
}

func (o resolverObject) identityMatches(other resolverObject) bool {
	return o.stat.Dev == other.stat.Dev && o.stat.Ino == other.stat.Ino
}

func (o resolverObject) unchangedFrom(other resolverObject) bool {
	return o.identityMatches(other) &&
		o.stat.Mode == other.stat.Mode &&
		o.stat.Nlink == other.stat.Nlink &&
		o.stat.Size == other.stat.Size &&
		o.stat.Mtim == other.stat.Mtim &&
		o.stat.Ctim == other.stat.Ctim &&
		o.stat.Gen == other.stat.Gen
}

func (o resolverObject) mode() os.FileMode {
	return os.FileMode(o.stat.Mode & 0o777)
}

func (b *resolverDNSBackend) openDirectory(create bool) (*os.File, error) {
	if create {
		if err := os.MkdirAll(b.dir, 0o755); err != nil {
			return nil, fmt.Errorf("create resolver directory: %w", err)
		}
	}
	fd, err := unix.Open(b.dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), b.dir), nil
}

func readResolverObjectAt(dirFD int, name string) (resolverObject, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return resolverObject{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return resolverObject{}, errors.New("open resolver descriptor")
	}
	defer file.Close()

	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return resolverObject{}, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		return resolverObject{}, errors.New("resolver is not a regular file")
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return resolverObject{}, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return resolverObject{}, err
	}
	result := resolverObject{content: content, stat: after}
	if !result.unchangedFrom(resolverObject{stat: before}) {
		return resolverObject{}, errors.New("resolver changed while being read")
	}
	return result, nil
}

func (b *resolverDNSBackend) Snapshot(owner controller.OwnerID) (dnsSnapshot, error) {
	if err := validateDNSOwner(owner); err != nil {
		return nil, err
	}
	result := make(dnsSnapshot)
	directory, err := b.openDirectory(false)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open resolver directory: %w", err)
	}
	defer directory.Close()
	dirFD := int(directory.Fd())
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("read resolver directory: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		var stat unix.Stat_t
		if err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, fmt.Errorf("inspect resolver %q: %w", name, err)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		object, err := readResolverObjectAt(dirFD, name)
		if err != nil {
			return nil, fmt.Errorf("read resolver %q: %w", name, err)
		}
		if isExactOwnedResolver(owner, name, object.content) {
			result[name] = dnsFileState{content: append([]byte(nil), object.content...), mode: object.mode()}
		}
	}
	return result, nil
}

func isExactOwnedResolver(owner controller.OwnerID, filename string, content []byte) bool {
	zone, err := canonicalDNSZone(filename)
	if err != nil || zone != filename {
		return false
	}
	firstLine, _, _ := strings.Cut(string(content), "\n")
	return firstLine == resolverTag(owner, zone)
}

func desiredResolverFiles(owner controller.OwnerID, desired controller.DNSConfig) map[string]dnsFileState {
	result := make(map[string]dnsFileState, len(desired.MatchDomains))
	if len(desired.Servers) == 0 {
		return result
	}
	for _, zone := range desired.MatchDomains {
		result[zone] = dnsFileState{content: resolverContent(owner, zone, desired.Servers[0]), mode: 0o644}
	}
	return result
}

func (b *resolverDNSBackend) Reconcile(owner controller.OwnerID, desired controller.DNSConfig) error {
	if err := validateDNSOwner(owner); err != nil {
		return err
	}
	expected := desiredResolverFiles(owner, desired)
	current, err := b.Snapshot(owner)
	if err != nil {
		return err
	}
	var mutationErrors []error
	for name, state := range expected {
		if err := b.writeFile(filepath.Join(b.dir, name), state.content, state.mode); err != nil {
			mutationErrors = append(mutationErrors, fmt.Errorf("write resolver %q: %w", name, err))
			break
		}
	}
	if len(mutationErrors) == 0 {
		for name := range current {
			if _, keep := expected[name]; keep {
				continue
			}
			if err := b.removeOwned(owner, name); err != nil {
				mutationErrors = append(mutationErrors, fmt.Errorf("remove stale resolver %q: %w", name, err))
			}
		}
	}
	if len(mutationErrors) > 0 {
		return errors.Join(mutationErrors...)
	}
	healthy, err := b.Healthy(owner, desired)
	if err != nil {
		return err
	}
	if !healthy {
		return errors.New("split DNS post-write verification failed")
	}
	return nil
}

func (b *resolverDNSBackend) Restore(owner controller.OwnerID, previous dnsSnapshot) error {
	current, err := b.Snapshot(owner)
	if err != nil {
		return err
	}
	var rollbackErrors []error
	for name := range current {
		if _, restore := previous[name]; restore {
			continue
		}
		if err := b.removeOwned(owner, name); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("remove resolver %q during rollback: %w", name, err))
		}
	}
	for name, state := range previous {
		if err := b.writeFile(filepath.Join(b.dir, name), state.content, state.mode); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore resolver %q: %w", name, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func randomResolverName(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

func syncResolverDirectory(dirFD int) error {
	if err := unix.Fsync(dirFD); err != nil {
		return fmt.Errorf("sync resolver directory: %w", err)
	}
	return nil
}

func (o resolverObject) sameVersion(other resolverObject) bool {
	return o.identityMatches(other) &&
		o.stat.Mode == other.stat.Mode &&
		o.stat.Nlink == other.stat.Nlink &&
		o.stat.Uid == other.stat.Uid &&
		o.stat.Gid == other.stat.Gid &&
		o.stat.Size == other.stat.Size &&
		o.stat.Mtim == other.stat.Mtim &&
		string(o.content) == string(other.content)
}

func restoreDisplacedAt(dirFD int, displacedName, destination string, displaced resolverObject) error {
	current, err := readResolverObjectAt(dirFD, displacedName)
	if err != nil {
		return fmt.Errorf("inspect displaced resolver %q: %w", displacedName, err)
	}
	if !current.sameVersion(displaced) {
		return fmt.Errorf("displaced resolver %q changed before recovery", displacedName)
	}

	if err := unix.RenameatxNp(dirFD, displacedName, dirFD, destination, unix.RENAME_EXCL); err == nil {
		restored, inspectErr := readResolverObjectAt(dirFD, destination)
		if inspectErr == nil && restored.sameVersion(displaced) {
			return syncResolverDirectory(dirFD)
		}
		var reverseErr error
		if inspectErr == nil {
			reverseErr = unix.RenameatxNp(dirFD, destination, dirFD, displacedName, unix.RENAME_EXCL)
			if errors.Is(reverseErr, unix.EEXIST) {
				reverseErr = unix.RenameatxNp(dirFD, destination, dirFD, displacedName, unix.RENAME_SWAP)
			}
			if reverseErr == nil {
				reversed, verifyErr := readResolverObjectAt(dirFD, displacedName)
				if verifyErr != nil || !reversed.sameVersion(restored) {
					reverseErr = errors.Join(errors.New("reversed recovery transition failed verification"), verifyErr)
				}
			}
		}
		return errors.Join(errors.New("recovered resolver changed during exclusive transition"), inspectErr, reverseErr, syncResolverDirectory(dirFD))
	} else if !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("restore displaced resolver %q: %w", destination, err)
	}

	destinationBefore, err := readResolverObjectAt(dirFD, destination)
	if err != nil {
		return fmt.Errorf("inspect concurrent resolver %q before recovery: %w", destination, err)
	}
	if err := unix.RenameatxNp(dirFD, displacedName, dirFD, destination, unix.RENAME_SWAP); err != nil {
		return fmt.Errorf("swap displaced resolver %q back into place: %w", destination, err)
	}
	restored, restoredErr := readResolverObjectAt(dirFD, destination)
	preserved, preservedErr := readResolverObjectAt(dirFD, displacedName)
	if restoredErr == nil && preservedErr == nil && restored.sameVersion(displaced) && preserved.sameVersion(destinationBefore) {
		return errors.Join(syncResolverDirectory(dirFD), fmt.Errorf("concurrent resolver at %q preserved as %q", destination, displacedName))
	}

	reverseErr := unix.RenameatxNp(dirFD, displacedName, dirFD, destination, unix.RENAME_SWAP)
	if reverseErr == nil && restoredErr == nil && preservedErr == nil {
		reversedSource, sourceErr := readResolverObjectAt(dirFD, displacedName)
		reversedDestination, destinationErr := readResolverObjectAt(dirFD, destination)
		if sourceErr != nil || destinationErr != nil || !reversedSource.sameVersion(restored) || !reversedDestination.sameVersion(preserved) {
			reverseErr = errors.Join(errors.New("reversed recovery swap failed verification"), sourceErr, destinationErr)
		}
	}
	return errors.Join(errors.New("recovered resolver changed during swap transition"), restoredErr, preservedErr, reverseErr, syncResolverDirectory(dirFD))
}

func quarantineOwnedAt(dirFD int, owner controller.OwnerID, name string) (quarantinedResolver, error) {
	before, err := readResolverObjectAt(dirFD, name)
	if err != nil {
		return quarantinedResolver{}, err
	}
	if !isExactOwnedResolver(owner, name, before.content) {
		return quarantinedResolver{}, errors.New("resolver ownership changed before mutation")
	}
	quarantineName, err := randomResolverName(".wireztna-quarantine-")
	if err != nil {
		return quarantinedResolver{}, err
	}
	if err := unix.RenameatxNp(dirFD, name, dirFD, quarantineName, unix.RENAME_EXCL); err != nil {
		return quarantinedResolver{}, err
	}
	after, inspectErr := readResolverObjectAt(dirFD, quarantineName)
	if inspectErr != nil || !after.sameVersion(before) || !isExactOwnedResolver(owner, name, after.content) {
		var recoveryErr error
		if inspectErr == nil {
			recoveryErr = restoreDisplacedAt(dirFD, quarantineName, name, after)
		}
		return quarantinedResolver{}, errors.Join(errors.New("resolver changed between validation and quarantine"), inspectErr, recoveryErr)
	}
	if err := syncResolverDirectory(dirFD); err != nil {
		return quarantinedResolver{}, errors.Join(err, restoreDisplacedAt(dirFD, quarantineName, name, after))
	}
	return quarantinedResolver{name: quarantineName, object: after}, nil
}

// discardVerifiedAt atomically captures an unpredictable non-canonical
// quarantine under a second unpredictable name before inspecting it. This
// prevents validation of one pathname object followed by removal of a raced
// replacement at that pathname. Canonical resolver paths are never accepted.
func discardVerifiedAt(dirFD int, name string, expected resolverObject, verify func(resolverObject) bool) error {
	if !strings.HasPrefix(name, ".wireztna-") {
		return fmt.Errorf("refusing to discard canonical resolver path %q", name)
	}
	if verify == nil {
		return errors.New("resolver discard verification predicate is required")
	}
	capturedName, err := randomResolverName(".wireztna-discard-")
	if err != nil {
		return err
	}
	if err := unix.RenameatxNp(dirFD, name, dirFD, capturedName, unix.RENAME_EXCL); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("capture resolver quarantine %q: %w", name, err)
	}
	captured, inspectErr := readResolverObjectAt(dirFD, capturedName)
	if inspectErr != nil {
		return errors.Join(fmt.Errorf("inspect captured resolver quarantine %q", capturedName), inspectErr, syncResolverDirectory(dirFD))
	}
	if !captured.sameVersion(expected) || !verify(captured) {
		restoreErr := restoreDisplacedAt(dirFD, capturedName, name, captured)
		return errors.Join(fmt.Errorf("refusing to discard changed quarantine %q", name), restoreErr)
	}
	if err := syncResolverDirectory(dirFD); err != nil {
		return errors.Join(err, restoreDisplacedAt(dirFD, capturedName, name, captured))
	}
	if err := unix.Unlinkat(dirFD, capturedName, 0); err != nil {
		return fmt.Errorf("discard captured resolver quarantine %q: %w", capturedName, err)
	}
	return syncResolverDirectory(dirFD)
}

func createStagedResolverAt(dirFD int, content []byte, mode os.FileMode) (name string, object resolverObject, resultErr error) {
	for attempts := 0; attempts < 128; attempts++ {
		candidate, err := randomResolverName(".wireztna-resolver-")
		if err != nil {
			return "", resolverObject{}, err
		}
		fd, err := unix.Openat(dirFD, candidate, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", resolverObject{}, err
		}
		file := os.NewFile(uintptr(fd), candidate)
		var initial unix.Stat_t
		if statErr := unix.Fstat(fd, &initial); statErr != nil {
			_ = file.Close()
			return "", resolverObject{}, statErr
		}
		cleanup := func(cause error) error {
			closeErr := file.Close()
			expected := resolverObject{stat: initial}
			removeErr := discardVerifiedAt(dirFD, candidate, expected, func(current resolverObject) bool {
				return current.identityMatches(expected)
			})
			return errors.Join(cause, closeErr, removeErr)
		}
		if _, err := file.Write(content); err != nil {
			return "", resolverObject{}, cleanup(err)
		}
		if err := file.Sync(); err != nil {
			return "", resolverObject{}, cleanup(err)
		}
		if err := file.Chmod(mode.Perm()); err != nil {
			return "", resolverObject{}, cleanup(err)
		}
		if err := file.Sync(); err != nil {
			return "", resolverObject{}, cleanup(err)
		}
		var final unix.Stat_t
		if err := unix.Fstat(fd, &final); err != nil {
			return "", resolverObject{}, cleanup(err)
		}
		if err := file.Close(); err != nil {
			return "", resolverObject{}, errors.Join(err, discardVerifiedAt(dirFD, candidate, resolverObject{stat: final}, func(current resolverObject) bool {
				return current.identityMatches(resolverObject{stat: final})
			}))
		}
		return candidate, resolverObject{content: append([]byte(nil), content...), stat: final}, nil
	}
	return "", resolverObject{}, errors.New("could not allocate resolver temporary file")
}

func resolverOwnerFromContent(name string, content []byte) (controller.OwnerID, error) {
	firstLine, _, _ := strings.Cut(string(content), "\n")
	rest, ok := strings.CutPrefix(firstLine, resolverTagPrefix+" owner=")
	if !ok {
		return "", errors.New("resolver content has no ownership tag")
	}
	ownerText, zone, ok := strings.Cut(rest, " zone=")
	owner := controller.OwnerID(ownerText)
	if !ok || zone != name || validateDNSOwner(owner) != nil || firstLine != resolverTag(owner, name) {
		return "", errors.New("resolver content has an invalid ownership tag")
	}
	return owner, nil
}

func exactStagedResolver(expected resolverObject, content []byte, mode os.FileMode) func(resolverObject) bool {
	return func(current resolverObject) bool {
		return current.identityMatches(expected) && current.mode().Perm() == mode.Perm() && string(current.content) == string(content)
	}
}

func moveRacedTargetAsideAt(dirFD int, name string, observed resolverObject) error {
	for attempts := 0; attempts < 128; attempts++ {
		quarantineName, err := randomResolverName(".wireztna-raced-")
		if err != nil {
			return err
		}
		if err := unix.RenameatxNp(dirFD, name, dirFD, quarantineName, unix.RENAME_EXCL); errors.Is(err, unix.EEXIST) {
			continue
		} else if err != nil {
			return err
		}
		quarantined, inspectErr := readResolverObjectAt(dirFD, quarantineName)
		if inspectErr == nil && quarantined.sameVersion(observed) {
			return errors.Join(syncResolverDirectory(dirFD), fmt.Errorf("raced resolver %q preserved as %q", name, quarantineName))
		}
		var recoveryErr error
		if inspectErr == nil {
			recoveryErr = restoreDisplacedAt(dirFD, quarantineName, name, quarantined)
		}
		return errors.Join(errors.New("raced resolver changed while moving aside"), inspectErr, recoveryErr)
	}
	return errors.New("could not allocate raced resolver quarantine")
}

func rollbackPublishedAt(dirFD int, name string, staged resolverObject, old *quarantinedResolver, content []byte, mode os.FileMode) error {
	current, err := readResolverObjectAt(dirFD, name)
	if errors.Is(err, unix.ENOENT) {
		if old != nil {
			return restoreDisplacedAt(dirFD, old.name, name, old.object)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if current.identityMatches(staged) {
		failedName, nameErr := randomResolverName(".wireztna-failed-")
		if nameErr != nil {
			return nameErr
		}
		if err := unix.RenameatxNp(dirFD, name, dirFD, failedName, unix.RENAME_EXCL); err != nil {
			return err
		}
		failed, inspectErr := readResolverObjectAt(dirFD, failedName)
		if inspectErr != nil || !failed.identityMatches(staged) {
			var recoveryErr error
			if inspectErr == nil {
				recoveryErr = restoreDisplacedAt(dirFD, failedName, name, failed)
			}
			return errors.Join(errors.New("published resolver changed during rollback"), inspectErr, recoveryErr)
		}
		var restoreErr error
		if old != nil {
			restoreErr = restoreDisplacedAt(dirFD, old.name, name, old.object)
		}
		var discardErr error
		if exactStagedResolver(staged, content, mode)(failed) {
			discardErr = discardVerifiedAt(dirFD, failedName, failed, exactStagedResolver(staged, content, mode))
		}
		return errors.Join(restoreErr, discardErr)
	}
	if old != nil {
		return restoreDisplacedAt(dirFD, old.name, name, old.object)
	}
	return moveRacedTargetAsideAt(dirFD, name, current)
}

func (b *resolverDNSBackend) removeOwned(owner controller.OwnerID, name string) error {
	directory, err := b.openDirectory(false)
	if err != nil {
		return err
	}
	defer directory.Close()
	dirFD := int(directory.Fd())
	quarantined, err := quarantineOwnedAt(dirFD, owner, name)
	if err != nil {
		return err
	}
	return discardVerifiedAt(dirFD, quarantined.name, quarantined.object, func(current resolverObject) bool {
		return isExactOwnedResolver(owner, name, current.content)
	})
}

func (b *resolverDNSBackend) Healthy(owner controller.OwnerID, desired controller.DNSConfig) (bool, error) {
	actual, err := b.Snapshot(owner)
	if err != nil {
		return false, err
	}
	expected := desiredResolverFiles(owner, desired)
	if len(actual) != len(expected) {
		return false, nil
	}
	for name, state := range expected {
		observed, ok := actual[name]
		if !ok || observed.mode.Perm() != state.mode.Perm() || string(observed.content) != string(state.content) {
			return false, nil
		}
	}
	return true, nil
}

func (b *resolverDNSBackend) atomicWrite(path string, content []byte, mode os.FileMode) (result error) {
	if filepath.Clean(filepath.Dir(path)) != filepath.Clean(b.dir) {
		return errors.New("resolver destination escaped the managed directory")
	}
	name := filepath.Base(path)
	canonical, err := canonicalDNSZone(name)
	if err != nil || canonical != name {
		return errors.New("resolver destination has an invalid zone name")
	}
	owner, err := resolverOwnerFromContent(name, content)
	if err != nil {
		return err
	}
	directory, err := b.openDirectory(true)
	if err != nil {
		return err
	}
	defer directory.Close()
	dirFD := int(directory.Fd())

	stagedName, staged, err := createStagedResolverAt(dirFD, content, mode)
	if err != nil {
		return err
	}
	stagedPresent := true
	defer func() {
		if !stagedPresent {
			return
		}
		cleanupErr := discardVerifiedAt(dirFD, stagedName, staged, exactStagedResolver(staged, content, mode))
		result = errors.Join(result, cleanupErr)
	}()

	stagedOnDisk, err := readResolverObjectAt(dirFD, stagedName)
	if err != nil || !exactStagedResolver(staged, content, mode)(stagedOnDisk) {
		return errors.Join(errors.New("staged resolver changed before publication"), err)
	}

	var old *quarantinedResolver
	if _, err := readResolverObjectAt(dirFD, name); err == nil {
		quarantined, quarantineErr := quarantineOwnedAt(dirFD, owner, name)
		if quarantineErr != nil {
			return quarantineErr
		}
		old = &quarantined
	} else if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("inspect resolver destination %q: %w", name, err)
	}

	if err := unix.RenameatxNp(dirFD, stagedName, dirFD, name, unix.RENAME_EXCL); err != nil {
		var restoreErr error
		if old != nil {
			restoreErr = restoreDisplacedAt(dirFD, old.name, name, old.object)
		}
		return errors.Join(fmt.Errorf("publish resolver with exclusive rename: %w", err), restoreErr)
	}
	stagedPresent = false
	if err := syncResolverDirectory(dirFD); err != nil {
		return errors.Join(err, rollbackPublishedAt(dirFD, name, staged, old, content, mode))
	}
	published, inspectErr := readResolverObjectAt(dirFD, name)
	if inspectErr != nil || !exactStagedResolver(staged, content, mode)(published) {
		return errors.Join(errors.New("published resolver failed inode, mode, or content verification"), inspectErr, rollbackPublishedAt(dirFD, name, staged, old, content, mode))
	}
	if old != nil {
		if err := discardVerifiedAt(dirFD, old.name, old.object, func(current resolverObject) bool {
			return isExactOwnedResolver(owner, name, current.content)
		}); err != nil {
			return err
		}
	}
	return nil
}

// DNSManager owns only owner-scoped split DNS. Full-tunnel mode deliberately
// preserves the host's physical DNS configuration; public DNS traffic follows
// the full-tunnel routes without giving wg-quick global DNS ownership.
type DNSManager struct {
	mu      sync.Mutex
	owner   controller.OwnerID
	backend dnsBackend
	current controller.DNSConfig
}

func (m *DNSManager) Apply(ctx context.Context, owner controller.OwnerID, desired controller.DNSConfig) (controller.Undo, error) {
	if ctx == nil || owner != m.owner {
		return controller.Undo{}, errors.New("DNS owner or context is invalid")
	}
	if err := validateDNSOwner(owner); err != nil {
		return controller.Undo{}, err
	}
	canonical, err := canonicalDNSConfig(desired)
	if err != nil {
		return controller.Undo{}, err
	}
	if err := ctx.Err(); err != nil {
		return controller.Undo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previousConfig := cloneDNSConfig(m.current)
	previousFiles, err := m.backend.Snapshot(owner)
	if err != nil {
		return controller.Undo{}, err
	}
	undo, err := controller.NewUndo(owner, func(undoCtx context.Context, undoOwner controller.OwnerID) error {
		return m.restore(undoCtx, undoOwner, previousConfig, previousFiles)
	})
	if err != nil {
		return controller.Undo{}, err
	}
	if err := m.backend.Reconcile(owner, canonical); err != nil {
		rollbackErr := m.backend.Restore(owner, previousFiles)
		return undo, errors.Join(err, rollbackErr)
	}
	m.current = cloneDNSConfig(canonical)
	if err := ctx.Err(); err != nil {
		return undo, err
	}
	return undo, nil
}

func (m *DNSManager) restore(ctx context.Context, owner controller.OwnerID, previousConfig controller.DNSConfig, previousFiles dnsSnapshot) error {
	if owner != m.owner {
		return errors.New("DNS rollback owner mismatch")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.backend.Restore(owner, previousFiles); err != nil {
		return err
	}
	m.current = cloneDNSConfig(previousConfig)
	return ctx.Err()
}

func (m *DNSManager) Health(ctx context.Context, owner controller.OwnerID) (controller.DNSHealth, error) {
	if ctx == nil || owner != m.owner {
		return controller.DNSHealth{}, errors.New("DNS health owner or context is invalid")
	}
	if err := ctx.Err(); err != nil {
		return controller.DNSHealth{}, err
	}
	m.mu.Lock()
	expected := cloneDNSConfig(m.current)
	m.mu.Unlock()
	healthy, err := m.backend.Healthy(owner, expected)
	if err != nil {
		return controller.DNSHealth{}, err
	}
	if !healthy {
		return controller.DNSHealth{Status: controller.HealthUnhealthy, Detail: "split DNS does not exactly match desired owner state"}, nil
	}
	return controller.DNSHealth{Status: controller.HealthHealthy, Detail: "split DNS exactly matches desired owner state"}, nil
}

func validateDNSConfig(value controller.DNSConfig) error {
	_, err := canonicalDNSConfig(value)
	return err
}

func cloneDNSConfig(value controller.DNSConfig) controller.DNSConfig {
	value.Servers = append([]netip.Addr(nil), value.Servers...)
	value.SearchDomains = append([]string(nil), value.SearchDomains...)
	value.MatchDomains = append([]string(nil), value.MatchDomains...)
	return value
}

//go:build darwin

package darwin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wireztna/client/internal/config"
	"golang.org/x/sys/unix"
)

const (
	defaultJournalFilename = "desktop-service-applied.json"
	defaultIntentFilename  = "desktop-service-intent.json"
	defaultJournalRoot     = "/var/db/wireztna/desktop"
)

// ServicePaths are fully resolved paths for one explicitly configured user.
type ServicePaths struct {
	OwnerUID    uint64
	ConfigDir   string
	ConfigFile  string
	TokenFile   string
	JournalFile string
	IntentFile  string
}

// ResolveClientPaths validates only owner-controlled config inputs. It performs
// no journal, service, IPC, or host-state work and is safe for build-time checks.
func ResolveClientPaths(ownerUID uint64, configDir string) (ServicePaths, error) {
	if ownerUID == 0 || ownerUID > math.MaxUint32 {
		return ServicePaths{}, errors.New("owner UID must identify a non-root desktop user within the operating-system UID range")
	}
	if configDir == "" || !filepath.IsAbs(configDir) || filepath.Clean(configDir) != configDir {
		return ServicePaths{}, errors.New("config directory must be an explicit clean absolute path")
	}
	resolved, err := filepath.EvalSymlinks(configDir)
	if err != nil || resolved != configDir {
		return ServicePaths{}, errors.New("config directory must exist and contain no symlink indirection")
	}
	if err := validateOwnedDirectory(configDir, ownerUID); err != nil {
		return ServicePaths{}, err
	}
	paths := ServicePaths{
		OwnerUID:   ownerUID,
		ConfigDir:  configDir,
		ConfigFile: filepath.Join(configDir, "config.yaml"),
		TokenFile:  filepath.Join(configDir, "token"),
	}
	if err := validateOwnedFile(paths.ConfigFile, ownerUID); err != nil {
		return ServicePaths{}, fmt.Errorf("unsafe config file: %w", err)
	}
	if err := validateOptionalOwnedFile(paths.TokenFile, ownerUID); err != nil {
		return ServicePaths{}, fmt.Errorf("unsafe token file: %w", err)
	}
	return paths, nil
}

// ResolveServicePaths validates owner-controlled config inputs and selects a
// process-owned journal location. A privileged service defaults to /var/db so
// the desktop user cannot replace lease inodes; an explicit journal is accepted
// only below an already-private directory owned by the service process.
func ResolveServicePaths(ownerUID uint64, configDir, journalPath string) (ServicePaths, error) {
	paths, err := ResolveClientPaths(ownerUID, configDir)
	if err != nil {
		return ServicePaths{}, err
	}
	defaultJournal := filepath.Join(defaultJournalRoot, fmt.Sprint(ownerUID), defaultJournalFilename)
	if journalPath == "" {
		journalPath = defaultJournal
	}
	if !filepath.IsAbs(journalPath) || filepath.Clean(journalPath) != journalPath || filepath.Base(journalPath) == "." {
		return ServicePaths{}, errors.New("journal path must be a clean absolute file path")
	}
	journalParent := filepath.Dir(journalPath)
	if _, err := os.Lstat(journalParent); err == nil {
		if err := validatePrivateProcessDirectory(journalParent); err != nil {
			return ServicePaths{}, fmt.Errorf("unsafe journal directory: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) || journalPath != defaultJournal {
		return ServicePaths{}, errors.New("custom journal directory must already exist and be private to the service process")
	}
	if err := validateOptionalOwnedFile(journalPath, uint64(os.Geteuid())); err != nil {
		return ServicePaths{}, fmt.Errorf("unsafe journal path: %w", err)
	}
	paths.JournalFile = journalPath
	paths.IntentFile = filepath.Join(journalParent, defaultIntentFilename)
	if paths.IntentFile == paths.JournalFile {
		return ServicePaths{}, errors.New("journal and durable intent paths must be distinct")
	}
	if err := validateOptionalOwnedFile(paths.IntentFile, uint64(os.Geteuid())); err != nil {
		return ServicePaths{}, fmt.Errorf("unsafe durable intent path: %w", err)
	}
	return paths, nil
}

// PrepareJournalPath creates only the fixed default hierarchy. Custom parents
// must pre-exist, which prevents a privileged service from creating arbitrary
// directory trees from command-line input.
func PrepareJournalPath(paths ServicePaths, ownerUID uint64) error {
	defaultParent := filepath.Join(defaultJournalRoot, fmt.Sprint(ownerUID))
	parent := filepath.Dir(paths.JournalFile)
	if parent != defaultParent {
		return validatePrivateProcessDirectory(parent)
	}
	for _, directory := range []string{
		filepath.Dir(defaultJournalRoot),
		defaultJournalRoot,
		defaultParent,
	} {
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create service journal directory: %w", err)
		}
		if err := validatePrivateProcessDirectory(directory); err != nil {
			return fmt.Errorf("unsafe service journal directory: %w", err)
		}
	}
	return nil
}

func validatePrivateProcessDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("journal directory must be a real process-private directory")
	}
	uid, ok := fileOwnerUID(info)
	if !ok || uid != uint64(os.Geteuid()) {
		return errors.New("journal directory owner does not match service process")
	}
	return nil
}

// LoadClientConfig opens and parses the explicit YAML config read-only after
// path validation. It does not consult global config or harden/rewrite the file.
func LoadClientConfig(paths ServicePaths) (*config.ClientConfig, error) {
	fd, err := unix.Open(paths.ConfigFile, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open explicit client configuration: %w", err)
	}
	file := os.NewFile(uintptr(fd), paths.ConfigFile)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("wrap explicit client configuration")
	}
	defer file.Close()
	if err := validateOpenFile(file, paths.OwnerUID); err != nil {
		return nil, fmt.Errorf("unsafe open client configuration: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil {
		return nil, fmt.Errorf("read explicit client configuration: %w", err)
	}
	if len(data) > 1024*1024 {
		return nil, errors.New("explicit client configuration exceeds 1 MiB")
	}
	clientConfig, err := config.ParseClientConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parse explicit client configuration: %w", err)
	}
	return clientConfig, nil
}

// SecureTokenSource opens the explicit token with O_NOFOLLOW for every connect,
// allowing a newly authenticated UI to refresh it without restarting service.
func SecureTokenSource(path string, ownerUID uint64) TokenSource {
	return func(ctx context.Context) (string, error) {
		if ctx == nil {
			return "", errors.New("token context is required")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
				return "", nil
			}
			return "", errors.New("token is unavailable")
		}
		file := os.NewFile(uintptr(fd), path)
		defer file.Close()
		if err := validateOpenFile(file, ownerUID); err != nil {
			return "", errors.New("token file is unsafe")
		}
		data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
		if err != nil || len(data) > 64*1024 {
			return "", errors.New("token is unavailable")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
}

func validateOwnedDirectory(path string, ownerUID uint64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect config directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("config path is not a real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("config directory must not be accessible by group or other users")
	}
	uid, ok := fileOwnerUID(info)
	if !ok || uid != ownerUID {
		return errors.New("config directory owner does not match owner UID")
	}
	return nil
}

func validateOwnedFile(path string, ownerUID uint64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("file must not be accessible by group or other users")
	}
	uid, ok := fileOwnerUID(info)
	if !ok || uid != ownerUID {
		return errors.New("file owner does not match owner UID")
	}
	return nil
}

func validateOptionalOwnedFile(path string, ownerUID uint64) error {
	err := validateOwnedFile(path, ownerUID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func validateOpenFile(file *os.File, ownerUID uint64) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("open file type or permissions are unsafe")
	}
	uid, ok := fileOwnerUID(info)
	if !ok || uid != ownerUID {
		return errors.New("open file owner does not match owner UID")
	}
	return nil
}

func fileOwnerUID(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Uid), true
}

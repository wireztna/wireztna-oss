// Package release defines the versioned desktop release manifest schema.
package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// ManifestSchemaVersion is the only manifest schema version currently supported.
	ManifestSchemaVersion = 1
	// MaxManifestBytes bounds untrusted manifest input before JSON decoding.
	MaxManifestBytes int64 = 1 << 20
)

var (
	canonicalVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	fullGitSHAPattern       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern           = regexp.MustCompile(`^[0-9a-f]{64}$`)

	// ErrEmptyManifest indicates that no JSON document was supplied.
	ErrEmptyManifest = errors.New("release manifest is empty")
	// ErrManifestTooLarge indicates that the manifest exceeded MaxManifestBytes.
	ErrManifestTooLarge = errors.New("release manifest exceeds size limit")
)

// Channel identifies an independently promoted release stream.
type Channel string

const (
	ChannelPilot  Channel = "pilot"
	ChannelBeta   Channel = "beta"
	ChannelStable Channel = "stable"
	ChannelGA     Channel = "ga"
)

// OS identifies the target operating system of an artifact.
type OS string

const (
	OSLinux   OS = "linux"
	OSMacOS   OS = "macos"
	OSWindows OS = "windows"
)

// Architecture identifies the target processor architecture of an artifact.
type Architecture string

const (
	ArchitectureAMD64 Architecture = "amd64"
	ArchitectureARM64 Architecture = "arm64"
)

// ArtifactType identifies a platform-owned installer or portable distribution format.
type ArtifactType string

const (
	ArtifactTypeDEB      ArtifactType = "deb"
	ArtifactTypePKG      ArtifactType = "pkg"
	ArtifactTypeMSI      ArtifactType = "msi"
	ArtifactTypePortable ArtifactType = "portable"
)

// NativeSignatureStatus records the native platform signature requirement and result.
type NativeSignatureStatus string

const (
	NativeSignatureRequiredAndVerified NativeSignatureStatus = "required-and-verified"
	NativeSignatureAdHoc               NativeSignatureStatus = "ad-hoc"
)

// Manifest is the schema-versioned source of desktop release metadata.
type Manifest struct {
	SchemaVersion           int           `json:"schema_version"`
	Channel                 Channel       `json:"channel"`
	Version                 string        `json:"version"`
	GitSHA                  string        `json:"git_sha"`
	PublishedAt             time.Time     `json:"published_at"`
	ExpiresAt               time.Time     `json:"expires_at"`
	ChannelEpoch            uint64        `json:"channel_epoch"`
	MinimumSupportedVersion string        `json:"minimum_supported_version"`
	RolloutPercentage       int           `json:"rollout_percentage"`
	Compatibility           Compatibility `json:"compatibility"`
	Artifacts               []Artifact    `json:"artifacts"`
}

// Compatibility declares service, UI, IPC, migration, and rollback boundaries.
type Compatibility struct {
	IPCMin           uint32   `json:"ipc_min"`
	IPCMax           uint32   `json:"ipc_max"`
	ServiceMin       string   `json:"service_min"`
	UIMin            string   `json:"ui_min"`
	MigrationEpoch   uint64   `json:"migration_epoch"`
	RollbackTo       []string `json:"rollback_to"`
	ReauthOnRollback bool     `json:"reauth_on_rollback"`
}

// Artifact describes one immutable platform installer and its verification metadata.
type Artifact struct {
	OS              OS                    `json:"os"`
	Architecture    Architecture          `json:"arch"`
	Type            ArtifactType          `json:"type"`
	URL             string                `json:"url"`
	Size            int64                 `json:"size"`
	SHA256          string                `json:"sha256"`
	NativeSignature NativeSignatureStatus `json:"native_signature"`
	ProvenanceURL   string                `json:"provenance_url"`
}

// canonicalManifestPayload fixes the signed payload field set and order. Detached
// signatures deliberately live outside this representation.
type canonicalManifestPayload struct {
	SchemaVersion           int           `json:"schema_version"`
	Channel                 Channel       `json:"channel"`
	Version                 string        `json:"version"`
	GitSHA                  string        `json:"git_sha"`
	PublishedAt             time.Time     `json:"published_at"`
	ExpiresAt               time.Time     `json:"expires_at"`
	ChannelEpoch            uint64        `json:"channel_epoch"`
	MinimumSupportedVersion string        `json:"minimum_supported_version"`
	RolloutPercentage       int           `json:"rollout_percentage"`
	Compatibility           Compatibility `json:"compatibility"`
	Artifacts               []Artifact    `json:"artifacts"`
}

// DecodeManifest reads, strictly decodes, and structurally validates one manifest.
func DecodeManifest(r io.Reader) (*Manifest, error) {
	if r == nil {
		return nil, ErrEmptyManifest
	}

	data, err := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read release manifest: %w", err)
	}
	if int64(len(data)) > MaxManifestBytes {
		return nil, ErrManifestTooLarge
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, ErrEmptyManifest
	}

	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := ensureJSONDocumentEnded(decoder); err != nil {
		return nil, err
	}
	if err := requireJSONFields(data); err != nil {
		return nil, err
	}
	if err := ValidateManifest(&manifest); err != nil {
		return nil, err
	}

	return &manifest, nil
}

// CanonicalManifestBytes validates manifest and returns its compact, deterministic
// JSON payload. Artifacts are ordered on a copy so the caller's manifest is unchanged.
func CanonicalManifestBytes(manifest *Manifest) ([]byte, error) {
	if err := ValidateManifest(manifest); err != nil {
		return nil, err
	}

	artifacts := append([]Artifact(nil), manifest.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool {
		left, right := artifacts[i], artifacts[j]
		if left.OS != right.OS {
			return left.OS < right.OS
		}
		if left.Architecture != right.Architecture {
			return left.Architecture < right.Architecture
		}
		return left.Type < right.Type
	})

	payload := canonicalManifestPayload{
		SchemaVersion:           manifest.SchemaVersion,
		Channel:                 manifest.Channel,
		Version:                 manifest.Version,
		GitSHA:                  manifest.GitSHA,
		PublishedAt:             manifest.PublishedAt.UTC(),
		ExpiresAt:               manifest.ExpiresAt.UTC(),
		ChannelEpoch:            manifest.ChannelEpoch,
		MinimumSupportedVersion: manifest.MinimumSupportedVersion,
		RolloutPercentage:       manifest.RolloutPercentage,
		Compatibility:           manifest.Compatibility,
		Artifacts:               artifacts,
	}

	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode canonical release manifest: %w", err)
	}
	return canonical, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := inspectJSONValue(decoder, "manifest"); err != nil {
		return err
	}
	return nil
}

func inspectJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}

	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("inspect %s field: %w", path, err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("inspect %s: object key is not a string", path)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("%s.%s is duplicated", path, key)
			}
			seen[key] = struct{}{}
			if err := inspectJSONValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("inspect %s closing object: %w", path, err)
		}
	case '[':
		index := 0
		for decoder.More() {
			if err := inspectJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			index++
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("inspect %s closing array: %w", path, err)
		}
	default:
		return fmt.Errorf("inspect %s: unexpected delimiter %q", path, delimiter)
	}

	return nil
}

func ensureJSONDocumentEnded(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing release manifest data: %w", err)
	}
	return errors.New("release manifest contains trailing JSON")
}

func requireJSONFields(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("inspect release manifest fields: %w", err)
	}
	if err := requireFields(root, "manifest",
		"schema_version", "channel", "version", "git_sha", "published_at", "expires_at",
		"channel_epoch", "minimum_supported_version", "rollout_percentage", "compatibility", "artifacts"); err != nil {
		return err
	}

	var compatibility map[string]json.RawMessage
	if err := json.Unmarshal(root["compatibility"], &compatibility); err != nil {
		return fmt.Errorf("inspect compatibility fields: %w", err)
	}
	if err := requireFields(compatibility, "compatibility",
		"ipc_min", "ipc_max", "service_min", "ui_min", "migration_epoch", "rollback_to", "reauth_on_rollback"); err != nil {
		return err
	}

	var artifacts []json.RawMessage
	if err := json.Unmarshal(root["artifacts"], &artifacts); err != nil {
		return fmt.Errorf("inspect artifact fields: %w", err)
	}
	for index, raw := range artifacts {
		var artifact map[string]json.RawMessage
		if err := json.Unmarshal(raw, &artifact); err != nil {
			return fmt.Errorf("inspect artifact %d fields: %w", index, err)
		}
		if err := requireFields(artifact, fmt.Sprintf("artifacts[%d]", index),
			"os", "arch", "type", "url", "size", "sha256", "native_signature", "provenance_url"); err != nil {
			return err
		}
	}
	return nil
}

func requireFields(object map[string]json.RawMessage, path string, fields ...string) error {
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
		raw, ok := object[field]
		if !ok {
			return fmt.Errorf("%s.%s is required", path, field)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s.%s must not be null", path, field)
		}
	}
	for field := range object {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("%s.%s is unknown", path, field)
		}
	}
	return nil
}

// ValidateManifest enforces the unambiguous structural invariants of schema version 1.
func ValidateManifest(manifest *Manifest) error {
	if manifest == nil {
		return errors.New("manifest is required")
	}
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("schema_version must be %d", ManifestSchemaVersion)
	}
	if !validChannel(manifest.Channel) {
		return fmt.Errorf("channel %q is invalid", manifest.Channel)
	}
	if err := validateVersion("version", manifest.Version); err != nil {
		return err
	}
	if !fullGitSHAPattern.MatchString(manifest.GitSHA) {
		return errors.New("git_sha must be a full 40-character lowercase hexadecimal SHA")
	}
	if err := validateUTCTimestamp("published_at", manifest.PublishedAt); err != nil {
		return err
	}
	if err := validateUTCTimestamp("expires_at", manifest.ExpiresAt); err != nil {
		return err
	}
	if !manifest.ExpiresAt.After(manifest.PublishedAt) {
		return errors.New("expires_at must be after published_at")
	}
	if manifest.ChannelEpoch == 0 {
		return errors.New("channel_epoch must be at least 1")
	}
	if err := validateVersion("minimum_supported_version", manifest.MinimumSupportedVersion); err != nil {
		return err
	}
	if manifest.RolloutPercentage < 0 || manifest.RolloutPercentage > 100 {
		return errors.New("rollout_percentage must be between 0 and 100")
	}
	if err := validateCompatibility(manifest.Compatibility); err != nil {
		return err
	}
	if len(manifest.Artifacts) == 0 {
		return errors.New("artifacts must contain at least one artifact")
	}

	type artifactTuple struct {
		channel      Channel
		version      string
		operatingSys OS
		architecture Architecture
		artifactType ArtifactType
	}
	seen := make(map[artifactTuple]struct{}, len(manifest.Artifacts))
	for index := range manifest.Artifacts {
		artifact := manifest.Artifacts[index]
		if err := validateArtifact(manifest.Version, manifest.GitSHA, artifact); err != nil {
			return fmt.Errorf("artifacts[%d]: %w", index, err)
		}
		tuple := artifactTuple{
			channel:      manifest.Channel,
			version:      manifest.Version,
			operatingSys: artifact.OS,
			architecture: artifact.Architecture,
			artifactType: artifact.Type,
		}
		if _, exists := seen[tuple]; exists {
			return fmt.Errorf("artifacts[%d]: duplicate channel/version/os/arch/type tuple", index)
		}
		seen[tuple] = struct{}{}
	}
	return nil
}

func validateCompatibility(compatibility Compatibility) error {
	if compatibility.IPCMin == 0 {
		return errors.New("compatibility.ipc_min must be at least 1")
	}
	if compatibility.IPCMax == 0 {
		return errors.New("compatibility.ipc_max must be at least 1")
	}
	if compatibility.IPCMin > compatibility.IPCMax {
		return errors.New("compatibility.ipc_min must not exceed ipc_max")
	}
	if err := validateVersion("compatibility.service_min", compatibility.ServiceMin); err != nil {
		return err
	}
	if err := validateVersion("compatibility.ui_min", compatibility.UIMin); err != nil {
		return err
	}
	if compatibility.MigrationEpoch == 0 {
		return errors.New("compatibility.migration_epoch must be at least 1")
	}
	if compatibility.RollbackTo == nil {
		return errors.New("compatibility.rollback_to is required")
	}
	for index, version := range compatibility.RollbackTo {
		if err := validateVersion(fmt.Sprintf("compatibility.rollback_to[%d]", index), version); err != nil {
			return err
		}
	}
	return nil
}

func validateArtifact(version, gitSHA string, artifact Artifact) error {
	if !validOS(artifact.OS) {
		return fmt.Errorf("os %q is invalid", artifact.OS)
	}
	if !validArchitecture(artifact.Architecture) {
		return fmt.Errorf("arch %q is invalid", artifact.Architecture)
	}
	if !validArtifactType(artifact.Type) {
		return fmt.Errorf("type %q is invalid", artifact.Type)
	}
	if err := validateImmutableReference("url", artifact.URL, version, gitSHA); err != nil {
		return err
	}
	if artifact.Size <= 0 {
		return errors.New("size must be greater than zero")
	}
	if !sha256Pattern.MatchString(artifact.SHA256) {
		return errors.New("sha256 must be 64 lowercase hexadecimal characters")
	}
	if !validNativeSignatureStatus(artifact.NativeSignature) {
		return fmt.Errorf("native_signature %q is invalid", artifact.NativeSignature)
	}
	if err := validateImmutableReference("provenance_url", artifact.ProvenanceURL, version, gitSHA); err != nil {
		return err
	}
	return nil
}

func validateVersion(field, version string) error {
	if !canonicalVersionPattern.MatchString(version) {
		return fmt.Errorf("%s must be a canonical x.y.z version", field)
	}
	return nil
}

func validateUTCTimestamp(field string, timestamp time.Time) error {
	if timestamp.IsZero() {
		return fmt.Errorf("%s is required", field)
	}
	_, offset := timestamp.Zone()
	if offset != 0 {
		return fmt.Errorf("%s must be UTC", field)
	}
	return nil
}

func validateImmutableReference(field, value, version, gitSHA string) error {
	invalid := func() error {
		return fmt.Errorf("%s must be an immutable canonical HTTPS URL under /clients/%s/%s/", field, version, gitSHA)
	}
	if value == "" || !strings.HasPrefix(value, "https://") || strings.TrimSpace(value) != value || strings.ToValidUTF8(value, "") != value || hasUnsafeURLText(value) || strings.Contains(value, `\`) {
		return invalid()
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil {
		return invalid()
	}
	if !isCanonicalAuthority(parsed) || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(value, "#") {
		return invalid()
	}

	requiredPrefix := "/clients/" + version + "/" + gitSHA + "/"
	escapedPath := parsed.EscapedPath()
	// Release object names are ASCII-safe, so any path percent-encoding is a
	// non-canonical alias rather than a necessary representation.
	if strings.Contains(escapedPath, "%") || !strings.HasPrefix(escapedPath, requiredPrefix) || len(escapedPath) == len(requiredPrefix) {
		return invalid()
	}
	if strings.HasSuffix(escapedPath, "/") || strings.Contains(escapedPath, "//") || hasDotSegment(escapedPath) || !hasOnlySafePathCharacters(escapedPath) {
		return invalid()
	}
	return nil
}

func hasUnsafeURLText(value string) bool {
	for _, character := range value {
		if character <= 0x1f || character == 0x7f {
			return true
		}
	}
	return false
}

func isCanonicalAuthority(parsed *url.URL) bool {
	hostname := parsed.Hostname()
	if parsed.Host != strings.ToLower(parsed.Host) || strings.HasSuffix(hostname, ".") || strings.HasSuffix(parsed.Host, ":") || looksLikeIPLiteralOrAlias(hostname) {
		return false
	}
	port := parsed.Port()
	if port == "443" || (len(port) > 1 && port[0] == '0') {
		return false
	}

	labels := strings.Split(hostname, ".")
	if len(labels) < 2 || len(hostname) > 253 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for index := range label {
			character := label[index]
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func looksLikeIPLiteralOrAlias(hostname string) bool {
	labels := strings.Split(hostname, ".")
	if len(labels) > 4 {
		return false
	}
	for _, label := range labels {
		digits := label
		base := byte(10)
		if strings.HasPrefix(label, "0x") {
			digits = label[2:]
			base = 16
		}
		if digits == "" {
			return false
		}
		for index := range digits {
			character := digits[index]
			if character >= '0' && character <= '9' {
				continue
			}
			if base == 16 && character >= 'a' && character <= 'f' {
				continue
			}
			return false
		}
	}
	return true
}

func hasOnlySafePathCharacters(path string) bool {
	for index := range path {
		character := path[index]
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && !strings.ContainsRune("/-._~", rune(character)) {
			return false
		}
	}
	return true
}

func hasDotSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func validChannel(channel Channel) bool {
	switch channel {
	case ChannelPilot, ChannelBeta, ChannelStable, ChannelGA:
		return true
	default:
		return false
	}
}

func validOS(os OS) bool {
	switch os {
	case OSLinux, OSMacOS, OSWindows:
		return true
	default:
		return false
	}
}

func validArchitecture(architecture Architecture) bool {
	switch architecture {
	case ArchitectureAMD64, ArchitectureARM64:
		return true
	default:
		return false
	}
}

func validArtifactType(artifactType ArtifactType) bool {
	switch artifactType {
	case ArtifactTypeDEB, ArtifactTypePKG, ArtifactTypeMSI, ArtifactTypePortable:
		return true
	default:
		return false
	}
}

func validNativeSignatureStatus(status NativeSignatureStatus) bool {
	switch status {
	case NativeSignatureRequiredAndVerified, NativeSignatureAdHoc:
		return true
	default:
		return false
	}
}

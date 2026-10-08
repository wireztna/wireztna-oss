package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// PlatformEvidenceSchemaVersion is the only platform evidence schema currently supported.
	PlatformEvidenceSchemaVersion = 1
	// MaxPlatformEvidenceBytes bounds untrusted platform evidence before JSON decoding.
	MaxPlatformEvidenceBytes int64 = 256 << 10
	// MaxEvidenceGates bounds work performed while validating gates.
	MaxEvidenceGates = 64
	// MaxEvidenceLimitations bounds work performed while validating limitations.
	MaxEvidenceLimitations = 32
)

var (
	evidenceIdentifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)
	artifactFilenamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

	// ErrEmptyPlatformEvidence indicates that no JSON document was supplied.
	ErrEmptyPlatformEvidence = errors.New("platform evidence is empty")
	// ErrPlatformEvidenceTooLarge indicates that evidence exceeded MaxPlatformEvidenceBytes.
	ErrPlatformEvidenceTooLarge = errors.New("platform evidence exceeds size limit")
)

// EvidenceClaimLevel identifies the strongest assertion made by an evidence envelope.
type EvidenceClaimLevel string

const (
	EvidenceClaimHostReadiness     EvidenceClaimLevel = "host-readiness"
	EvidenceClaimBuildOnly         EvidenceClaimLevel = "build-only"
	EvidenceClaimPlatformQualified EvidenceClaimLevel = "platform-qualified"
)

// EvidenceGateStatus records the result of a platform-defined gate.
type EvidenceGateStatus string

const (
	EvidenceGatePassed EvidenceGateStatus = "passed"
	EvidenceGateFailed EvidenceGateStatus = "failed"
	EvidenceGateNotRun EvidenceGateStatus = "not-run"
)

// EvidenceSignatureStatus records the native signature assertion made for an artifact.
type EvidenceSignatureStatus string

const (
	EvidenceSignatureNotAsserted         EvidenceSignatureStatus = "not-asserted"
	EvidenceSignatureRequiredAndVerified EvidenceSignatureStatus = "required-and-verified"
	EvidenceSignatureAdHoc               EvidenceSignatureStatus = "ad-hoc"
)

// EvidenceProvenanceStatus records whether artifact provenance was verified.
type EvidenceProvenanceStatus string

const (
	EvidenceProvenanceNotAsserted EvidenceProvenanceStatus = "not-asserted"
	EvidenceProvenanceVerified    EvidenceProvenanceStatus = "verified"
)

// PlatformEvidence is a bounded, versioned envelope supplied by a platform.
type PlatformEvidence struct {
	Identity    EvidenceIdentity   `json:"identity"`
	Source      EvidenceSource     `json:"source"`
	Tuple       EvidenceTuple      `json:"tuple"`
	Claim       EvidenceClaim      `json:"claim"`
	Gates       []EvidenceGate     `json:"gates"`
	Signature   EvidenceSignature  `json:"signature"`
	Provenance  EvidenceProvenance `json:"provenance"`
	Limitations []string           `json:"limitations"`
}

// EvidenceIdentity identifies the contract instance, not a supported release.
type EvidenceIdentity struct {
	Contract      string    `json:"contract"`
	SchemaVersion int       `json:"schema_version"`
	EvidenceID    string    `json:"evidence_id"`
	GeneratedAt   time.Time `json:"generated_at"`
}

// EvidenceSource identifies the producer revision and whether the record is test-only.
type EvidenceSource struct {
	Producer    string `json:"producer"`
	ProducerSHA string `json:"producer_git_sha"`
	TestFixture bool   `json:"test_fixture"`
}

// EvidenceTuple identifies the exact release target and, when claimed, its artifact.
type EvidenceTuple struct {
	Version      string            `json:"version"`
	GitSHA       string            `json:"git_sha"`
	OS           OS                `json:"os"`
	Architecture Architecture      `json:"arch"`
	Type         ArtifactType      `json:"type"`
	Artifact     *EvidenceArtifact `json:"artifact,omitempty"`
}

// EvidenceArtifact identifies artifact bytes without introducing a publication location.
type EvidenceArtifact struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

// EvidenceClaim declares the scope of the producer's assertions.
type EvidenceClaim struct {
	Level             EvidenceClaimLevel `json:"level"`
	Artifact          bool               `json:"artifact"`
	Runtime           bool               `json:"runtime"`
	PublicEligibility bool               `json:"public_eligibility"`
}

// EvidenceGate is a platform-namespaced qualification check.
type EvidenceGate struct {
	ID     string             `json:"id"`
	Status EvidenceGateStatus `json:"status"`
}

// EvidenceSignature binds a native signature assertion to exact artifact bytes.
type EvidenceSignature struct {
	Status         EvidenceSignatureStatus `json:"status"`
	ArtifactSHA256 string                  `json:"artifact_sha256,omitempty"`
	SignerIdentity string                  `json:"signer_identity,omitempty"`
}

// EvidenceProvenance binds verified build provenance to artifact and source revisions.
type EvidenceProvenance struct {
	Status         EvidenceProvenanceStatus `json:"status"`
	ArtifactSHA256 string                   `json:"artifact_sha256,omitempty"`
	SourceGitSHA   string                   `json:"source_git_sha,omitempty"`
	BuilderID      string                   `json:"builder_id,omitempty"`
}

// EvidenceValidationError identifies a semantic contract violation by field path.
type EvidenceValidationError struct {
	Field   string
	Problem string
}

func (e *EvidenceValidationError) Error() string {
	return e.Field + ": " + e.Problem
}

// EvidenceIneligibilityReason is a stable reason why valid evidence cannot authorize promotion.
type EvidenceIneligibilityReason string

const (
	EvidenceIneligibleClaimLevel        EvidenceIneligibilityReason = "claim-not-platform-qualified"
	EvidenceIneligiblePublicEligibility EvidenceIneligibilityReason = "public-eligibility-not-asserted"
	EvidenceIneligibleTestFixture       EvidenceIneligibilityReason = "test-fixture"
)

// EvidenceEligibilityResult is policy output derived separately from decoding.
type EvidenceEligibilityResult struct {
	PromotionEligible  bool
	EvaluatedCandidate PromotionCandidate
	EvidenceID         string
	EvidenceDigest     string
	ProfileID          string
	Reasons            []EvidenceIneligibilityReason
}

// DecodePlatformEvidence reads, strictly decodes, and structurally validates one envelope.
// It does not decide promotion eligibility.
func DecodePlatformEvidence(r io.Reader) (*PlatformEvidence, error) {
	if r == nil {
		return nil, ErrEmptyPlatformEvidence
	}

	data, err := io.ReadAll(io.LimitReader(r, MaxPlatformEvidenceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read platform evidence: %w", err)
	}
	if int64(len(data)) > MaxPlatformEvidenceBytes {
		return nil, ErrPlatformEvidenceTooLarge
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, ErrEmptyPlatformEvidence
	}
	if !utf8.Valid(data) {
		return nil, errors.New("platform evidence must be valid UTF-8")
	}

	if err := rejectDuplicateEvidenceJSONKeys(data); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var evidence PlatformEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return nil, fmt.Errorf("decode platform evidence: %w", err)
	}
	if err := ensureEvidenceJSONDocumentEnded(decoder); err != nil {
		return nil, err
	}
	if err := requireEvidenceJSONFields(data); err != nil {
		return nil, err
	}
	if err := ValidatePlatformEvidence(&evidence); err != nil {
		return nil, err
	}
	return &evidence, nil
}

// ValidatePlatformEvidence enforces schema and cross-field claim invariants.
func ValidatePlatformEvidence(evidence *PlatformEvidence) error {
	if evidence == nil {
		return evidenceError("evidence", "is required")
	}
	if evidence.Identity.Contract != "wireztna-platform-evidence" {
		return evidenceError("identity.contract", `must be "wireztna-platform-evidence"`)
	}
	if evidence.Identity.SchemaVersion != PlatformEvidenceSchemaVersion {
		return evidenceError("identity.schema_version", fmt.Sprintf("must be %d", PlatformEvidenceSchemaVersion))
	}
	if !evidenceIdentifierPattern.MatchString(evidence.Identity.EvidenceID) {
		return evidenceError("identity.evidence_id", "must be a lowercase identifier of at most 128 characters")
	}
	if err := validateUTCTimestamp("identity.generated_at", evidence.Identity.GeneratedAt); err != nil {
		return evidenceError("identity.generated_at", strings.TrimPrefix(err.Error(), "identity.generated_at "))
	}
	if !evidenceIdentifierPattern.MatchString(evidence.Source.Producer) {
		return evidenceError("source.producer", "must be a lowercase identifier of at most 128 characters")
	}
	if !fullGitSHAPattern.MatchString(evidence.Source.ProducerSHA) {
		return evidenceError("source.producer_git_sha", "must be a full 40-character lowercase hexadecimal SHA")
	}
	if err := validateVersion("tuple.version", evidence.Tuple.Version); err != nil {
		return evidenceError("tuple.version", "must be a canonical x.y.z version")
	}
	if !fullGitSHAPattern.MatchString(evidence.Tuple.GitSHA) {
		return evidenceError("tuple.git_sha", "must be a full 40-character lowercase hexadecimal SHA")
	}
	if !validOS(evidence.Tuple.OS) {
		return evidenceError("tuple.os", fmt.Sprintf("%q is invalid", evidence.Tuple.OS))
	}
	if !validArchitecture(evidence.Tuple.Architecture) {
		return evidenceError("tuple.arch", fmt.Sprintf("%q is invalid", evidence.Tuple.Architecture))
	}
	if !validArtifactType(evidence.Tuple.Type) {
		return evidenceError("tuple.type", fmt.Sprintf("%q is invalid", evidence.Tuple.Type))
	}
	if err := validateEvidenceClaim(evidence); err != nil {
		return err
	}
	if err := validateEvidenceArtifact(evidence.Tuple.Artifact); err != nil {
		return err
	}
	if err := validateEvidenceGates(evidence); err != nil {
		return err
	}
	if err := validateEvidenceSignature(evidence); err != nil {
		return err
	}
	if err := validateEvidenceProvenance(evidence); err != nil {
		return err
	}
	if err := validateEvidenceLimitations(evidence.Limitations); err != nil {
		return err
	}
	return nil
}

func validateEvidenceClaim(evidence *PlatformEvidence) error {
	claim := evidence.Claim
	switch claim.Level {
	case EvidenceClaimHostReadiness:
		if claim.Artifact || evidence.Tuple.Artifact != nil {
			return evidenceError("claim.artifact", "host-readiness must not assert an artifact")
		}
		if claim.Runtime {
			return evidenceError("claim.runtime", "host-readiness must not assert runtime qualification")
		}
		if claim.PublicEligibility {
			return evidenceError("claim.public_eligibility", "host-readiness must not assert public eligibility")
		}
	case EvidenceClaimBuildOnly:
		if !claim.Artifact || evidence.Tuple.Artifact == nil {
			return evidenceError("claim.artifact", "build-only requires an artifact")
		}
		if claim.Runtime {
			return evidenceError("claim.runtime", "build-only must not assert runtime qualification")
		}
		if claim.PublicEligibility {
			return evidenceError("claim.public_eligibility", "build-only must not assert public eligibility")
		}
	case EvidenceClaimPlatformQualified:
		if !claim.Artifact || evidence.Tuple.Artifact == nil {
			return evidenceError("claim.artifact", "platform-qualified requires an artifact")
		}
		if !claim.Runtime {
			return evidenceError("claim.runtime", "platform-qualified requires runtime qualification")
		}
	default:
		return evidenceError("claim.level", fmt.Sprintf("%q is invalid", claim.Level))
	}

	if claim.Artifact != (evidence.Tuple.Artifact != nil) {
		return evidenceError("claim.artifact", "must agree with tuple.artifact presence")
	}
	return nil
}

func validateEvidenceArtifact(artifact *EvidenceArtifact) error {
	if artifact == nil {
		return nil
	}
	if artifact.Filename == "." || artifact.Filename == ".." || !artifactFilenamePattern.MatchString(artifact.Filename) {
		return evidenceError("tuple.artifact.filename", "must be a safe basename of at most 255 characters")
	}
	if artifact.Size <= 0 {
		return evidenceError("tuple.artifact.size", "must be greater than zero")
	}
	if !sha256Pattern.MatchString(artifact.SHA256) {
		return evidenceError("tuple.artifact.sha256", "must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func validateEvidenceGates(evidence *PlatformEvidence) error {
	if evidence.Gates == nil {
		return evidenceError("gates", "is required")
	}
	if len(evidence.Gates) > MaxEvidenceGates {
		return evidenceError("gates", fmt.Sprintf("must contain at most %d entries", MaxEvidenceGates))
	}
	if evidence.Claim.Level == EvidenceClaimPlatformQualified && len(evidence.Gates) == 0 {
		return evidenceError("gates", "platform-qualified requires at least one gate")
	}

	seen := make(map[string]struct{}, len(evidence.Gates))
	for index, gate := range evidence.Gates {
		path := fmt.Sprintf("gates[%d]", index)
		if !evidenceIdentifierPattern.MatchString(gate.ID) {
			return evidenceError(path+".id", "must be a lowercase platform-namespaced identifier of at most 128 characters")
		}
		if _, exists := seen[gate.ID]; exists {
			return evidenceError(path+".id", "is duplicated")
		}
		seen[gate.ID] = struct{}{}
		switch gate.Status {
		case EvidenceGatePassed, EvidenceGateFailed, EvidenceGateNotRun:
		default:
			return evidenceError(path+".status", fmt.Sprintf("%q is invalid", gate.Status))
		}
		if evidence.Claim.Level == EvidenceClaimPlatformQualified && gate.Status != EvidenceGatePassed {
			return evidenceError(path+".status", "platform-qualified requires every gate to pass")
		}
	}
	return nil
}

func validateEvidenceSignature(evidence *PlatformEvidence) error {
	signature := evidence.Signature
	artifact := evidence.Tuple.Artifact
	switch signature.Status {
	case EvidenceSignatureNotAsserted:
		if signature.ArtifactSHA256 != "" || signature.SignerIdentity != "" {
			return evidenceError("signature", "not-asserted must not contain artifact_sha256 or signer_identity")
		}
	case EvidenceSignatureRequiredAndVerified, EvidenceSignatureAdHoc:
		if artifact == nil {
			return evidenceError("signature.status", "cannot assert a signature without an artifact")
		}
		if signature.ArtifactSHA256 != artifact.SHA256 {
			return evidenceError("signature.artifact_sha256", "must equal tuple.artifact.sha256")
		}
		if !evidenceIdentifierPattern.MatchString(signature.SignerIdentity) {
			return evidenceError("signature.signer_identity", "must be a lowercase identifier of at most 128 characters")
		}
	default:
		return evidenceError("signature.status", fmt.Sprintf("%q is invalid", signature.Status))
	}
	if evidence.Claim.Level == EvidenceClaimPlatformQualified && signature.Status == EvidenceSignatureNotAsserted {
		return evidenceError("signature.status", "platform-qualified requires a coherent signature assertion")
	}
	if evidence.Claim.PublicEligibility && signature.Status != EvidenceSignatureRequiredAndVerified {
		return evidenceError("signature.status", "public eligibility requires required-and-verified")
	}
	return nil
}

func validateEvidenceProvenance(evidence *PlatformEvidence) error {
	provenance := evidence.Provenance
	artifact := evidence.Tuple.Artifact
	switch provenance.Status {
	case EvidenceProvenanceNotAsserted:
		if provenance.ArtifactSHA256 != "" || provenance.SourceGitSHA != "" || provenance.BuilderID != "" {
			return evidenceError("provenance", "not-asserted must not contain binding fields")
		}
	case EvidenceProvenanceVerified:
		if artifact == nil {
			return evidenceError("provenance.status", "cannot assert provenance without an artifact")
		}
		if provenance.ArtifactSHA256 != artifact.SHA256 {
			return evidenceError("provenance.artifact_sha256", "must equal tuple.artifact.sha256")
		}
		if !fullGitSHAPattern.MatchString(provenance.SourceGitSHA) {
			return evidenceError("provenance.source_git_sha", "must be a full 40-character lowercase hexadecimal SHA")
		}
		if provenance.SourceGitSHA != evidence.Tuple.GitSHA {
			return evidenceError("provenance.source_git_sha", "must equal tuple.git_sha")
		}
		if !evidenceIdentifierPattern.MatchString(provenance.BuilderID) {
			return evidenceError("provenance.builder_id", "must be a lowercase identifier of at most 128 characters")
		}
	default:
		return evidenceError("provenance.status", fmt.Sprintf("%q is invalid", provenance.Status))
	}
	if evidence.Claim.Level == EvidenceClaimPlatformQualified && provenance.Status != EvidenceProvenanceVerified {
		return evidenceError("provenance.status", "platform-qualified requires verified coherent provenance")
	}
	return nil
}

func validateEvidenceLimitations(limitations []string) error {
	if limitations == nil {
		return evidenceError("limitations", "is required")
	}
	if len(limitations) > MaxEvidenceLimitations {
		return evidenceError("limitations", fmt.Sprintf("must contain at most %d entries", MaxEvidenceLimitations))
	}
	seen := make(map[string]struct{}, len(limitations))
	for index, limitation := range limitations {
		path := fmt.Sprintf("limitations[%d]", index)
		if limitation == "" || strings.TrimSpace(limitation) != limitation {
			return evidenceError(path, "must be a non-empty trimmed value")
		}
		if len(limitation) > 256 || strings.ContainsAny(limitation, "\r\n\x00") {
			return evidenceError(path, "must be at most 256 bytes without line breaks or NUL")
		}
		if _, exists := seen[limitation]; exists {
			return evidenceError(path, "is duplicated")
		}
		seen[limitation] = struct{}{}
	}
	return nil
}

func rejectDuplicateEvidenceJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	return inspectJSONValue(decoder, "evidence")
}

func ensureEvidenceJSONDocumentEnded(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing platform evidence data: %w", err)
	}
	return errors.New("platform evidence contains trailing JSON")
}

func requireEvidenceJSONFields(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("inspect platform evidence fields: %w", err)
	}
	if err := requireEvidenceFields(root, "evidence",
		[]string{"identity", "source", "tuple", "claim", "gates", "signature", "provenance", "limitations"}, nil); err != nil {
		return err
	}

	checks := []struct {
		field    string
		required []string
		optional []string
	}{
		{field: "identity", required: []string{"contract", "schema_version", "evidence_id", "generated_at"}},
		{field: "source", required: []string{"producer", "producer_git_sha", "test_fixture"}},
		{field: "tuple", required: []string{"version", "git_sha", "os", "arch", "type"}, optional: []string{"artifact"}},
		{field: "claim", required: []string{"level", "artifact", "runtime", "public_eligibility"}},
		{field: "signature", required: []string{"status"}, optional: []string{"artifact_sha256", "signer_identity"}},
		{field: "provenance", required: []string{"status"}, optional: []string{"artifact_sha256", "source_git_sha", "builder_id"}},
	}
	for _, check := range checks {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(root[check.field], &object); err != nil {
			return fmt.Errorf("inspect evidence.%s fields: %w", check.field, err)
		}
		if err := requireEvidenceFields(object, "evidence."+check.field, check.required, check.optional); err != nil {
			return err
		}
	}

	var tuple map[string]json.RawMessage
	if err := json.Unmarshal(root["tuple"], &tuple); err != nil {
		return fmt.Errorf("inspect evidence.tuple fields: %w", err)
	}
	if rawArtifact, exists := tuple["artifact"]; exists {
		var artifact map[string]json.RawMessage
		if err := json.Unmarshal(rawArtifact, &artifact); err != nil {
			return fmt.Errorf("inspect evidence.tuple.artifact fields: %w", err)
		}
		if err := requireEvidenceFields(artifact, "evidence.tuple.artifact", []string{"filename", "size", "sha256"}, nil); err != nil {
			return err
		}
	}

	var gates []json.RawMessage
	if err := json.Unmarshal(root["gates"], &gates); err != nil {
		return fmt.Errorf("inspect evidence.gates fields: %w", err)
	}
	for index, rawGate := range gates {
		var gate map[string]json.RawMessage
		if err := json.Unmarshal(rawGate, &gate); err != nil {
			return fmt.Errorf("inspect evidence.gates[%d] fields: %w", index, err)
		}
		if err := requireEvidenceFields(gate, fmt.Sprintf("evidence.gates[%d]", index), []string{"id", "status"}, nil); err != nil {
			return err
		}
	}
	return nil
}

func requireEvidenceFields(object map[string]json.RawMessage, path string, required, optional []string) error {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, field := range required {
		allowed[field] = struct{}{}
		raw, exists := object[field]
		if !exists {
			return fmt.Errorf("%s.%s is required", path, field)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s.%s must not be null", path, field)
		}
	}
	for _, field := range optional {
		allowed[field] = struct{}{}
		if raw, exists := object[field]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s.%s must not be null", path, field)
		}
	}
	for field := range object {
		if _, exists := allowed[field]; !exists {
			return fmt.Errorf("%s.%s is unknown", path, field)
		}
	}
	return nil
}

func evidenceError(field, problem string) error {
	return &EvidenceValidationError{Field: field, Problem: problem}
}

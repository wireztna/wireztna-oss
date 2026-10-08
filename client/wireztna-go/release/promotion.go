package release

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// PromotionCandidate identifies the exact artifact the caller intends to promote.
type PromotionCandidate struct {
	Version      string
	GitSHA       string
	OS           OS
	Architecture Architecture
	Type         ArtifactType
	Filename     string
	Size         int64
	SHA256       string
}

// AuthenticatedPlatformEvidence is a receipt issued by an authentication boundary
// outside the evidence envelope. This package validates its bindings and policy;
// creation of receipts and real trust roots deliberately remain out of scope.
type AuthenticatedPlatformEvidence struct {
	Boundary       string
	EvidenceID     string
	EvidenceSHA256 string
	Producer       string
	SignerIdentity string
	BuilderID      string
}

// PlatformProfileTuple is one OS, architecture, and artifact-type row explicitly
// allowed by a promotion profile.
type PlatformProfileTuple struct {
	OS           OS
	Architecture Architecture
	Type         ArtifactType
}

// PlatformPromotionProfile defines an allowed matrix and its gate vocabulary.
// RequiredGateIDs is the minimum set. AllowedGateIDs bounds all accepted gates;
// making both sets equal requires an exact gate set.
type PlatformPromotionProfile struct {
	ID              string
	Matrix          []PlatformProfileTuple
	RequiredGateIDs []string
	AllowedGateIDs  []string
}

// PlatformPromotionPolicy supplies all trust and matrix decisions from outside
// the untrusted evidence document.
type PlatformPromotionPolicy struct {
	TrustedBoundaries []string
	TrustedProducers  []string
	TrustedSigners    []string
	TrustedBuilders   []string
	Profiles          []PlatformPromotionProfile
}

// PlatformPromotionEvaluation is the explicit context required for promotion.
type PlatformPromotionEvaluation struct {
	Candidate      PromotionCandidate
	Authentication *AuthenticatedPlatformEvidence
	Policy         PlatformPromotionPolicy
	ProfileID      string
}

const (
	EvidenceIneligibleAuthenticationMissing     EvidenceIneligibilityReason = "authentication-missing"
	EvidenceIneligibleAuthenticationBoundary    EvidenceIneligibilityReason = "authentication-boundary-untrusted"
	EvidenceIneligibleAuthenticationEvidenceID  EvidenceIneligibilityReason = "authentication-evidence-id-mismatch"
	EvidenceIneligibleAuthenticationDigest      EvidenceIneligibilityReason = "authentication-digest-mismatch"
	EvidenceIneligibleAuthenticationProducer    EvidenceIneligibilityReason = "authentication-producer-mismatch"
	EvidenceIneligibleAuthenticationSigner      EvidenceIneligibilityReason = "authentication-signer-mismatch"
	EvidenceIneligibleAuthenticationBuilder     EvidenceIneligibilityReason = "authentication-builder-mismatch"
	EvidenceIneligibleProducerUntrusted         EvidenceIneligibilityReason = "producer-untrusted"
	EvidenceIneligibleSignerUntrusted           EvidenceIneligibilityReason = "signer-untrusted"
	EvidenceIneligibleBuilderUntrusted          EvidenceIneligibilityReason = "builder-untrusted"
	EvidenceIneligibleCandidateVersionMismatch  EvidenceIneligibilityReason = "candidate-version-mismatch"
	EvidenceIneligibleCandidateGitSHAMismatch   EvidenceIneligibilityReason = "candidate-git-sha-mismatch"
	EvidenceIneligibleCandidateOSMismatch       EvidenceIneligibilityReason = "candidate-os-mismatch"
	EvidenceIneligibleCandidateArchMismatch     EvidenceIneligibilityReason = "candidate-arch-mismatch"
	EvidenceIneligibleCandidateTypeMismatch     EvidenceIneligibilityReason = "candidate-type-mismatch"
	EvidenceIneligibleCandidateFilenameMismatch EvidenceIneligibilityReason = "candidate-filename-mismatch"
	EvidenceIneligibleCandidateSizeMismatch     EvidenceIneligibilityReason = "candidate-size-mismatch"
	EvidenceIneligibleCandidateDigestMismatch   EvidenceIneligibilityReason = "candidate-digest-mismatch"
	EvidenceIneligibleProfileNotFound           EvidenceIneligibilityReason = "profile-not-found"
	EvidenceIneligibleProfileTupleNotAllowed    EvidenceIneligibilityReason = "profile-tuple-not-allowed"
	EvidenceIneligibleRequiredGateMissing       EvidenceIneligibilityReason = "required-gate-missing"
	EvidenceIneligibleUnexpectedGate            EvidenceIneligibilityReason = "unexpected-gate"
)

// PlatformEvidenceDigest returns the canonical digest that an external
// authentication boundary must bind in its receipt.
func PlatformEvidenceDigest(evidence *PlatformEvidence) (string, error) {
	if err := ValidatePlatformEvidence(evidence); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(evidence)
	if err != nil {
		return "", fmt.Errorf("marshal canonical platform evidence: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", digest), nil
}

// EvaluatePlatformEvidence evaluates untrusted declarations against required
// external authentication, an exact promotion candidate, and explicit policy.
// It cannot authorize promotion from the JSON envelope alone.
func EvaluatePlatformEvidence(evidence *PlatformEvidence, evaluation PlatformPromotionEvaluation) (EvidenceEligibilityResult, error) {
	digest, err := PlatformEvidenceDigest(evidence)
	if err != nil {
		return EvidenceEligibilityResult{}, err
	}
	if err := validatePromotionCandidate(evaluation.Candidate); err != nil {
		return EvidenceEligibilityResult{}, err
	}

	result := EvidenceEligibilityResult{
		EvaluatedCandidate: evaluation.Candidate,
		EvidenceID:         evidence.Identity.EvidenceID,
		EvidenceDigest:     digest,
		ProfileID:          evaluation.ProfileID,
		Reasons:            make([]EvidenceIneligibilityReason, 0, 16),
	}

	if evidence.Claim.Level != EvidenceClaimPlatformQualified {
		result.Reasons = append(result.Reasons, EvidenceIneligibleClaimLevel)
	}
	if !evidence.Claim.PublicEligibility {
		result.Reasons = append(result.Reasons, EvidenceIneligiblePublicEligibility)
	}
	if evidence.Source.TestFixture {
		result.Reasons = append(result.Reasons, EvidenceIneligibleTestFixture)
	}

	appendCandidateMismatchReasons(&result, evidence, evaluation.Candidate)
	appendAuthenticationReasons(&result, evidence, digest, evaluation.Authentication, evaluation.Policy)

	profile, profileErr := resolvePromotionProfile(evaluation.Policy.Profiles, evaluation.ProfileID)
	if profileErr != nil {
		return EvidenceEligibilityResult{}, profileErr
	}
	if profile == nil {
		result.Reasons = append(result.Reasons, EvidenceIneligibleProfileNotFound)
	} else {
		if err := validatePromotionProfile(*profile); err != nil {
			return EvidenceEligibilityResult{}, err
		}
		appendProfileReasons(&result, evidence, evaluation.Candidate, *profile)
	}

	result.PromotionEligible = len(result.Reasons) == 0
	return result, nil
}

func validatePromotionCandidate(candidate PromotionCandidate) error {
	if err := validateVersion("candidate.version", candidate.Version); err != nil {
		return evidenceError("candidate.version", "must be a canonical x.y.z version")
	}
	if !fullGitSHAPattern.MatchString(candidate.GitSHA) {
		return evidenceError("candidate.git_sha", "must be a full 40-character lowercase hexadecimal SHA")
	}
	if !validOS(candidate.OS) {
		return evidenceError("candidate.os", fmt.Sprintf("%q is invalid", candidate.OS))
	}
	if !validArchitecture(candidate.Architecture) {
		return evidenceError("candidate.arch", fmt.Sprintf("%q is invalid", candidate.Architecture))
	}
	if !validArtifactType(candidate.Type) {
		return evidenceError("candidate.type", fmt.Sprintf("%q is invalid", candidate.Type))
	}
	return validateEvidenceArtifact(&EvidenceArtifact{
		Filename: candidate.Filename,
		Size:     candidate.Size,
		SHA256:   candidate.SHA256,
	})
}

func appendCandidateMismatchReasons(result *EvidenceEligibilityResult, evidence *PlatformEvidence, candidate PromotionCandidate) {
	artifact := evidence.Tuple.Artifact
	if candidate.Version != evidence.Tuple.Version {
		result.Reasons = append(result.Reasons, EvidenceIneligibleCandidateVersionMismatch)
	}
	if candidate.GitSHA != evidence.Tuple.GitSHA {
		result.Reasons = append(result.Reasons, EvidenceIneligibleCandidateGitSHAMismatch)
	}
	if candidate.OS != evidence.Tuple.OS {
		result.Reasons = append(result.Reasons, EvidenceIneligibleCandidateOSMismatch)
	}
	if candidate.Architecture != evidence.Tuple.Architecture {
		result.Reasons = append(result.Reasons, EvidenceIneligibleCandidateArchMismatch)
	}
	if candidate.Type != evidence.Tuple.Type {
		result.Reasons = append(result.Reasons, EvidenceIneligibleCandidateTypeMismatch)
	}
	if artifact == nil || candidate.Filename != artifact.Filename {
		result.Reasons = append(result.Reasons, EvidenceIneligibleCandidateFilenameMismatch)
	}
	if artifact == nil || candidate.Size != artifact.Size {
		result.Reasons = append(result.Reasons, EvidenceIneligibleCandidateSizeMismatch)
	}
	if artifact == nil || candidate.SHA256 != artifact.SHA256 {
		result.Reasons = append(result.Reasons, EvidenceIneligibleCandidateDigestMismatch)
	}
}

func appendAuthenticationReasons(result *EvidenceEligibilityResult, evidence *PlatformEvidence, digest string, authentication *AuthenticatedPlatformEvidence, policy PlatformPromotionPolicy) {
	if authentication == nil {
		result.Reasons = append(result.Reasons, EvidenceIneligibleAuthenticationMissing)
	} else {
		if !containsString(policy.TrustedBoundaries, authentication.Boundary) {
			result.Reasons = append(result.Reasons, EvidenceIneligibleAuthenticationBoundary)
		}
		if authentication.EvidenceID != evidence.Identity.EvidenceID {
			result.Reasons = append(result.Reasons, EvidenceIneligibleAuthenticationEvidenceID)
		}
		if authentication.EvidenceSHA256 != digest {
			result.Reasons = append(result.Reasons, EvidenceIneligibleAuthenticationDigest)
		}
		if authentication.Producer != evidence.Source.Producer {
			result.Reasons = append(result.Reasons, EvidenceIneligibleAuthenticationProducer)
		}
		if authentication.SignerIdentity != evidence.Signature.SignerIdentity {
			result.Reasons = append(result.Reasons, EvidenceIneligibleAuthenticationSigner)
		}
		if authentication.BuilderID != evidence.Provenance.BuilderID {
			result.Reasons = append(result.Reasons, EvidenceIneligibleAuthenticationBuilder)
		}
	}
	if !containsString(policy.TrustedProducers, evidence.Source.Producer) {
		result.Reasons = append(result.Reasons, EvidenceIneligibleProducerUntrusted)
	}
	if !containsString(policy.TrustedSigners, evidence.Signature.SignerIdentity) {
		result.Reasons = append(result.Reasons, EvidenceIneligibleSignerUntrusted)
	}
	if !containsString(policy.TrustedBuilders, evidence.Provenance.BuilderID) {
		result.Reasons = append(result.Reasons, EvidenceIneligibleBuilderUntrusted)
	}
}

func resolvePromotionProfile(profiles []PlatformPromotionProfile, profileID string) (*PlatformPromotionProfile, error) {
	var match *PlatformPromotionProfile
	for index := range profiles {
		if profiles[index].ID != profileID {
			continue
		}
		if match != nil {
			return nil, evidenceError("policy.profiles", fmt.Sprintf("profile %q is duplicated", profileID))
		}
		match = &profiles[index]
	}
	return match, nil
}

func validatePromotionProfile(profile PlatformPromotionProfile) error {
	if !evidenceIdentifierPattern.MatchString(profile.ID) {
		return evidenceError("policy.profile.id", "must be a lowercase identifier of at most 128 characters")
	}
	if len(profile.Matrix) == 0 {
		return evidenceError("policy.profile.matrix", "must declare at least one tuple")
	}
	seenTuples := make(map[PlatformProfileTuple]struct{}, len(profile.Matrix))
	for index, row := range profile.Matrix {
		path := fmt.Sprintf("policy.profile.matrix[%d]", index)
		if !validOS(row.OS) || !validArchitecture(row.Architecture) || !validArtifactType(row.Type) {
			return evidenceError(path, "contains an invalid tuple value")
		}
		if _, duplicate := seenTuples[row]; duplicate {
			return evidenceError(path, "is duplicated")
		}
		seenTuples[row] = struct{}{}
	}
	if len(profile.RequiredGateIDs) == 0 {
		return evidenceError("policy.profile.required_gates", "must declare at least one required gate")
	}
	if len(profile.AllowedGateIDs) == 0 {
		return evidenceError("policy.profile.allowed_gates", "must explicitly bound allowed gates")
	}
	required, err := validatePolicyGateIDs("policy.profile.required_gates", profile.RequiredGateIDs)
	if err != nil {
		return err
	}
	allowed, err := validatePolicyGateIDs("policy.profile.allowed_gates", profile.AllowedGateIDs)
	if err != nil {
		return err
	}
	for gateID := range required {
		if _, exists := allowed[gateID]; !exists {
			return evidenceError("policy.profile.required_gates", fmt.Sprintf("gate %q is not allowed", gateID))
		}
	}
	return nil
}

func validatePolicyGateIDs(path string, gateIDs []string) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(gateIDs))
	for index, gateID := range gateIDs {
		if !evidenceIdentifierPattern.MatchString(gateID) {
			return nil, evidenceError(fmt.Sprintf("%s[%d]", path, index), "must be a lowercase platform-namespaced identifier of at most 128 characters")
		}
		if _, duplicate := seen[gateID]; duplicate {
			return nil, evidenceError(fmt.Sprintf("%s[%d]", path, index), "is duplicated")
		}
		seen[gateID] = struct{}{}
	}
	return seen, nil
}

func appendProfileReasons(result *EvidenceEligibilityResult, evidence *PlatformEvidence, candidate PromotionCandidate, profile PlatformPromotionProfile) {
	candidateTuple := PlatformProfileTuple{OS: candidate.OS, Architecture: candidate.Architecture, Type: candidate.Type}
	if !coherentPlatformTuple(candidateTuple) || !containsProfileTuple(profile.Matrix, candidateTuple) {
		result.Reasons = append(result.Reasons, EvidenceIneligibleProfileTupleNotAllowed)
	}

	observed := make(map[string]struct{}, len(evidence.Gates))
	for _, gate := range evidence.Gates {
		observed[gate.ID] = struct{}{}
	}
	for _, required := range profile.RequiredGateIDs {
		if _, exists := observed[required]; !exists {
			result.Reasons = append(result.Reasons, EvidenceIneligibleRequiredGateMissing)
			break
		}
	}
	allowed := make(map[string]struct{}, len(profile.AllowedGateIDs))
	for _, gateID := range profile.AllowedGateIDs {
		allowed[gateID] = struct{}{}
	}
	for gateID := range observed {
		if _, exists := allowed[gateID]; !exists {
			result.Reasons = append(result.Reasons, EvidenceIneligibleUnexpectedGate)
			break
		}
	}
}

func coherentPlatformTuple(tuple PlatformProfileTuple) bool {
	switch tuple.OS {
	case OSMacOS:
		return tuple.Architecture == ArchitectureARM64 && tuple.Type == ArtifactTypePKG
	case OSLinux:
		return tuple.Type == ArtifactTypeDEB || tuple.Type == ArtifactTypePortable
	case OSWindows:
		return tuple.Type == ArtifactTypeMSI || tuple.Type == ArtifactTypePortable
	default:
		return false
	}
}

func containsProfileTuple(matrix []PlatformProfileTuple, tuple PlatformProfileTuple) bool {
	for _, allowed := range matrix {
		if allowed == tuple {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

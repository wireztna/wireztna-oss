package release

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

const (
	// MaxBuildOnlyArtifactBytes bounds artifact hashing by the pure producer.
	MaxBuildOnlyArtifactBytes = 1 << 30
)

// BuildOnlyEvidenceProfile fixes one producer, tuple, filename convention, gate,
// and limitation set. It is evidence-production configuration, not promotion
// policy, and carries no trust roots or publication authorization.
type BuildOnlyEvidenceProfile struct {
	ID             string
	Producer       string
	Tuple          PlatformProfileTuple
	FilenameSuffix string
	GateID         string
	FailedGateIDs  []string
	Limitations    []string
}

// BuildOnlyEvidenceInput supplies every non-profile value explicitly. The
// producer reads no clock, filesystem, environment, network, or trust store.
type BuildOnlyEvidenceInput struct {
	Candidate      PromotionCandidate
	ProducerGitSHA string
	GeneratedAt    time.Time
	ArtifactBytes  []byte
}

// LinuxAMD64DEBBuildOnlyProfile returns the explicit Linux DEB producer profile.
func LinuxAMD64DEBBuildOnlyProfile() BuildOnlyEvidenceProfile {
	return BuildOnlyEvidenceProfile{
		ID:             "linux.deb.build-only",
		Producer:       "linux-deb-build",
		Tuple:          PlatformProfileTuple{OS: OSLinux, Architecture: ArchitectureAMD64, Type: ArtifactTypeDEB},
		FilenameSuffix: "-linux-amd64.deb",
		GateID:         "linux.deb.artifact-built",
		Limitations: []string{
			"Build-only evidence; package installation, upgrade, and removal were not executed.",
			"Runtime behavior and host integration were not qualified.",
			"Repository signing and package-manager trust were not asserted.",
			"This evidence does not authorize promotion or publication.",
		},
	}
}

// WindowsAMD64MSIBuildOnlyProfile returns the generic Windows MSI build profile.
// MSI source-contract inspection is deliberately separate; this byte producer
// neither executes nor substitutes for the inspector.
func WindowsAMD64MSIBuildOnlyProfile() BuildOnlyEvidenceProfile {
	return BuildOnlyEvidenceProfile{
		ID:             "windows.msi-artifact.build-only",
		Producer:       "windows-msi-build",
		Tuple:          PlatformProfileTuple{OS: OSWindows, Architecture: ArchitectureAMD64, Type: ArtifactTypeMSI},
		FilenameSuffix: "-windows-amd64.msi",
		GateID:         "windows.msi.artifact-built",
		FailedGateIDs:  []string{"windows.ipc-v2.peer-auth"},
		Limitations: []string{
			"Build-only evidence; install, upgrade, repair, and uninstall were not executed.",
			"SCM behavior, service runtime, PATH propagation, shortcuts, and autostart were not runtime-qualified.",
			"Known limitation: named-pipe ACL grants BU GRGW, and legacy CmdQuit lacks authenticated IPC v2 peer authorization.",
			"Authenticode signing and SmartScreen reputation were not asserted.",
			"This evidence does not authorize promotion or publication.",
		},
	}
}

// MacOSARM64LocalPKGBuildOnlyProfile returns the explicit local macOS arm64 profile.
func MacOSARM64LocalPKGBuildOnlyProfile() BuildOnlyEvidenceProfile {
	return BuildOnlyEvidenceProfile{
		ID:             "macos.arm64-local.build-only",
		Producer:       "macos-arm64-local-build",
		Tuple:          PlatformProfileTuple{OS: OSMacOS, Architecture: ArchitectureARM64, Type: ArtifactTypePKG},
		FilenameSuffix: "-macos-arm64.pkg",
		GateID:         "macos.local.pkg-built",
		Limitations: []string{
			"Build-only local evidence; package installation, upgrade, and uninstall were not executed.",
			"Runtime behavior, Gatekeeper, and launch services integration were not qualified.",
			"Developer ID signing and notarization were not asserted.",
			"This evidence does not authorize promotion or publication.",
		},
	}
}

// ProduceBuildOnlyPlatformEvidence deterministically binds build-only evidence
// to the exact candidate and bytes. It can never assert runtime qualification,
// public eligibility, native signature verification, or provenance verification.
func ProduceBuildOnlyPlatformEvidence(profile BuildOnlyEvidenceProfile, input BuildOnlyEvidenceInput) (*PlatformEvidence, error) {
	if err := validateBuildOnlyProfile(profile); err != nil {
		return nil, err
	}
	if err := validatePromotionCandidate(input.Candidate); err != nil {
		return nil, err
	}
	if input.Candidate.OS != profile.Tuple.OS || input.Candidate.Architecture != profile.Tuple.Architecture || input.Candidate.Type != profile.Tuple.Type {
		return nil, evidenceError("candidate", "tuple does not match build-only profile")
	}
	expectedFilename := "wireztna-" + input.Candidate.Version + profile.FilenameSuffix
	if input.Candidate.Filename != expectedFilename {
		return nil, evidenceError("candidate.filename", fmt.Sprintf("must be %q", expectedFilename))
	}
	if len(input.ArtifactBytes) == 0 {
		return nil, evidenceError("artifact_bytes", "must not be empty")
	}
	if len(input.ArtifactBytes) > MaxBuildOnlyArtifactBytes {
		return nil, evidenceError("artifact_bytes", fmt.Sprintf("must not exceed %d bytes", MaxBuildOnlyArtifactBytes))
	}

	digest := sha256.Sum256(input.ArtifactBytes)
	actualSHA256 := fmt.Sprintf("%x", digest)
	if input.Candidate.Size != int64(len(input.ArtifactBytes)) {
		return nil, evidenceError("candidate.size", "does not match artifact bytes")
	}
	if input.Candidate.SHA256 != actualSHA256 {
		return nil, evidenceError("candidate.sha256", "does not match artifact bytes")
	}

	gates := make([]EvidenceGate, 0, 1+len(profile.FailedGateIDs))
	gates = append(gates, EvidenceGate{ID: profile.GateID, Status: EvidenceGatePassed})
	for _, gateID := range profile.FailedGateIDs {
		gates = append(gates, EvidenceGate{ID: gateID, Status: EvidenceGateFailed})
	}

	evidence := &PlatformEvidence{
		Identity: EvidenceIdentity{
			Contract:      "wireztna-platform-evidence",
			SchemaVersion: PlatformEvidenceSchemaVersion,
			EvidenceID:    profile.ID + ":" + actualSHA256,
			GeneratedAt:   input.GeneratedAt,
		},
		Source: EvidenceSource{
			Producer:    profile.Producer,
			ProducerSHA: input.ProducerGitSHA,
			TestFixture: false,
		},
		Tuple: EvidenceTuple{
			Version:      input.Candidate.Version,
			GitSHA:       input.Candidate.GitSHA,
			OS:           input.Candidate.OS,
			Architecture: input.Candidate.Architecture,
			Type:         input.Candidate.Type,
			Artifact: &EvidenceArtifact{
				Filename: input.Candidate.Filename,
				Size:     input.Candidate.Size,
				SHA256:   input.Candidate.SHA256,
			},
		},
		Claim: EvidenceClaim{
			Level:             EvidenceClaimBuildOnly,
			Artifact:          true,
			Runtime:           false,
			PublicEligibility: false,
		},
		Gates:       gates,
		Signature:   EvidenceSignature{Status: EvidenceSignatureNotAsserted},
		Provenance:  EvidenceProvenance{Status: EvidenceProvenanceNotAsserted},
		Limitations: append([]string(nil), profile.Limitations...),
	}
	if err := ValidatePlatformEvidence(evidence); err != nil {
		return nil, err
	}
	return evidence, nil
}

// MarshalPlatformEvidence returns validated deterministic JSON without adding a
// signature, receipt, trust decision, or publication location.
func MarshalPlatformEvidence(evidence *PlatformEvidence) ([]byte, error) {
	if err := ValidatePlatformEvidence(evidence); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("marshal platform evidence: %w", err)
	}
	return encoded, nil
}

func validateBuildOnlyProfile(profile BuildOnlyEvidenceProfile) error {
	if !evidenceIdentifierPattern.MatchString(profile.ID) {
		return evidenceError("profile.id", "must be a lowercase identifier of at most 128 characters")
	}
	if !evidenceIdentifierPattern.MatchString(profile.Producer) {
		return evidenceError("profile.producer", "must be a lowercase identifier of at most 128 characters")
	}
	if !coherentPlatformTuple(profile.Tuple) {
		return evidenceError("profile.tuple", "must be a coherent platform tuple")
	}
	if profile.FilenameSuffix == "" {
		return evidenceError("profile.filename_suffix", "is required")
	}
	if !evidenceIdentifierPattern.MatchString(profile.GateID) {
		return evidenceError("profile.gate_id", "must be a lowercase identifier of at most 128 characters")
	}
	seenGates := map[string]struct{}{profile.GateID: {}}
	for index, gateID := range profile.FailedGateIDs {
		if !evidenceIdentifierPattern.MatchString(gateID) {
			return evidenceError(fmt.Sprintf("profile.failed_gate_ids[%d]", index), "must be a lowercase identifier of at most 128 characters")
		}
		if _, exists := seenGates[gateID]; exists {
			return evidenceError(fmt.Sprintf("profile.failed_gate_ids[%d]", index), "is duplicated")
		}
		seenGates[gateID] = struct{}{}
	}
	if len(seenGates) > MaxEvidenceGates {
		return evidenceError("profile.failed_gate_ids", fmt.Sprintf("must produce at most %d total gates", MaxEvidenceGates))
	}
	if err := validateEvidenceLimitations(profile.Limitations); err != nil {
		return err
	}
	if len(profile.ID)+1+64 > 128 {
		return evidenceError("profile.id", "is too long to derive a bounded evidence ID")
	}
	return nil
}

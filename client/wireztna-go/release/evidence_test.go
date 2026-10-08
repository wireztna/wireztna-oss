package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPlatformEvidenceFixtures(t *testing.T) {
	type expectation struct {
		claimLevel      EvidenceClaimLevel
		validationField string
		errorContains   string
	}
	expectations := map[string]expectation{
		"positive-host-readiness.json":                  {claimLevel: EvidenceClaimHostReadiness},
		"positive-build-only.json":                      {claimLevel: EvidenceClaimBuildOnly},
		"positive-platform-qualified-test-fixture.json": {claimLevel: EvidenceClaimPlatformQualified},
		"negative-short-git-sha.json":                   {validationField: "tuple.git_sha"},
		"negative-host-claims-artifact.json":            {validationField: "claim.artifact"},
		"negative-build-claims-runtime.json":            {validationField: "claim.runtime"},
		"negative-platform-failed-gate.json":            {validationField: "gates[0].status"},
		"negative-signature-hash-drift.json":            {validationField: "signature.artifact_sha256"},
		"negative-unknown-field.json":                   {errorContains: `unknown field "unexpected"`},
	}

	paths, err := filepath.Glob("testdata/evidence/*.json")
	if err != nil {
		t.Fatalf("Glob(): %v", err)
	}
	if len(paths) != len(expectations) {
		t.Fatalf("fixture count = %d, want %d explicitly asserted fixtures", len(paths), len(expectations))
	}

	for _, path := range paths {
		path := path
		name := filepath.Base(path)
		want, exists := expectations[name]
		if !exists {
			t.Fatalf("fixture %q has no explicit expectation", name)
		}
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(): %v", err)
			}
			evidence, decodeErr := DecodePlatformEvidence(bytes.NewReader(data))
			if want.validationField != "" || want.errorContains != "" {
				if decodeErr == nil {
					t.Fatalf("DecodePlatformEvidence() error = nil, want rejection: %#v", evidence)
				}
				if want.validationField != "" {
					var validationErr *EvidenceValidationError
					if !errors.As(decodeErr, &validationErr) || validationErr.Field != want.validationField {
						t.Fatalf("DecodePlatformEvidence() error = %T %v, want EvidenceValidationError field %q", decodeErr, decodeErr, want.validationField)
					}
				} else if !strings.Contains(decodeErr.Error(), want.errorContains) {
					t.Fatalf("DecodePlatformEvidence() error = %v, want substring %q", decodeErr, want.errorContains)
				}
				return
			}
			if decodeErr != nil {
				t.Fatalf("DecodePlatformEvidence() unexpected error: %v", decodeErr)
			}
			if evidence.Claim.Level != want.claimLevel {
				t.Fatalf("claim level = %q, want %q", evidence.Claim.Level, want.claimLevel)
			}
			if !evidence.Source.TestFixture {
				t.Fatal("positive fixture must remain marked test_fixture")
			}
			eligibility, err := EvaluatePlatformEvidence(evidence, testPromotionEvaluation(t, evidence))
			if err != nil {
				t.Fatalf("EvaluatePlatformEvidence() unexpected error: %v", err)
			}
			if eligibility.PromotionEligible {
				t.Fatal("test fixture became promotion eligible")
			}
		})
	}
}

func TestDecodePlatformEvidenceStrictAndBounded(t *testing.T) {
	validJSON := platformEvidenceJSON(t, validQualifiedEvidence(true))

	unknownRoot := mutatePlatformEvidenceJSON(t, validJSON, func(root map[string]any) {
		root["unexpected"] = true
	})
	unknownIdentity := mutatePlatformEvidenceJSON(t, validJSON, func(root map[string]any) {
		root["identity"].(map[string]any)["unexpected"] = true
	})
	unknownArtifact := mutatePlatformEvidenceJSON(t, validJSON, func(root map[string]any) {
		root["tuple"].(map[string]any)["artifact"].(map[string]any)["unexpected"] = true
	})
	unknownGate := mutatePlatformEvidenceJSON(t, validJSON, func(root map[string]any) {
		root["gates"].([]any)[0].(map[string]any)["unexpected"] = true
	})
	unknownSignature := mutatePlatformEvidenceJSON(t, validJSON, func(root map[string]any) {
		root["signature"].(map[string]any)["unexpected"] = true
	})
	unknownProvenance := mutatePlatformEvidenceJSON(t, validJSON, func(root map[string]any) {
		root["provenance"].(map[string]any)["unexpected"] = true
	})
	missingFalse := mutatePlatformEvidenceJSON(t, validJSON, func(root map[string]any) {
		delete(root["source"].(map[string]any), "test_fixture")
	})
	missingLimitations := mutatePlatformEvidenceJSON(t, validJSON, func(root map[string]any) {
		delete(root, "limitations")
	})
	nullFalse := mutatePlatformEvidenceJSON(t, validJSON, func(root map[string]any) {
		root["claim"].(map[string]any)["public_eligibility"] = nil
	})
	nullArtifact := mutatePlatformEvidenceJSON(t, validJSON, func(root map[string]any) {
		root["tuple"].(map[string]any)["artifact"] = nil
	})
	caseAlias := mutatePlatformEvidenceJSON(t, validJSON, func(root map[string]any) {
		source := root["source"].(map[string]any)
		source["TEST_FIXTURE"] = source["test_fixture"]
		delete(source, "test_fixture")
	})
	duplicateRoot := bytes.Replace(validJSON, []byte(`"limitations":`), []byte(`"limitations":[],"limitations":`), 1)
	duplicateNested := bytes.Replace(validJSON, []byte(`"producer":"contract-test"`), []byte(`"producer":"other","producer":"contract-test"`), 1)
	duplicateGate := bytes.Replace(validJSON, []byte(`"id":"contract.e0"`), []byte(`"id":"other","id":"contract.e0"`), 1)

	tests := []struct {
		name    string
		reader  io.Reader
		wantErr error
	}{
		{name: "valid", reader: bytes.NewReader(validJSON)},
		{name: "surrounding whitespace", reader: strings.NewReader(" \n" + string(validJSON) + "\t")},
		{name: "nil reader", reader: nil, wantErr: ErrEmptyPlatformEvidence},
		{name: "empty", reader: strings.NewReader(""), wantErr: ErrEmptyPlatformEvidence},
		{name: "whitespace at limit", reader: strings.NewReader(strings.Repeat(" ", int(MaxPlatformEvidenceBytes))), wantErr: ErrEmptyPlatformEvidence},
		{name: "over limit", reader: strings.NewReader(strings.Repeat(" ", int(MaxPlatformEvidenceBytes)+1)), wantErr: ErrPlatformEvidenceTooLarge},
		{name: "malformed", reader: strings.NewReader(`{"identity":`)},
		{name: "trailing JSON", reader: strings.NewReader(string(validJSON) + `{}`)},
		{name: "invalid UTF-8", reader: bytes.NewReader([]byte{'{', '"', 0xff, '"', ':', '1', '}'})},
		{name: "unknown root", reader: bytes.NewReader(unknownRoot)},
		{name: "unknown identity", reader: bytes.NewReader(unknownIdentity)},
		{name: "unknown artifact", reader: bytes.NewReader(unknownArtifact)},
		{name: "unknown gate", reader: bytes.NewReader(unknownGate)},
		{name: "unknown signature", reader: bytes.NewReader(unknownSignature)},
		{name: "unknown provenance", reader: bytes.NewReader(unknownProvenance)},
		{name: "missing false boolean", reader: bytes.NewReader(missingFalse)},
		{name: "missing limitations", reader: bytes.NewReader(missingLimitations)},
		{name: "null false boolean", reader: bytes.NewReader(nullFalse)},
		{name: "null optional artifact", reader: bytes.NewReader(nullArtifact)},
		{name: "case alias", reader: bytes.NewReader(caseAlias)},
		{name: "duplicate root", reader: bytes.NewReader(duplicateRoot)},
		{name: "duplicate nested", reader: bytes.NewReader(duplicateNested)},
		{name: "duplicate gate", reader: bytes.NewReader(duplicateGate)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence, err := DecodePlatformEvidence(test.reader)
			if test.wantErr != nil || test.name != "valid" && test.name != "surrounding whitespace" {
				if err == nil {
					t.Fatalf("DecodePlatformEvidence() error = nil, want error: %#v", evidence)
				}
				if test.wantErr != nil && !errors.Is(err, test.wantErr) {
					t.Fatalf("DecodePlatformEvidence() error = %v, want errors.Is(%v)", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodePlatformEvidence() unexpected error: %v", err)
			}
		})
	}
}

func TestValidatePlatformEvidenceClaimsAndBindings(t *testing.T) {
	tests := []struct {
		name    string
		base    func() *PlatformEvidence
		mutate  func(*PlatformEvidence)
		wantErr string
	}{
		{name: "qualified valid", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }},
		{name: "host readiness valid", base: validHostReadinessEvidence},
		{name: "build only valid", base: validBuildOnlyEvidence},
		{name: "wrong contract", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Identity.Contract = "other" }, wantErr: "identity.contract"},
		{name: "wrong schema", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Identity.SchemaVersion = 2 }, wantErr: "identity.schema_version"},
		{name: "short producer sha", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Source.ProducerSHA = "bbbbbbb" }, wantErr: "source.producer_git_sha"},
		{name: "short tuple sha", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Tuple.GitSHA = "aaaaaaa" }, wantErr: "tuple.git_sha"},
		{name: "uppercase tuple sha", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Tuple.GitSHA = strings.Repeat("A", 40) }, wantErr: "tuple.git_sha"},
		{name: "host asserts artifact flag", base: validHostReadinessEvidence, mutate: func(e *PlatformEvidence) { e.Claim.Artifact = true }, wantErr: "host-readiness"},
		{name: "host contains artifact", base: validHostReadinessEvidence, mutate: func(e *PlatformEvidence) { e.Tuple.Artifact = testEvidenceArtifact() }, wantErr: "host-readiness"},
		{name: "build asserts runtime", base: validBuildOnlyEvidence, mutate: func(e *PlatformEvidence) { e.Claim.Runtime = true }, wantErr: "build-only"},
		{name: "build asserts public", base: validBuildOnlyEvidence, mutate: func(e *PlatformEvidence) { e.Claim.PublicEligibility = true }, wantErr: "build-only"},
		{name: "qualified missing artifact", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Tuple.Artifact = nil }, wantErr: "claim.artifact"},
		{name: "qualified missing runtime", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Claim.Runtime = false }, wantErr: "claim.runtime"},
		{name: "qualified no gates", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Gates = []EvidenceGate{} }, wantErr: "gates"},
		{name: "qualified failed gate", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Gates[0].Status = EvidenceGateFailed }, wantErr: "gates[0].status"},
		{name: "duplicate gate", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Gates = append(e.Gates, e.Gates[0]) }, wantErr: "duplicated"},
		{name: "signature hash drift", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Signature.ArtifactSHA256 = strings.Repeat("d", 64) }, wantErr: "signature.artifact_sha256"},
		{name: "qualified signature absent", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Signature = EvidenceSignature{Status: EvidenceSignatureNotAsserted} }, wantErr: "signature.status"},
		{name: "public ad hoc signature", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Signature.Status = EvidenceSignatureAdHoc }, wantErr: "signature.status"},
		{name: "provenance artifact drift", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Provenance.ArtifactSHA256 = strings.Repeat("d", 64) }, wantErr: "provenance.artifact_sha256"},
		{name: "provenance source drift", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Provenance.SourceGitSHA = strings.Repeat("d", 40) }, wantErr: "provenance.source_git_sha"},
		{name: "qualified provenance absent", base: func() *PlatformEvidence { return validQualifiedEvidence(false) }, mutate: func(e *PlatformEvidence) { e.Provenance = EvidenceProvenance{Status: EvidenceProvenanceNotAsserted} }, wantErr: "provenance.status"},
		{name: "unsafe artifact filename", base: validBuildOnlyEvidence, mutate: func(e *PlatformEvidence) { e.Tuple.Artifact.Filename = "../artifact.deb" }, wantErr: "filename"},
		{name: "zero artifact size", base: validBuildOnlyEvidence, mutate: func(e *PlatformEvidence) { e.Tuple.Artifact.Size = 0 }, wantErr: "size"},
		{name: "uppercase artifact hash", base: validBuildOnlyEvidence, mutate: func(e *PlatformEvidence) { e.Tuple.Artifact.SHA256 = strings.Repeat("C", 64) }, wantErr: "sha256"},
		{name: "nil gates", base: validBuildOnlyEvidence, mutate: func(e *PlatformEvidence) { e.Gates = nil }, wantErr: "gates"},
		{name: "too many gates", base: validBuildOnlyEvidence, mutate: func(e *PlatformEvidence) { e.Gates = make([]EvidenceGate, MaxEvidenceGates+1) }, wantErr: "at most"},
		{name: "nil limitations", base: validBuildOnlyEvidence, mutate: func(e *PlatformEvidence) { e.Limitations = nil }, wantErr: "limitations"},
		{name: "too many limitations", base: validBuildOnlyEvidence, mutate: func(e *PlatformEvidence) { e.Limitations = make([]string, MaxEvidenceLimitations+1) }, wantErr: "at most"},
		{name: "duplicate limitation", base: validBuildOnlyEvidence, mutate: func(e *PlatformEvidence) { e.Limitations = []string{"test only", "test only"} }, wantErr: "duplicated"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := test.base()
			if test.mutate != nil {
				test.mutate(evidence)
			}
			err := ValidatePlatformEvidence(evidence)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidatePlatformEvidence() unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidatePlatformEvidence() error = %v, want substring %q", err, test.wantErr)
			}
			var validationErr *EvidenceValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("ValidatePlatformEvidence() error type = %T, want *EvidenceValidationError", err)
			}
		})
	}
}

func TestEvaluatePlatformEvidenceRequiresExplicitExternalContext(t *testing.T) {
	evidence := validQualifiedEvidence(false)
	evaluation := testPromotionEvaluation(t, evidence)

	result, err := EvaluatePlatformEvidence(evidence, evaluation)
	if err != nil {
		t.Fatalf("EvaluatePlatformEvidence() unexpected error: %v", err)
	}
	if !result.PromotionEligible {
		t.Fatalf("EvaluatePlatformEvidence() = %#v, want eligible", result)
	}
	if !reflect.DeepEqual(result.EvaluatedCandidate, evaluation.Candidate) {
		t.Fatalf("evaluated candidate = %#v, want %#v", result.EvaluatedCandidate, evaluation.Candidate)
	}
	if result.EvidenceDigest != evaluation.Authentication.EvidenceSHA256 || result.EvidenceID != evidence.Identity.EvidenceID || result.ProfileID != evaluation.ProfileID {
		t.Fatalf("result did not preserve evaluated bindings: %#v", result)
	}

	withoutPolicyOrAuthentication := PlatformPromotionEvaluation{Candidate: evaluation.Candidate, ProfileID: evaluation.ProfileID}
	result, err = EvaluatePlatformEvidence(evidence, withoutPolicyOrAuthentication)
	if err != nil {
		t.Fatalf("EvaluatePlatformEvidence() unexpected error: %v", err)
	}
	assertIneligibleWithReason(t, result, EvidenceIneligibleAuthenticationMissing)
	assertReason(t, result, EvidenceIneligibleProducerUntrusted)
	assertReason(t, result, EvidenceIneligibleSignerUntrusted)
	assertReason(t, result, EvidenceIneligibleBuilderUntrusted)
	assertReason(t, result, EvidenceIneligibleProfileNotFound)

	invalid := validQualifiedEvidence(false)
	invalid.Gates[0].Status = EvidenceGateNotRun
	if _, err := EvaluatePlatformEvidence(invalid, testPromotionEvaluation(t, evidence)); err == nil {
		t.Fatal("EvaluatePlatformEvidence(invalid) error = nil, want validation error")
	}
}

func TestEvaluatePlatformEvidenceBindsExactCandidate(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*PromotionCandidate)
		wantReason EvidenceIneligibilityReason
	}{
		{name: "version", mutate: func(c *PromotionCandidate) { c.Version = "1.2.4" }, wantReason: EvidenceIneligibleCandidateVersionMismatch},
		{name: "full git sha", mutate: func(c *PromotionCandidate) { c.GitSHA = strings.Repeat("d", 40) }, wantReason: EvidenceIneligibleCandidateGitSHAMismatch},
		{name: "os", mutate: func(c *PromotionCandidate) { c.OS = OSWindows }, wantReason: EvidenceIneligibleCandidateOSMismatch},
		{name: "architecture", mutate: func(c *PromotionCandidate) { c.Architecture = ArchitectureARM64 }, wantReason: EvidenceIneligibleCandidateArchMismatch},
		{name: "artifact type", mutate: func(c *PromotionCandidate) { c.Type = ArtifactTypeMSI }, wantReason: EvidenceIneligibleCandidateTypeMismatch},
		{name: "filename", mutate: func(c *PromotionCandidate) { c.Filename = "different.deb" }, wantReason: EvidenceIneligibleCandidateFilenameMismatch},
		{name: "size", mutate: func(c *PromotionCandidate) { c.Size++ }, wantReason: EvidenceIneligibleCandidateSizeMismatch},
		{name: "artifact hash", mutate: func(c *PromotionCandidate) { c.SHA256 = strings.Repeat("d", 64) }, wantReason: EvidenceIneligibleCandidateDigestMismatch},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := validQualifiedEvidence(false)
			evaluation := testPromotionEvaluation(t, evidence)
			test.mutate(&evaluation.Candidate)
			result, err := EvaluatePlatformEvidence(evidence, evaluation)
			if err != nil {
				t.Fatalf("EvaluatePlatformEvidence() unexpected error: %v", err)
			}
			assertIneligibleWithReason(t, result, test.wantReason)
		})
	}
}

func TestEvaluatePlatformEvidenceRequiresAuthenticatedTrustedIdentities(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*PlatformPromotionEvaluation)
		wantReason EvidenceIneligibilityReason
	}{
		{name: "missing authentication", mutate: func(e *PlatformPromotionEvaluation) { e.Authentication = nil }, wantReason: EvidenceIneligibleAuthenticationMissing},
		{name: "untrusted boundary", mutate: func(e *PlatformPromotionEvaluation) { e.Authentication.Boundary = "unknown-boundary" }, wantReason: EvidenceIneligibleAuthenticationBoundary},
		{name: "different evidence id", mutate: func(e *PlatformPromotionEvaluation) { e.Authentication.EvidenceID = "other.evidence" }, wantReason: EvidenceIneligibleAuthenticationEvidenceID},
		{name: "different authenticated digest", mutate: func(e *PlatformPromotionEvaluation) { e.Authentication.EvidenceSHA256 = strings.Repeat("d", 64) }, wantReason: EvidenceIneligibleAuthenticationDigest},
		{name: "different authenticated producer", mutate: func(e *PlatformPromotionEvaluation) { e.Authentication.Producer = "other-producer" }, wantReason: EvidenceIneligibleAuthenticationProducer},
		{name: "different authenticated signer", mutate: func(e *PlatformPromotionEvaluation) { e.Authentication.SignerIdentity = "other-signer" }, wantReason: EvidenceIneligibleAuthenticationSigner},
		{name: "different authenticated builder", mutate: func(e *PlatformPromotionEvaluation) { e.Authentication.BuilderID = "other-builder" }, wantReason: EvidenceIneligibleAuthenticationBuilder},
		{name: "untrusted producer", mutate: func(e *PlatformPromotionEvaluation) { e.Policy.TrustedProducers = nil }, wantReason: EvidenceIneligibleProducerUntrusted},
		{name: "untrusted signer", mutate: func(e *PlatformPromotionEvaluation) { e.Policy.TrustedSigners = nil }, wantReason: EvidenceIneligibleSignerUntrusted},
		{name: "untrusted builder", mutate: func(e *PlatformPromotionEvaluation) { e.Policy.TrustedBuilders = nil }, wantReason: EvidenceIneligibleBuilderUntrusted},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := validQualifiedEvidence(false)
			evaluation := testPromotionEvaluation(t, evidence)
			test.mutate(&evaluation)
			result, err := EvaluatePlatformEvidence(evidence, evaluation)
			if err != nil {
				t.Fatalf("EvaluatePlatformEvidence() unexpected error: %v", err)
			}
			assertIneligibleWithReason(t, result, test.wantReason)
		})
	}
}

func TestEvaluatePlatformEvidenceEnforcesProfileGates(t *testing.T) {
	tests := []struct {
		name       string
		gates      []EvidenceGate
		wantReason EvidenceIneligibilityReason
	}{
		{name: "arbitrary gate", gates: []EvidenceGate{{ID: "attacker.self-asserted", Status: EvidenceGatePassed}}, wantReason: EvidenceIneligibleRequiredGateMissing},
		{name: "required gate missing", gates: []EvidenceGate{{ID: "contract.e0", Status: EvidenceGatePassed}}, wantReason: EvidenceIneligibleRequiredGateMissing},
		{name: "unexpected extra gate", gates: []EvidenceGate{{ID: "contract.e0", Status: EvidenceGatePassed}, {ID: "contract.e1", Status: EvidenceGatePassed}, {ID: "attacker.extra", Status: EvidenceGatePassed}}, wantReason: EvidenceIneligibleUnexpectedGate},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := validQualifiedEvidence(false)
			evidence.Gates = test.gates
			evaluation := testPromotionEvaluation(t, evidence)
			evaluation.Policy.Profiles[0].RequiredGateIDs = []string{"contract.e0", "contract.e1"}
			evaluation.Policy.Profiles[0].AllowedGateIDs = []string{"contract.e0", "contract.e1"}
			result, err := EvaluatePlatformEvidence(evidence, evaluation)
			if err != nil {
				t.Fatalf("EvaluatePlatformEvidence() unexpected error: %v", err)
			}
			assertIneligibleWithReason(t, result, test.wantReason)
			if test.name == "arbitrary gate" {
				assertReason(t, result, EvidenceIneligibleUnexpectedGate)
			}
		})
	}
}

func TestEvaluatePlatformEvidenceEnforcesProfileMatrix(t *testing.T) {
	tests := []struct {
		name       string
		tuple      PlatformProfileTuple
		matrix     []PlatformProfileTuple
		eligible   bool
		wantReason EvidenceIneligibilityReason
	}{
		{name: "macos arm64 pkg declared", tuple: PlatformProfileTuple{OS: OSMacOS, Architecture: ArchitectureARM64, Type: ArtifactTypePKG}, matrix: []PlatformProfileTuple{{OS: OSMacOS, Architecture: ArchitectureARM64, Type: ArtifactTypePKG}}, eligible: true},
		{name: "macos amd64 denied even if declared", tuple: PlatformProfileTuple{OS: OSMacOS, Architecture: ArchitectureAMD64, Type: ArtifactTypePKG}, matrix: []PlatformProfileTuple{{OS: OSMacOS, Architecture: ArchitectureAMD64, Type: ArtifactTypePKG}}, wantReason: EvidenceIneligibleProfileTupleNotAllowed},
		{name: "macos incoherent msi denied", tuple: PlatformProfileTuple{OS: OSMacOS, Architecture: ArchitectureARM64, Type: ArtifactTypeMSI}, matrix: []PlatformProfileTuple{{OS: OSMacOS, Architecture: ArchitectureARM64, Type: ArtifactTypeMSI}}, wantReason: EvidenceIneligibleProfileTupleNotAllowed},
		{name: "linux declared row", tuple: PlatformProfileTuple{OS: OSLinux, Architecture: ArchitectureARM64, Type: ArtifactTypeDEB}, matrix: []PlatformProfileTuple{{OS: OSLinux, Architecture: ArchitectureARM64, Type: ArtifactTypeDEB}}, eligible: true},
		{name: "linux undeclared row", tuple: PlatformProfileTuple{OS: OSLinux, Architecture: ArchitectureARM64, Type: ArtifactTypeDEB}, matrix: []PlatformProfileTuple{{OS: OSLinux, Architecture: ArchitectureAMD64, Type: ArtifactTypeDEB}}, wantReason: EvidenceIneligibleProfileTupleNotAllowed},
		{name: "windows declared row", tuple: PlatformProfileTuple{OS: OSWindows, Architecture: ArchitectureAMD64, Type: ArtifactTypeMSI}, matrix: []PlatformProfileTuple{{OS: OSWindows, Architecture: ArchitectureAMD64, Type: ArtifactTypeMSI}}, eligible: true},
		{name: "windows undeclared row", tuple: PlatformProfileTuple{OS: OSWindows, Architecture: ArchitectureARM64, Type: ArtifactTypeMSI}, matrix: []PlatformProfileTuple{{OS: OSWindows, Architecture: ArchitectureAMD64, Type: ArtifactTypeMSI}}, wantReason: EvidenceIneligibleProfileTupleNotAllowed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := validQualifiedEvidence(false)
			evidence.Tuple.OS = test.tuple.OS
			evidence.Tuple.Architecture = test.tuple.Architecture
			evidence.Tuple.Type = test.tuple.Type
			evaluation := testPromotionEvaluation(t, evidence)
			evaluation.Policy.Profiles[0].Matrix = test.matrix
			result, err := EvaluatePlatformEvidence(evidence, evaluation)
			if err != nil {
				t.Fatalf("EvaluatePlatformEvidence() unexpected error: %v", err)
			}
			if test.eligible {
				if !result.PromotionEligible {
					t.Fatalf("EvaluatePlatformEvidence() = %#v, want eligible under explicit test policy", result)
				}
				return
			}
			assertIneligibleWithReason(t, result, test.wantReason)
		})
	}
}

func TestEvaluatePlatformEvidenceTestFixtureAlwaysIneligible(t *testing.T) {
	evidence := validQualifiedEvidence(true)
	result, err := EvaluatePlatformEvidence(evidence, testPromotionEvaluation(t, evidence))
	if err != nil {
		t.Fatalf("EvaluatePlatformEvidence() unexpected error: %v", err)
	}
	assertIneligibleWithReason(t, result, EvidenceIneligibleTestFixture)
}

func TestBuildOnlyRequiresArtifact(t *testing.T) {
	evidence := validBuildOnlyEvidence()
	evidence.Claim.Artifact = false
	evidence.Tuple.Artifact = nil

	err := ValidatePlatformEvidence(evidence)
	var validationErr *EvidenceValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "claim.artifact" {
		t.Fatalf("ValidatePlatformEvidence() error = %T %v, want claim.artifact validation error", err, err)
	}
}

func TestValidatePlatformEvidenceNil(t *testing.T) {
	err := ValidatePlatformEvidence(nil)
	var validationErr *EvidenceValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("ValidatePlatformEvidence(nil) error = %T %v, want *EvidenceValidationError", err, err)
	}
}

func validQualifiedEvidence(testFixture bool) *PlatformEvidence {
	artifact := testEvidenceArtifact()
	return &PlatformEvidence{
		Identity: EvidenceIdentity{
			Contract:      "wireztna-platform-evidence",
			SchemaVersion: PlatformEvidenceSchemaVersion,
			EvidenceID:    "fixture.qualified",
			GeneratedAt:   time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		},
		Source: EvidenceSource{
			Producer:    "contract-test",
			ProducerSHA: strings.Repeat("b", 40),
			TestFixture: testFixture,
		},
		Tuple: EvidenceTuple{
			Version:      "1.2.3",
			GitSHA:       strings.Repeat("a", 40),
			OS:           OSLinux,
			Architecture: ArchitectureAMD64,
			Type:         ArtifactTypeDEB,
			Artifact:     artifact,
		},
		Claim: EvidenceClaim{
			Level:             EvidenceClaimPlatformQualified,
			Artifact:          true,
			Runtime:           true,
			PublicEligibility: true,
		},
		Gates: []EvidenceGate{
			{ID: "contract.e0", Status: EvidenceGatePassed},
			{ID: "contract.e1", Status: EvidenceGatePassed},
		},
		Signature: EvidenceSignature{
			Status:         EvidenceSignatureRequiredAndVerified,
			ArtifactSHA256: artifact.SHA256,
			SignerIdentity: "contract-test-signer",
		},
		Provenance: EvidenceProvenance{
			Status:         EvidenceProvenanceVerified,
			ArtifactSHA256: artifact.SHA256,
			SourceGitSHA:   strings.Repeat("a", 40),
			BuilderID:      "contract-test-builder",
		},
		Limitations: []string{"contract fixture only; not platform support evidence"},
	}
}

func validBuildOnlyEvidence() *PlatformEvidence {
	evidence := validQualifiedEvidence(true)
	evidence.Identity.EvidenceID = "fixture.build-only"
	evidence.Claim = EvidenceClaim{Level: EvidenceClaimBuildOnly, Artifact: true}
	evidence.Gates = []EvidenceGate{{ID: "contract.e0", Status: EvidenceGatePassed}}
	evidence.Signature = EvidenceSignature{Status: EvidenceSignatureNotAsserted}
	evidence.Provenance = EvidenceProvenance{Status: EvidenceProvenanceNotAsserted}
	evidence.Limitations = []string{"build evidence only; runtime and public eligibility not asserted"}
	return evidence
}

func validHostReadinessEvidence() *PlatformEvidence {
	evidence := validBuildOnlyEvidence()
	evidence.Identity.EvidenceID = "fixture.host-readiness"
	evidence.Tuple.Artifact = nil
	evidence.Claim = EvidenceClaim{Level: EvidenceClaimHostReadiness}
	evidence.Gates = []EvidenceGate{{ID: "contract.host", Status: EvidenceGatePassed}}
	evidence.Limitations = []string{"host readiness only; no artifact exists or is asserted"}
	return evidence
}

func testEvidenceArtifact() *EvidenceArtifact {
	return &EvidenceArtifact{
		Filename: "wireztna_contract_fixture_1.2.3_amd64.deb",
		Size:     1024,
		SHA256:   strings.Repeat("c", 64),
	}
}

func testPromotionEvaluation(t *testing.T, evidence *PlatformEvidence) PlatformPromotionEvaluation {
	t.Helper()
	artifact := evidence.Tuple.Artifact
	if artifact == nil {
		artifact = testEvidenceArtifact()
	}
	candidate := PromotionCandidate{
		Version:      evidence.Tuple.Version,
		GitSHA:       evidence.Tuple.GitSHA,
		OS:           evidence.Tuple.OS,
		Architecture: evidence.Tuple.Architecture,
		Type:         evidence.Tuple.Type,
		Filename:     artifact.Filename,
		Size:         artifact.Size,
		SHA256:       artifact.SHA256,
	}
	digest, err := PlatformEvidenceDigest(evidence)
	if err != nil {
		t.Fatalf("PlatformEvidenceDigest(): %v", err)
	}
	gateIDs := make([]string, 0, len(evidence.Gates))
	for _, gate := range evidence.Gates {
		gateIDs = append(gateIDs, gate.ID)
	}
	const profileID = "test.explicit-profile"
	return PlatformPromotionEvaluation{
		Candidate: candidate,
		Authentication: &AuthenticatedPlatformEvidence{
			Boundary:       "test-authentication-boundary",
			EvidenceID:     evidence.Identity.EvidenceID,
			EvidenceSHA256: digest,
			Producer:       evidence.Source.Producer,
			SignerIdentity: evidence.Signature.SignerIdentity,
			BuilderID:      evidence.Provenance.BuilderID,
		},
		Policy: PlatformPromotionPolicy{
			TrustedBoundaries: []string{"test-authentication-boundary"},
			TrustedProducers:  []string{evidence.Source.Producer},
			TrustedSigners:    []string{evidence.Signature.SignerIdentity},
			TrustedBuilders:   []string{evidence.Provenance.BuilderID},
			Profiles: []PlatformPromotionProfile{{
				ID:              profileID,
				Matrix:          []PlatformProfileTuple{{OS: evidence.Tuple.OS, Architecture: evidence.Tuple.Architecture, Type: evidence.Tuple.Type}},
				RequiredGateIDs: append([]string(nil), gateIDs...),
				AllowedGateIDs:  append([]string(nil), gateIDs...),
			}},
		},
		ProfileID: profileID,
	}
}

func assertIneligibleWithReason(t *testing.T, result EvidenceEligibilityResult, reason EvidenceIneligibilityReason) {
	t.Helper()
	if result.PromotionEligible {
		t.Fatalf("PromotionEligible = true, want false; result=%#v", result)
	}
	assertReason(t, result, reason)
}

func assertReason(t *testing.T, result EvidenceEligibilityResult, reason EvidenceIneligibilityReason) {
	t.Helper()
	for _, actual := range result.Reasons {
		if actual == reason {
			return
		}
	}
	t.Fatalf("reasons = %v, want %q", result.Reasons, reason)
}

func platformEvidenceJSON(t *testing.T, evidence *PlatformEvidence) []byte {
	t.Helper()
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	return data
}

func mutatePlatformEvidenceJSON(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("json.Unmarshal(): %v", err)
	}
	mutate(root)
	result, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	return result
}

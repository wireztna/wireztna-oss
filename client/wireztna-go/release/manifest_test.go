package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDecodeManifest(t *testing.T) {
	validJSON := manifestJSON(t, validManifest())

	unknownRoot := withJSONMutation(t, validJSON, func(root map[string]any) {
		root["unexpected"] = true
	})
	unknownCompatibility := withJSONMutation(t, validJSON, func(root map[string]any) {
		root["compatibility"].(map[string]any)["unexpected"] = true
	})
	unknownArtifact := withJSONMutation(t, validJSON, func(root map[string]any) {
		root["artifacts"].([]any)[0].(map[string]any)["unexpected"] = true
	})
	missingReauth := withJSONMutation(t, validJSON, func(root map[string]any) {
		delete(root["compatibility"].(map[string]any), "reauth_on_rollback")
	})
	missingRollout := withJSONMutation(t, validJSON, func(root map[string]any) {
		delete(root, "rollout_percentage")
	})
	nullRollout := withJSONMutation(t, validJSON, func(root map[string]any) {
		root["rollout_percentage"] = nil
	})
	nullReauth := withJSONMutation(t, validJSON, func(root map[string]any) {
		root["compatibility"].(map[string]any)["reauth_on_rollback"] = nil
	})
	caseAliasRoot := withJSONMutation(t, validJSON, func(root map[string]any) {
		root["CHANNEL"] = "beta"
	})
	caseAliasCompatibility := withJSONMutation(t, validJSON, func(root map[string]any) {
		root["compatibility"].(map[string]any)["IPC_MIN"] = float64(2)
	})
	caseAliasArtifact := withJSONMutation(t, validJSON, func(root map[string]any) {
		root["artifacts"].([]any)[0].(map[string]any)["OS"] = "windows"
	})
	invalidChannel := withJSONMutation(t, validJSON, func(root map[string]any) {
		root["channel"] = "nightly"
	})
	duplicateRoot := bytes.Replace(validJSON,
		[]byte(`"channel":"stable"`),
		[]byte(`"channel":"pilot","channel":"stable"`), 1)
	duplicateCompatibility := bytes.Replace(validJSON,
		[]byte(`"ipc_min":1`),
		[]byte(`"ipc_min":2,"ipc_min":1`), 1)
	duplicateArtifactField := bytes.Replace(validJSON,
		[]byte(`"os":"linux"`),
		[]byte(`"os":"windows","os":"linux"`), 1)
	duplicateTupleManifest := validManifest()
	duplicateTupleManifest.Artifacts = append(duplicateTupleManifest.Artifacts, duplicateTupleManifest.Artifacts[0])
	duplicateTuple := manifestJSON(t, duplicateTupleManifest)

	tests := []struct {
		name    string
		reader  io.Reader
		wantErr bool
		isErr   error
	}{
		{name: "valid", reader: bytes.NewReader(validJSON)},
		{name: "valid with surrounding whitespace", reader: strings.NewReader(" \n" + string(validJSON) + "\t")},
		{name: "nil reader", reader: nil, wantErr: true, isErr: ErrEmptyManifest},
		{name: "empty", reader: strings.NewReader(""), wantErr: true, isErr: ErrEmptyManifest},
		{name: "whitespace only", reader: strings.NewReader(" \n\t"), wantErr: true, isErr: ErrEmptyManifest},
		{name: "too large", reader: strings.NewReader(strings.Repeat(" ", int(MaxManifestBytes)+1)), wantErr: true, isErr: ErrManifestTooLarge},
		{name: "malformed", reader: strings.NewReader(`{"schema_version":`), wantErr: true},
		{name: "trailing JSON", reader: strings.NewReader(string(validJSON) + `{}`), wantErr: true},
		{name: "unknown root field", reader: bytes.NewReader(unknownRoot), wantErr: true},
		{name: "unknown compatibility field", reader: bytes.NewReader(unknownCompatibility), wantErr: true},
		{name: "unknown artifact field", reader: bytes.NewReader(unknownArtifact), wantErr: true},
		{name: "missing false boolean", reader: bytes.NewReader(missingReauth), wantErr: true},
		{name: "missing zero-valid rollout", reader: bytes.NewReader(missingRollout), wantErr: true},
		{name: "null zero-valid rollout", reader: bytes.NewReader(nullRollout), wantErr: true},
		{name: "null false boolean", reader: bytes.NewReader(nullReauth), wantErr: true},
		{name: "case alias at root", reader: bytes.NewReader(caseAliasRoot), wantErr: true},
		{name: "case alias in compatibility", reader: bytes.NewReader(caseAliasCompatibility), wantErr: true},
		{name: "case alias in artifact", reader: bytes.NewReader(caseAliasArtifact), wantErr: true},
		{name: "duplicate root field", reader: bytes.NewReader(duplicateRoot), wantErr: true},
		{name: "duplicate compatibility field", reader: bytes.NewReader(duplicateCompatibility), wantErr: true},
		{name: "duplicate artifact field", reader: bytes.NewReader(duplicateArtifactField), wantErr: true},
		{name: "duplicate artifact tuple", reader: bytes.NewReader(duplicateTuple), wantErr: true},
		{name: "invalid enum", reader: bytes.NewReader(invalidChannel), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, err := DecodeManifest(test.reader)
			if test.wantErr {
				if err == nil {
					t.Fatal("DecodeManifest() error = nil, want error")
				}
				if test.isErr != nil && !errors.Is(err, test.isErr) {
					t.Fatalf("DecodeManifest() error = %v, want errors.Is(%v)", err, test.isErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeManifest() unexpected error: %v", err)
			}
			if manifest.Channel != ChannelStable || manifest.Artifacts[0].Type != ArtifactTypeDEB {
				t.Fatalf("DecodeManifest() returned unexpected manifest: %#v", manifest)
			}
		})
	}
}

func TestValidateManifest(t *testing.T) {
	nonUTC := time.FixedZone("UTC+1", 60*60)

	tests := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr string
	}{
		{name: "valid"},
		{name: "nil artifacts", mutate: func(m *Manifest) { m.Artifacts = nil }, wantErr: "artifacts"},
		{name: "wrong schema", mutate: func(m *Manifest) { m.SchemaVersion = 2 }, wantErr: "schema_version"},
		{name: "invalid channel", mutate: func(m *Manifest) { m.Channel = "nightly" }, wantErr: "channel"},
		{name: "empty version", mutate: func(m *Manifest) { m.Version = "" }, wantErr: "version"},
		{name: "noncanonical version leading zero", mutate: func(m *Manifest) { m.Version = "01.2.3" }, wantErr: "version"},
		{name: "noncanonical prerelease", mutate: func(m *Manifest) { m.Version = "1.2.3-beta.1" }, wantErr: "version"},
		{name: "short git sha", mutate: func(m *Manifest) { m.GitSHA = strings.Repeat("a", 39) }, wantErr: "git_sha"},
		{name: "uppercase git sha", mutate: func(m *Manifest) { m.GitSHA = strings.Repeat("A", 40) }, wantErr: "git_sha"},
		{name: "missing publication time", mutate: func(m *Manifest) { m.PublishedAt = time.Time{} }, wantErr: "published_at"},
		{name: "publication time not UTC", mutate: func(m *Manifest) { m.PublishedAt = m.PublishedAt.In(nonUTC) }, wantErr: "published_at"},
		{name: "missing expiry", mutate: func(m *Manifest) { m.ExpiresAt = time.Time{} }, wantErr: "expires_at"},
		{name: "expiry not UTC", mutate: func(m *Manifest) { m.ExpiresAt = m.ExpiresAt.In(nonUTC) }, wantErr: "expires_at"},
		{name: "expiry equals publication", mutate: func(m *Manifest) { m.ExpiresAt = m.PublishedAt }, wantErr: "after"},
		{name: "zero channel epoch", mutate: func(m *Manifest) { m.ChannelEpoch = 0 }, wantErr: "channel_epoch"},
		{name: "invalid minimum version", mutate: func(m *Manifest) { m.MinimumSupportedVersion = "v1.2.3" }, wantErr: "minimum_supported_version"},
		{name: "negative rollout", mutate: func(m *Manifest) { m.RolloutPercentage = -1 }, wantErr: "rollout_percentage"},
		{name: "rollout above 100", mutate: func(m *Manifest) { m.RolloutPercentage = 101 }, wantErr: "rollout_percentage"},
		{name: "zero ipc min", mutate: func(m *Manifest) { m.Compatibility.IPCMin = 0 }, wantErr: "ipc_min"},
		{name: "zero ipc max", mutate: func(m *Manifest) { m.Compatibility.IPCMax = 0 }, wantErr: "ipc_max"},
		{name: "reversed ipc range", mutate: func(m *Manifest) { m.Compatibility.IPCMin = 3 }, wantErr: "ipc_min"},
		{name: "invalid service minimum", mutate: func(m *Manifest) { m.Compatibility.ServiceMin = "1.2" }, wantErr: "service_min"},
		{name: "invalid ui minimum", mutate: func(m *Manifest) { m.Compatibility.UIMin = "1.2.3+build" }, wantErr: "ui_min"},
		{name: "zero migration epoch", mutate: func(m *Manifest) { m.Compatibility.MigrationEpoch = 0 }, wantErr: "migration_epoch"},
		{name: "missing rollback list", mutate: func(m *Manifest) { m.Compatibility.RollbackTo = nil }, wantErr: "rollback_to"},
		{name: "invalid rollback version", mutate: func(m *Manifest) { m.Compatibility.RollbackTo[0] = "1.2.x" }, wantErr: "rollback_to"},
		{name: "invalid artifact os", mutate: func(m *Manifest) { m.Artifacts[0].OS = "freebsd" }, wantErr: "os"},
		{name: "invalid artifact architecture", mutate: func(m *Manifest) { m.Artifacts[0].Architecture = "386" }, wantErr: "arch"},
		{name: "invalid artifact type", mutate: func(m *Manifest) { m.Artifacts[0].Type = "tar" }, wantErr: "type"},
		{name: "missing artifact url", mutate: func(m *Manifest) { m.Artifacts[0].URL = "" }, wantErr: "url"},
		{name: "relative artifact url", mutate: func(m *Manifest) { m.Artifacts[0].URL = "clients/file.deb" }, wantErr: "url"},
		{name: "zero artifact size", mutate: func(m *Manifest) { m.Artifacts[0].Size = 0 }, wantErr: "size"},
		{name: "negative artifact size", mutate: func(m *Manifest) { m.Artifacts[0].Size = -1 }, wantErr: "size"},
		{name: "short artifact hash", mutate: func(m *Manifest) { m.Artifacts[0].SHA256 = strings.Repeat("a", 63) }, wantErr: "sha256"},
		{name: "uppercase artifact hash", mutate: func(m *Manifest) { m.Artifacts[0].SHA256 = strings.Repeat("A", 64) }, wantErr: "sha256"},
		{name: "invalid signature status", mutate: func(m *Manifest) { m.Artifacts[0].NativeSignature = "unknown" }, wantErr: "native_signature"},
		{name: "missing provenance", mutate: func(m *Manifest) { m.Artifacts[0].ProvenanceURL = "" }, wantErr: "provenance_url"},
		{name: "relative provenance", mutate: func(m *Manifest) { m.Artifacts[0].ProvenanceURL = "provenance.json" }, wantErr: "provenance_url"},
		{name: "duplicate artifact tuple", mutate: func(m *Manifest) { m.Artifacts = append(m.Artifacts, m.Artifacts[0]) }, wantErr: "duplicate"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			if test.mutate != nil {
				test.mutate(manifest)
			}

			err := ValidateManifest(manifest)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateManifest() unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateManifest() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateManifestRejectsAdversarialURLs(t *testing.T) {
	base := immutableBaseURL()
	validURL := base + "artifacts/wireztna.deb"
	wrongSHA := strings.Repeat("c", 40)
	cases := []struct {
		name  string
		value string
	}{
		{name: "HTTP", value: strings.Replace(validURL, "https://", "http://", 1)},
		{name: "uppercase scheme", value: strings.Replace(validURL, "https://", "HTTPS://", 1)},
		{name: "uppercase host", value: strings.Replace(validURL, "downloads.example.test", "Downloads.Example.Test", 1)},
		{name: "explicit default HTTPS port", value: strings.Replace(validURL, "downloads.example.test", "downloads.example.test:443", 1)},
		{name: "zero-padded port", value: strings.Replace(validURL, "downloads.example.test", "downloads.example.test:08443", 1)},
		{name: "trailing host dot", value: strings.Replace(validURL, "downloads.example.test", "downloads.example.test.", 1)},
		{name: "empty port", value: strings.Replace(validURL, "downloads.example.test", "downloads.example.test:", 1)},
		{name: "empty hostname label", value: strings.Replace(validURL, "downloads.example.test", "downloads..example.test", 1)},
		{name: "IPv4 literal", value: strings.Replace(validURL, "downloads.example.test", "127.0.0.1", 1)},
		{name: "legacy octal IPv4 alias", value: strings.Replace(validURL, "downloads.example.test", "0177.0.0.1", 1)},
		{name: "legacy hexadecimal IPv4 alias", value: strings.Replace(validURL, "downloads.example.test", "0x7f.0.0.1", 1)},
		{name: "authority without hostname", value: strings.Replace(validURL, "downloads.example.test", ":443", 1)},
		{name: "userinfo", value: strings.Replace(validURL, "https://", "https://user:password@", 1)},
		{name: "invalid UTF-8", value: base + "artifacts/\xff.deb"},
		{name: "query", value: validURL + "?token=mutable"},
		{name: "empty query", value: validURL + "?"},
		{name: "fragment", value: validURL + "#section"},
		{name: "empty fragment", value: validURL + "#"},
		{name: "parent dot segment", value: base + "artifacts/../wireztna.deb"},
		{name: "current dot segment", value: base + "artifacts/./wireztna.deb"},
		{name: "encoded unreserved character", value: base + "artifacts/%77ireztna.deb"},
		{name: "percent-decoded invalid UTF-8", value: base + "artifacts/%ff.deb"},
		{name: "encoded path separator", value: base + "artifacts%2fwireztna.deb"},
		{name: "encoded dot segment", value: base + "artifacts/%2e%2e/wireztna.deb"},
		{name: "double encoded dot segment", value: base + "artifacts/%252e%252e/wireztna.deb"},
		{name: "double path separator", value: base + "artifacts//wireztna.deb"},
		{name: "object with trailing slash", value: base + "artifacts/wireztna.deb/"},
		{name: "unsafe reserved path character", value: base + "artifacts/wireztna:latest.deb"},
		{name: "non-ASCII path", value: base + "artifacts/wireztna-é.deb"},
		{name: "backslash", value: base + `artifacts\wireztna.deb`},
		{name: "encoded backslash", value: base + "artifacts/%5cwireztna.deb"},
		{name: "wrong version", value: strings.Replace(validURL, "/1.2.3/", "/1.2.4/", 1)},
		{name: "wrong git sha", value: strings.Replace(validURL, strings.Repeat("a", 40), wrongSHA, 1)},
		{name: "sha prefix lookalike", value: strings.Replace(validURL, strings.Repeat("a", 40)+"/", strings.Repeat("a", 40)+"0/", 1)},
		{name: "release directory only", value: base},
	}
	for control := byte(0); control <= 0x1f; control++ {
		cases = append(cases, struct {
			name  string
			value string
		}{
			name:  fmt.Sprintf("C0 control 0x%02x", control),
			value: base + "artifacts/wire" + string([]byte{control}) + "ztna.deb",
		})
	}
	cases = append(cases, struct {
		name  string
		value string
	}{
		name:  "DEL control 0x7f",
		value: base + "artifacts/wire" + string([]byte{0x7f}) + "ztna.deb",
	})

	for _, field := range []string{"url", "provenance_url"} {
		for _, test := range cases {
			t.Run(field+"/"+test.name, func(t *testing.T) {
				manifest := validManifest()
				if field == "url" {
					manifest.Artifacts[0].URL = test.value
				} else {
					manifest.Artifacts[0].ProvenanceURL = test.value
				}
				if err := ValidateManifest(manifest); err == nil || !strings.Contains(err.Error(), field) {
					t.Fatalf("ValidateManifest() error = %v, want %s URL rejection", err, field)
				}
			})
		}
	}
}

func TestCanonicalManifestBytesGolden(t *testing.T) {
	manifest := canonicalManifestFixture()
	got, err := CanonicalManifestBytes(manifest)
	if err != nil {
		t.Fatalf("CanonicalManifestBytes() unexpected error: %v", err)
	}

	const want = `{"schema_version":1,"channel":"stable","version":"1.2.3","git_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","published_at":"2026-01-02T03:04:05.123Z","expires_at":"2026-01-03T03:04:05.123Z","channel_epoch":1,"minimum_supported_version":"1.0.0","rollout_percentage":100,"compatibility":{"ipc_min":1,"ipc_max":2,"service_min":"1.0.0","ui_min":"1.0.0","migration_epoch":1,"rollback_to":["1.2.2"],"reauth_on_rollback":false},"artifacts":[{"os":"linux","arch":"amd64","type":"deb","url":"https://downloads.example.test/clients/1.2.3/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/artifacts/wireztna_1.2.3_amd64.deb","size":1024,"sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","native_signature":"required-and-verified","provenance_url":"https://downloads.example.test/clients/1.2.3/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/provenance/linux-amd64.intoto.jsonl"},{"os":"linux","arch":"arm64","type":"portable","url":"https://downloads.example.test/clients/1.2.3/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/artifacts/wireztna_1.2.3_arm64","size":2048,"sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","native_signature":"required-and-verified","provenance_url":"https://downloads.example.test/clients/1.2.3/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/provenance/linux-arm64.intoto.jsonl"},{"os":"windows","arch":"amd64","type":"msi","url":"https://downloads.example.test/clients/1.2.3/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/artifacts/wireztna_1.2.3_amd64.msi","size":4096,"sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","native_signature":"required-and-verified","provenance_url":"https://downloads.example.test/clients/1.2.3/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/provenance/windows-amd64.intoto.jsonl"}]}`
	if string(got) != want {
		t.Fatalf("CanonicalManifestBytes() mismatch\ngot:  %s\nwant: %s", got, want)
	}
	if bytes.Contains(got, []byte("\n")) {
		t.Fatal("CanonicalManifestBytes() appended a newline")
	}
	if bytes.Contains(got, []byte(`"signature"`)) {
		t.Fatal("CanonicalManifestBytes() included a detached signature")
	}
}

func TestCanonicalManifestBytesEquivalentArtifactPermutations(t *testing.T) {
	artifacts := canonicalManifestFixture().Artifacts
	permutations := [][]int{
		{0, 1, 2},
		{0, 2, 1},
		{1, 0, 2},
		{1, 2, 0},
		{2, 0, 1},
		{2, 1, 0},
	}

	var want []byte
	for _, permutation := range permutations {
		manifest := canonicalManifestFixture()
		manifest.Artifacts = []Artifact{
			artifacts[permutation[0]],
			artifacts[permutation[1]],
			artifacts[permutation[2]],
		}
		got, err := CanonicalManifestBytes(manifest)
		if err != nil {
			t.Fatalf("CanonicalManifestBytes(%v) unexpected error: %v", permutation, err)
		}
		if want == nil {
			want = got
			continue
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("CanonicalManifestBytes(%v) differs by permutation\ngot:  %s\nwant: %s", permutation, got, want)
		}
	}
}

func TestCanonicalManifestBytesIdempotent(t *testing.T) {
	first, err := CanonicalManifestBytes(canonicalManifestFixture())
	if err != nil {
		t.Fatalf("first CanonicalManifestBytes() unexpected error: %v", err)
	}
	decoded, err := DecodeManifest(bytes.NewReader(first))
	if err != nil {
		t.Fatalf("DecodeManifest(canonical) unexpected error: %v", err)
	}
	second, err := CanonicalManifestBytes(decoded)
	if err != nil {
		t.Fatalf("second CanonicalManifestBytes() unexpected error: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("canonicalization is not idempotent\nfirst:  %s\nsecond: %s", first, second)
	}
}

func TestCanonicalManifestBytesDoesNotMutateInput(t *testing.T) {
	manifest := canonicalManifestFixture()
	before := cloneManifest(manifest)

	if _, err := CanonicalManifestBytes(manifest); err != nil {
		t.Fatalf("CanonicalManifestBytes() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(manifest, before) {
		t.Fatalf("CanonicalManifestBytes() mutated input\ngot:  %#v\nwant: %#v", manifest, before)
	}
}

func TestCanonicalManifestBytesRejectsInvalidManifest(t *testing.T) {
	if _, err := CanonicalManifestBytes(nil); err == nil {
		t.Fatal("CanonicalManifestBytes(nil) error = nil, want error")
	}

	manifest := validManifest()
	manifest.Artifacts = append(manifest.Artifacts, manifest.Artifacts[0])
	if _, err := CanonicalManifestBytes(manifest); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("CanonicalManifestBytes(duplicate tuple) error = %v, want duplicate error", err)
	}
}

func TestValidateManifestAcceptsDeclaredEnumsAndBoundaryRanges(t *testing.T) {
	channels := []Channel{ChannelPilot, ChannelBeta, ChannelStable, ChannelGA}
	operatingSystems := []OS{OSLinux, OSMacOS, OSWindows}
	architectures := []Architecture{ArchitectureAMD64, ArchitectureARM64}
	artifactTypes := []ArtifactType{ArtifactTypeDEB, ArtifactTypePKG, ArtifactTypeMSI, ArtifactTypePortable}
	signatures := []NativeSignatureStatus{NativeSignatureRequiredAndVerified, NativeSignatureAdHoc}

	for _, channel := range channels {
		for _, operatingSystem := range operatingSystems {
			for _, architecture := range architectures {
				for _, artifactType := range artifactTypes {
					for _, signature := range signatures {
						manifest := validManifest()
						manifest.Channel = channel
						manifest.RolloutPercentage = 0
						manifest.Artifacts[0].OS = operatingSystem
						manifest.Artifacts[0].Architecture = architecture
						manifest.Artifacts[0].Type = artifactType
						manifest.Artifacts[0].NativeSignature = signature
						if err := ValidateManifest(manifest); err != nil {
							t.Fatalf("ValidateManifest() rejected declared values: %v", err)
						}
					}
				}
			}
		}
	}
}

func TestValidateManifestNil(t *testing.T) {
	if err := ValidateManifest(nil); err == nil {
		t.Fatal("ValidateManifest(nil) error = nil, want error")
	}
}

func validManifest() *Manifest {
	publishedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	return &Manifest{
		SchemaVersion:           ManifestSchemaVersion,
		Channel:                 ChannelStable,
		Version:                 "1.2.3",
		GitSHA:                  strings.Repeat("a", 40),
		PublishedAt:             publishedAt,
		ExpiresAt:               publishedAt.Add(24 * time.Hour),
		ChannelEpoch:            1,
		MinimumSupportedVersion: "1.0.0",
		RolloutPercentage:       100,
		Compatibility: Compatibility{
			IPCMin:           1,
			IPCMax:           2,
			ServiceMin:       "1.0.0",
			UIMin:            "1.0.0",
			MigrationEpoch:   1,
			RollbackTo:       []string{"1.2.2"},
			ReauthOnRollback: false,
		},
		Artifacts: []Artifact{artifact(
			OSLinux,
			ArchitectureAMD64,
			ArtifactTypeDEB,
			"artifacts/wireztna_1.2.3_amd64.deb",
			"provenance/linux-amd64.intoto.jsonl",
			1024,
			"b",
		)},
	}
}

func canonicalManifestFixture() *Manifest {
	manifest := validManifest()
	zeroOffset := time.FixedZone("zero-offset", 0)
	manifest.PublishedAt = time.Date(2026, time.January, 2, 3, 4, 5, 123000000, zeroOffset)
	manifest.ExpiresAt = manifest.PublishedAt.Add(24 * time.Hour)
	manifest.Artifacts = []Artifact{
		artifact(OSWindows, ArchitectureAMD64, ArtifactTypeMSI, "artifacts/wireztna_1.2.3_amd64.msi", "provenance/windows-amd64.intoto.jsonl", 4096, "d"),
		artifact(OSLinux, ArchitectureARM64, ArtifactTypePortable, "artifacts/wireztna_1.2.3_arm64", "provenance/linux-arm64.intoto.jsonl", 2048, "c"),
		artifact(OSLinux, ArchitectureAMD64, ArtifactTypeDEB, "artifacts/wireztna_1.2.3_amd64.deb", "provenance/linux-amd64.intoto.jsonl", 1024, "b"),
	}
	return manifest
}

func artifact(operatingSystem OS, architecture Architecture, artifactType ArtifactType, path, provenancePath string, size int64, hashCharacter string) Artifact {
	return Artifact{
		OS:              operatingSystem,
		Architecture:    architecture,
		Type:            artifactType,
		URL:             immutableBaseURL() + path,
		Size:            size,
		SHA256:          strings.Repeat(hashCharacter, 64),
		NativeSignature: NativeSignatureRequiredAndVerified,
		ProvenanceURL:   immutableBaseURL() + provenancePath,
	}
}

func immutableBaseURL() string {
	return "https://downloads.example.test/clients/1.2.3/" + strings.Repeat("a", 40) + "/"
}

func cloneManifest(manifest *Manifest) *Manifest {
	clone := *manifest
	clone.Compatibility.RollbackTo = append([]string(nil), manifest.Compatibility.RollbackTo...)
	clone.Artifacts = append([]Artifact(nil), manifest.Artifacts...)
	return &clone
}

func manifestJSON(t *testing.T, manifest *Manifest) []byte {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	return data
}

func withJSONMutation(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
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

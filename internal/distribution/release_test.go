package distribution

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifyReleaseManifestSelectsCompleteHostSet(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testReleaseManifest()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, data)))

	verified, components, err := VerifyReleaseManifest(data, signature, publicKey, "linux", "amd64")
	if err != nil {
		t.Fatalf("VerifyReleaseManifest() error = %v", err)
	}
	if verified.Version != "0.2.0" || len(components) != 3 {
		t.Fatalf("verified release = %#v, components = %#v", verified, components)
	}
	for i, kind := range []string{PackKindCompiler, PackKindTarget, PackKindToolchain} {
		if components[i].Kind != kind {
			t.Fatalf("component %d kind = %q", i, components[i].Kind)
		}
	}
}

func TestVerifyReleaseManifestRejectsInvalidSignatureBeforeJSON(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = VerifyReleaseManifest([]byte(`{"schema_version":1}`), []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))), publicKey, "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("VerifyReleaseManifest() error = %v", err)
	}
}

func TestVerifyReleaseManifestRejectsIncompleteOrUnsupportedSet(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		mutate   func(*ReleaseManifest)
		hostOS   string
		hostArch string
		want     string
	}{
		{name: "unsupported", mutate: func(*ReleaseManifest) {}, hostOS: "darwin", hostArch: "arm64", want: "no release set"},
		{name: "missing kind", mutate: func(manifest *ReleaseManifest) {
			manifest.InstallSets[0].Components = manifest.InstallSets[0].Components[:2]
		}, hostOS: "linux", hostArch: "amd64", want: "requires exactly one"},
		{name: "duplicate kind", mutate: func(manifest *ReleaseManifest) { manifest.Components[2].Kind = PackKindTarget }, hostOS: "linux", hostArch: "amd64", want: "requires exactly one"},
		{name: "component mismatch", mutate: func(manifest *ReleaseManifest) { manifest.Components[2].Arch = "arm64" }, hostOS: "linux", hostArch: "amd64", want: "does not match release set"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := testReleaseManifest()
			test.mutate(&manifest)
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			signature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, data)))
			_, _, err = VerifyReleaseManifest(data, signature, publicKey, test.hostOS, test.hostArch)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyReleaseManifest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVerifyReleaseManifestRejectsUnknownFields(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"schema_version":1,"version":"0.2.0","components":[],"install_sets":[],"surprise":true}`)
	signature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, data)))
	_, _, err = VerifyReleaseManifest(data, signature, publicKey, "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("VerifyReleaseManifest() error = %v", err)
	}
}

func TestBuildReleaseManifestCreatesDeterministicCompleteHostSets(t *testing.T) {
	artifacts := completeReleaseArtifacts()
	manifest, err := BuildReleaseManifest("0.2.0", "https://github.com/PeeperLanguage/peeper/releases/download/v0.2.0", artifacts)
	if err != nil {
		t.Fatalf("BuildReleaseManifest() error = %v", err)
	}
	if len(manifest.Components) != 18 || len(manifest.InstallSets) != 6 {
		t.Fatalf("manifest has %d components and %d install sets", len(manifest.Components), len(manifest.InstallSets))
	}
	if manifest.InstallSets[0].OS != "darwin" || manifest.InstallSets[0].Arch != "amd64" {
		t.Fatalf("first install set = %#v", manifest.InstallSets[0])
	}
	if got := manifest.Components[0].URL; got != "https://github.com/PeeperLanguage/peeper/releases/download/v0.2.0/compiler-darwin-amd64.tar.gz" {
		t.Fatalf("first component URL = %q", got)
	}
}

func TestBuildReleaseManifestRejectsIncompleteHostSet(t *testing.T) {
	artifacts := completeReleaseArtifacts()
	_, err := BuildReleaseManifest("0.2.0", "https://example.com/v0.2.0", artifacts[:len(artifacts)-1])
	if err == nil || !strings.Contains(err.Error(), "windows/arm64") {
		t.Fatalf("BuildReleaseManifest() error = %v", err)
	}
}

func TestSignReleaseManifestSignsExactBytes(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("release bytes\n")
	encoded, err := SignReleaseManifest(data, privateKey)
	if err != nil {
		t.Fatalf("SignReleaseManifest() error = %v", err)
	}
	signature, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(publicKey, data, signature) {
		t.Fatal("signature does not verify exact release bytes")
	}
}

func testReleaseManifest() ReleaseManifest {
	digest := strings.Repeat("a", 64)
	return ReleaseManifest{
		SchemaVersion: ReleaseManifestVersion,
		Version:       "0.2.0",
		Components: []ReleaseComponent{
			{ID: "compiler-linux-amd64", Kind: PackKindCompiler, Version: "0.2.0", OS: "linux", Arch: "amd64", URL: "https://example.com/compiler.tar.gz", Size: 10, SHA256: digest, Format: FormatTarGz},
			{ID: "target-linux-amd64", Kind: PackKindTarget, Version: "0.2.0", OS: "linux", Arch: "amd64", URL: "https://example.com/target.tar.gz", Size: 20, SHA256: digest, Format: FormatTarGz},
			{ID: "toolchain-linux-amd64", Kind: PackKindToolchain, Version: "23.1.0", OS: "linux", Arch: "amd64", URL: "https://example.com/toolchain.tar.gz", Size: 30, SHA256: digest, Format: FormatTarGz},
		},
		InstallSets: []InstallSet{{OS: "linux", Arch: "amd64", Components: []string{"compiler-linux-amd64", "target-linux-amd64", "toolchain-linux-amd64"}}},
	}
}

func completeReleaseArtifacts() []ReleaseArtifact {
	digest := strings.Repeat("b", 64)
	artifacts := make([]ReleaseArtifact, 0, 18)
	for _, host := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		format := FormatTarGz
		if host[0] == "windows" {
			format = FormatZip
		}
		for _, kind := range []string{PackKindCompiler, PackKindTarget, PackKindToolchain} {
			id := kind + "-" + host[0] + "-" + host[1]
			artifacts = append(artifacts, ReleaseArtifact{
				FileName: id + format.Extension(),
				Manifest: Manifest{SchemaVersion: PackManifestVersion, Metadata: Metadata{Kind: kind, ID: id, Version: "0.2.0", OS: host[0], Arch: host[1]}, Format: format, Size: 10, SHA256: digest},
			})
		}
	}
	return artifacts
}

package distribution

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"
)

const (
	ReleaseManifestVersion = 1
	PackKindCompiler       = "compiler"
	PackKindTarget         = "target"
	PackKindToolchain      = "toolchain"
)

type ReleaseComponent struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	URL     string `json:"url"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Format  Format `json:"format"`
}

type InstallSet struct {
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	Components []string `json:"components"`
}

type ReleaseManifest struct {
	SchemaVersion int                `json:"schema_version"`
	Version       string             `json:"version"`
	Components    []ReleaseComponent `json:"components"`
	InstallSets   []InstallSet       `json:"install_sets"`
}

type ReleaseArtifact struct {
	FileName string
	Manifest Manifest
}

type releaseHost struct {
	os   string
	arch string
}

var supportedReleaseHosts = [...]releaseHost{
	{os: "darwin", arch: "amd64"},
	{os: "darwin", arch: "arm64"},
	{os: "linux", arch: "amd64"},
	{os: "linux", arch: "arm64"},
	{os: "windows", arch: "amd64"},
	{os: "windows", arch: "arm64"},
}

func BuildReleaseManifest(version, baseURL string, artifacts []ReleaseArtifact) (ReleaseManifest, error) {
	if strings.TrimSpace(version) == "" {
		return ReleaseManifest{}, fmt.Errorf("release manifest has no version")
	}
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil || parsedBaseURL.Scheme != "https" || parsedBaseURL.Host == "" || parsedBaseURL.User != nil || parsedBaseURL.RawQuery != "" || parsedBaseURL.Fragment != "" {
		return ReleaseManifest{}, fmt.Errorf("release base URL is unsafe")
	}
	expectedHosts := make(map[releaseHost]bool, len(supportedReleaseHosts))
	for _, host := range supportedReleaseHosts {
		expectedHosts[host] = true
	}
	manifest := ReleaseManifest{SchemaVersion: ReleaseManifestVersion, Version: version, Components: make([]ReleaseComponent, 0, len(artifacts))}
	for _, artifact := range artifacts {
		pack := artifact.Manifest
		if pack.SchemaVersion != PackManifestVersion {
			return ReleaseManifest{}, fmt.Errorf("artifact %q has unsupported pack schema %d", artifact.FileName, pack.SchemaVersion)
		}
		if artifact.FileName == "" || path.Base(artifact.FileName) != artifact.FileName || !strings.HasSuffix(artifact.FileName, pack.Format.Extension()) {
			return ReleaseManifest{}, fmt.Errorf("artifact %q has unsafe or mismatched filename", artifact.FileName)
		}
		host := releaseHost{os: pack.Metadata.OS, arch: pack.Metadata.Arch}
		if !expectedHosts[host] {
			return ReleaseManifest{}, fmt.Errorf("artifact %q has unsupported host %s/%s", artifact.FileName, host.os, host.arch)
		}
		artifactURL, err := url.JoinPath(baseURL, artifact.FileName)
		if err != nil {
			return ReleaseManifest{}, fmt.Errorf("build URL for artifact %q: %w", artifact.FileName, err)
		}
		manifest.Components = append(manifest.Components, ReleaseComponent{
			ID: pack.Metadata.ID, Kind: pack.Metadata.Kind, Version: pack.Metadata.Version,
			OS: host.os, Arch: host.arch, URL: artifactURL, Size: pack.Size, SHA256: pack.SHA256, Format: pack.Format,
		})
	}
	sort.Slice(manifest.Components, func(i, j int) bool {
		left, right := manifest.Components[i], manifest.Components[j]
		if left.OS != right.OS {
			return left.OS < right.OS
		}
		if left.Arch != right.Arch {
			return left.Arch < right.Arch
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.ID < right.ID
	})
	componentsByHost := make(map[releaseHost][]string, len(supportedReleaseHosts))
	for _, component := range manifest.Components {
		host := releaseHost{os: component.OS, arch: component.Arch}
		componentsByHost[host] = append(componentsByHost[host], component.ID)
	}
	manifest.InstallSets = make([]InstallSet, 0, len(supportedReleaseHosts))
	for _, host := range supportedReleaseHosts {
		componentIDs := componentsByHost[host]
		if len(componentIDs) != 3 {
			return ReleaseManifest{}, fmt.Errorf("release requires exactly three components for %s/%s", host.os, host.arch)
		}
		manifest.InstallSets = append(manifest.InstallSets, InstallSet{OS: host.os, Arch: host.arch, Components: componentIDs})
	}
	if _, err := validateReleaseManifest(manifest); err != nil {
		return ReleaseManifest{}, err
	}
	return manifest, nil
}

func SignReleaseManifest(data, privateKey []byte) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("release private key has invalid length")
	}
	encoded := make([]byte, base64.StdEncoding.EncodedLen(ed25519.SignatureSize))
	base64.StdEncoding.Encode(encoded, ed25519.Sign(ed25519.PrivateKey(privateKey), data))
	return encoded, nil
}

func VerifyReleaseManifest(data, encodedSignature, publicKey []byte, hostOS, hostArch string) (ReleaseManifest, []ReleaseComponent, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return ReleaseManifest{}, nil, fmt.Errorf("release public key has invalid length")
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encodedSignature)))
	if err != nil {
		return ReleaseManifest{}, nil, fmt.Errorf("decode release signature: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), data, signature) {
		return ReleaseManifest{}, nil, fmt.Errorf("release manifest signature is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest ReleaseManifest
	if err := decoder.Decode(&manifest); err != nil {
		return ReleaseManifest{}, nil, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ReleaseManifest{}, nil, fmt.Errorf("decode release manifest: trailing JSON data")
	}
	installSets, err := validateReleaseManifest(manifest)
	if err != nil {
		return ReleaseManifest{}, nil, err
	}
	selectedComponents, ok := installSets[releaseHost{os: hostOS, arch: hostArch}]
	if !ok {
		return ReleaseManifest{}, nil, fmt.Errorf("no release set for %s/%s", hostOS, hostArch)
	}
	return manifest, selectedComponents, nil
}

func validateReleaseManifest(manifest ReleaseManifest) (map[releaseHost][]ReleaseComponent, error) {
	if manifest.SchemaVersion != ReleaseManifestVersion {
		return nil, fmt.Errorf("unsupported release manifest schema %d", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return nil, fmt.Errorf("release manifest has no version")
	}
	components := make(map[string]ReleaseComponent, len(manifest.Components))
	for _, component := range manifest.Components {
		if err := validateReleaseComponent(component); err != nil {
			return nil, err
		}
		if _, exists := components[component.ID]; exists {
			return nil, fmt.Errorf("release manifest repeats component %q", component.ID)
		}
		components[component.ID] = component
	}
	installSets := make(map[releaseHost][]ReleaseComponent, len(manifest.InstallSets))
	referenced := make(map[string]bool, len(components))
	for _, installSet := range manifest.InstallSets {
		host := releaseHost{os: installSet.OS, arch: installSet.Arch}
		if _, exists := installSets[host]; exists {
			return nil, fmt.Errorf("release manifest repeats install set for %s/%s", host.os, host.arch)
		}
		byKind := make(map[string]ReleaseComponent, 3)
		for _, componentID := range installSet.Components {
			component, ok := components[componentID]
			if !ok {
				return nil, fmt.Errorf("release set references unknown component %q", componentID)
			}
			if referenced[componentID] {
				return nil, fmt.Errorf("release component %q belongs to multiple install sets", componentID)
			}
			if component.OS != host.os || component.Arch != host.arch {
				return nil, fmt.Errorf("component %q does not match release set %s/%s", component.ID, host.os, host.arch)
			}
			if _, exists := byKind[component.Kind]; exists {
				return nil, fmt.Errorf("release set requires exactly one %s component", component.Kind)
			}
			byKind[component.Kind] = component
			referenced[componentID] = true
		}
		ordered := make([]ReleaseComponent, 0, 3)
		for _, kind := range []string{PackKindCompiler, PackKindTarget, PackKindToolchain} {
			component, ok := byKind[kind]
			if !ok {
				return nil, fmt.Errorf("release set requires exactly one %s component", kind)
			}
			ordered = append(ordered, component)
		}
		if len(installSet.Components) != len(ordered) {
			return nil, fmt.Errorf("release set contains unsupported component kind")
		}
		installSets[host] = ordered
	}
	for componentID := range components {
		if !referenced[componentID] {
			return nil, fmt.Errorf("release component %q belongs to no install set", componentID)
		}
	}
	return installSets, nil
}

func validateReleaseComponent(component ReleaseComponent) error {
	fields := [][2]string{{"id", component.ID}, {"kind", component.Kind}, {"version", component.Version}, {"os", component.OS}, {"arch", component.Arch}, {"url", component.URL}, {"sha256", component.SHA256}}
	for _, field := range fields {
		if strings.TrimSpace(field[1]) == "" {
			return fmt.Errorf("release component %q has no %s", component.ID, field[0])
		}
	}
	if component.Kind != PackKindCompiler && component.Kind != PackKindTarget && component.Kind != PackKindToolchain {
		return fmt.Errorf("release component %q has unsupported kind %q", component.ID, component.Kind)
	}
	parsedURL, err := url.Parse(component.URL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil {
		return fmt.Errorf("release component %q has unsafe URL", component.ID)
	}
	if component.Size <= 0 {
		return fmt.Errorf("release component %q has invalid size", component.ID)
	}
	digest, err := hex.DecodeString(component.SHA256)
	if err != nil || len(digest) != 32 {
		return fmt.Errorf("release component %q has invalid SHA-256", component.ID)
	}
	if component.Format.Extension() == "" {
		return fmt.Errorf("release component %q has unsupported format %q", component.ID, component.Format)
	}
	return nil
}

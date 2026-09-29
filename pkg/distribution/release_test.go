package distribution

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseHostsMatchToolchainPlannerOutputs(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is required to exercise the toolchain planner")
	}

	repositoryRoot := filepath.Clean(filepath.Join("..", ".."))
	outputPath := filepath.Join(t.TempDir(), "planner-output")
	command := exec.Command(bash, "scripts/plan-toolchains.sh")
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(),
		"FORCE_ALL=true",
		"TOOLCHAIN_FAMILY=auto",
		"GITHUB_OUTPUT="+outputPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("plan all toolchains: %v\n%s", err, output)
	}

	file, err := os.Open(outputPath)
	if err != nil {
		t.Fatalf("open planner output: %v", err)
	}
	defer file.Close()

	planned := make(map[releaseHost]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok || value != "true" {
			t.Fatalf("unexpected planner output %q", scanner.Text())
		}
		osName, arch, ok := strings.Cut(key, "_")
		if !ok || osName == "" || arch == "" {
			t.Fatalf("invalid planner target %q", key)
		}
		host := releaseHost{os: osName, arch: arch}
		if planned[host] {
			t.Fatalf("planner repeated target %s/%s", host.os, host.arch)
		}
		planned[host] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read planner output: %v", err)
	}

	if len(planned) != len(supportedReleaseHosts) {
		t.Fatalf("planner emitted %d targets, release manifest supports %d", len(planned), len(supportedReleaseHosts))
	}
	for _, host := range supportedReleaseHosts {
		if !planned[host] {
			t.Fatalf("planner omits supported release host %s/%s", host.os, host.arch)
		}
	}
}

func TestReleaseHostsMatchWorkflowJobs(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(workflow)
	// Restrict matches to job headers, not target strings in shell snippets or comments.
	headers := regexp.MustCompile(`(?m)^  ([a-z][a-z0-9_]*):$`).FindAllStringSubmatchIndex(text, -1)
	jobs := make(map[releaseHost]bool)
	for i, header := range headers {
		name := text[header[2]:header[3]]
		target, isHost := strings.CutPrefix(name, "host_")
		if !isHost {
			continue
		}
		osName, arch, valid := strings.Cut(target, "_")
		if !valid || osName == "" || arch == "" {
			t.Fatalf("invalid release host job %q", name)
		}
		end := len(text)
		if i+1 < len(headers) {
			end = headers[i+1][0]
		}
		body := text[header[1]:end]
		if !strings.Contains(body, "\n    uses: ./.github/workflows/release-host.yml\n") ||
			!strings.Contains(body, "\n      os: "+osName+"\n") ||
			!strings.Contains(body, "\n      arch: "+arch+"\n") {
			t.Fatalf("release host job %q has mismatched workflow or os/arch inputs", name)
		}
		host := releaseHost{os: osName, arch: arch}
		if jobs[host] {
			t.Fatalf("workflow repeats release host %s/%s", host.os, host.arch)
		}
		jobs[host] = true
	}
	if len(jobs) != len(supportedReleaseHosts) {
		t.Fatalf("workflow schedules %d release hosts, Go supports %d", len(jobs), len(supportedReleaseHosts))
	}
	for _, host := range supportedReleaseHosts {
		if !jobs[host] {
			t.Fatalf("workflow omits supported release host %s/%s", host.os, host.arch)
		}
	}
}

func TestBuildReleaseManifestCreatesDeterministicCompleteHostSets(t *testing.T) {
	artifacts := completeReleaseArtifacts()
	toolchains := artifactsByKind(artifacts, PackKindToolchain)
	artifacts = artifactsWithoutKind(artifacts, PackKindToolchain)
	manifest, err := BuildReleaseManifest("0.2.0", "https://github.com/PeeperLanguage/peeper/releases/download/v0.2.0", artifacts, toolchains)
	if err != nil {
		t.Fatalf("BuildReleaseManifest() error = %v", err)
	}
	if len(manifest.Components) != 12 || len(manifest.InstallSets) != 6 {
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
	toolchains := artifactsByKind(artifacts, PackKindToolchain)
	artifacts = artifactsWithoutKind(artifacts, PackKindToolchain)
	_, err := BuildReleaseManifest("0.2.0", "https://example.com/v0.2.0", artifacts[:len(artifacts)-1], toolchains)
	if err == nil || !strings.Contains(err.Error(), "windows/arm64") {
		t.Fatalf("BuildReleaseManifest() error = %v", err)
	}
}

func TestBuildReleaseManifestKeepsExternalToolchainVersionAndURL(t *testing.T) {
	artifacts := completeReleaseArtifacts()
	toolchains := artifactsByKind(artifacts, PackKindToolchain)
	artifacts = artifactsWithoutKind(artifacts, PackKindToolchain)
	toolchains[0].Version = "llvm23.1.0-rabc123"
	toolchains[0].URL = "https://github.com/PeeperLanguage/compiler/releases/download/toolchain-darwin-amd64-abc123/toolchain-darwin-amd64.tar.gz"

	manifest, err := BuildReleaseManifest("0.2.0", "https://example.com/v0.2.0", artifacts, toolchains)
	if err != nil {
		t.Fatalf("BuildReleaseManifest() error = %v", err)
	}
	var external ReleaseComponent
	for _, component := range manifest.Components {
		if component.ID == toolchains[0].ID {
			external = component
			break
		}
	}
	if external.Version != "llvm23.1.0-rabc123" || external.URL != toolchains[0].URL {
		t.Fatalf("external toolchain changed: %#v", external)
	}
}

func completeReleaseArtifacts() []ReleaseArtifact {
	digest := strings.Repeat("b", 64)
	artifacts := make([]ReleaseArtifact, 0, 12)
	for _, host := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		format := FormatTarGz
		if host[0] == "windows" {
			format = FormatZip
		}
		for _, kind := range []string{PackKindCompiler, PackKindToolchain} {
			id := kind + "-" + host[0] + "-" + host[1]
			artifacts = append(artifacts, ReleaseArtifact{
				FileName: id + format.Extension(),
				Manifest: Manifest{SchemaVersion: PackManifestVersion, Metadata: Metadata{Kind: kind, ID: id, Version: "0.2.0", OS: host[0], Arch: host[1]}, Format: format, Size: 10, SHA256: digest},
			})
		}
	}
	return artifacts
}

func artifactsByKind(artifacts []ReleaseArtifact, kind string) []ReleaseComponent {
	components := make([]ReleaseComponent, 0, len(supportedReleaseHosts))
	for _, artifact := range artifacts {
		if artifact.Manifest.Metadata.Kind != kind {
			continue
		}
		components = append(components, ReleaseComponent{
			ID: artifact.Manifest.Metadata.ID, Kind: kind, Version: "llvm23.1.0-rfixture",
			OS: artifact.Manifest.Metadata.OS, Arch: artifact.Manifest.Metadata.Arch,
			URL:  "https://example.com/toolchains/" + artifact.FileName,
			Size: artifact.Manifest.Size, SHA256: artifact.Manifest.SHA256, Format: artifact.Manifest.Format,
		})
	}
	return components
}

func artifactsWithoutKind(artifacts []ReleaseArtifact, kind string) []ReleaseArtifact {
	filtered := make([]ReleaseArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Manifest.Metadata.Kind != kind {
			filtered = append(filtered, artifact)
		}
	}
	return filtered
}

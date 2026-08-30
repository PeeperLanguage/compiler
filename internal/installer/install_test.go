package installer

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"compiler/internal/target"
	"compiler/pkg/distribution"
)

func TestInstallDownloadsVerifiesAndActivatesRelease(t *testing.T) {
	fixture := newReleaseFixture(t)
	defer fixture.server.Close()
	installRoot := filepath.Join(t.TempDir(), "peeper")

	result, err := Install(t.Context(), Config{
		Client: fixture.server.Client(), ManifestURL: fixture.server.URL + "/release-manifest.json", SignatureURL: fixture.server.URL + "/release-manifest.json.sig",
		PublicKey: fixture.publicKey, HostOS: runtime.GOOS, HostArch: runtime.GOARCH, InstallRoot: installRoot,
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if result.Version != "0.2.0" || result.InstallRoot != installRoot {
		t.Fatalf("Install() result = %#v", result)
	}
	for _, relative := range []string{
		filepath.Join("bin", "peeper"+target.ExecutableExt(runtime.GOOS)),
		filepath.Join("libs", "core", "src", "global.peep"),
		filepath.Join("toolchains", "native", "profile.json"),
		filepath.Join("targets", target.Host().LLVMTriple, "lib", "libpeeper_rt_v1.a"),
	} {
		if _, err := os.Stat(filepath.Join(installRoot, relative)); err != nil {
			t.Fatalf("installed %s: %v", relative, err)
		}
	}
}

func TestInstallHashFailurePreservesExistingInstall(t *testing.T) {
	fixture := newReleaseFixture(t)
	defer fixture.server.Close()
	fixture.manifest.Components[0].SHA256 = hex.EncodeToString(make([]byte, sha256.Size))
	fixture.signManifest(t)
	installRoot := filepath.Join(t.TempDir(), "peeper")
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(installRoot, "existing")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Install(t.Context(), Config{
		Client: fixture.server.Client(), ManifestURL: fixture.server.URL + "/release-manifest.json", SignatureURL: fixture.server.URL + "/release-manifest.json.sig",
		PublicKey: fixture.publicKey, HostOS: runtime.GOOS, HostArch: runtime.GOARCH, InstallRoot: installRoot,
	})
	if err == nil {
		t.Fatal("Install() accepted component hash mismatch")
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil || string(data) != "keep" {
		t.Fatalf("prior install changed: data=%q err=%v", data, readErr)
	}
}

func TestInstallRejectsTruncatedComponent(t *testing.T) {
	fixture := newReleaseFixture(t)
	defer fixture.server.Close()
	fixture.responses["/compiler.tar.gz"] = fixture.responses["/compiler.tar.gz"][:10]
	installRoot := filepath.Join(t.TempDir(), "peeper")
	_, err := Install(t.Context(), Config{
		Client: fixture.server.Client(), ManifestURL: fixture.server.URL + "/release-manifest.json", SignatureURL: fixture.server.URL + "/release-manifest.json.sig",
		PublicKey: fixture.publicKey, HostOS: runtime.GOOS, HostArch: runtime.GOARCH, InstallRoot: installRoot,
	})
	if err == nil {
		t.Fatal("Install() accepted truncated component")
	}
	if _, statErr := os.Stat(installRoot); !os.IsNotExist(statErr) {
		t.Fatalf("failed install activated destination: %v", statErr)
	}
}

type releaseFixture struct {
	server     *httptest.Server
	responses  map[string][]byte
	manifest   distribution.ReleaseManifest
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
}

func newReleaseFixture(t *testing.T) *releaseFixture {
	t.Helper()
	fixture := &releaseFixture{responses: make(map[string][]byte)}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		data, ok := fixture.responses[request.URL.Path]
		if !ok {
			http.NotFound(response, request)
			return
		}
		_, _ = response.Write(data)
	}))
	fixture.publicKey, fixture.privateKey, _ = ed25519.GenerateKey(rand.Reader)
	host := target.Host()
	digest := func(data []byte) string {
		hash := sha256.Sum256(data)
		return hex.EncodeToString(hash[:])
	}
	components := []distribution.ReleaseComponent{
		fixture.writePack(t, "/compiler.tar.gz", distribution.Metadata{Kind: distribution.PackKindCompiler, ID: "compiler-host", Version: "0.2.0", OS: host.OS, Arch: host.Arch}, func(root string) {
			writeFixtureFile(t, filepath.Join(root, "bin", "peeper"+target.ExecutableExt(host.OS)), "compiler", 0o755)
			writeFixtureFile(t, filepath.Join(root, "libs", "core", "src", "global.peep"), "core", 0o644)
			writeFixtureFile(t, filepath.Join(root, "targets", host.LLVMTriple, "lib", "libpeeper_rt_v1.a"), "runtime", 0o644)
		}),
		fixture.writePack(t, "/toolchain.tar.gz", distribution.Metadata{Kind: distribution.PackKindToolchain, ID: "toolchain-host", Version: "23.1.0", OS: host.OS, Arch: host.Arch}, func(root string) {
			clang := filepath.Join("toolchains", "native", "bin", "clang"+target.ExecutableExt(host.OS))
			writeFixtureFile(t, filepath.Join(root, clang), "clang", 0o755)
			debugFormat := "dwarf"
			if host.OS == "windows" {
				debugFormat = "codeview"
			}
			profile := map[string]any{
				"schema_version": 1, "profile_id": "native-host", "target_os": host.OS, "target_arch": host.Arch, "llvm_triple": host.LLVMTriple,
				"clang_path": clang, "linker_path": clang, "runtime_archive": filepath.ToSlash(filepath.Join("targets", host.LLVMTriple, "lib", "libpeeper_rt_v1.a")),
				"runtime_abi": "peeper_rt_v1", "link_mode": "system", "debug_format": debugFormat,
			}
			if host.OS == "darwin" {
				profile["sdk_discovery"] = "xcrun"
				profile["minimum_os"] = "14.0"
			}
			data, err := json.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
			writeFixtureFile(t, filepath.Join(root, "toolchains", "native", "profile.json"), string(data), 0o644)
		}),
	}
	for i := range components {
		data := fixture.responses[[]string{"/compiler.tar.gz", "/toolchain.tar.gz"}[i]]
		components[i].URL = fixture.server.URL + []string{"/compiler.tar.gz", "/toolchain.tar.gz"}[i]
		components[i].Size = int64(len(data))
		components[i].SHA256 = digest(data)
		components[i].Format = distribution.FormatTarGz
	}
	fixture.manifest = distribution.ReleaseManifest{
		SchemaVersion: distribution.ReleaseManifestVersion, Version: "0.2.0", Components: components,
		InstallSets: []distribution.InstallSet{{OS: host.OS, Arch: host.Arch, Components: []string{"compiler-host", "toolchain-host"}}},
	}
	fixture.signManifest(t)
	return fixture
}

func (fixture *releaseFixture) writePack(t *testing.T, requestPath string, metadata distribution.Metadata, populate func(string)) distribution.ReleaseComponent {
	t.Helper()
	root := t.TempDir()
	populate(root)
	archivePath := filepath.Join(t.TempDir(), filepath.Base(requestPath))
	if _, err := distribution.WritePack(root, archivePath, distribution.FormatTarGz, metadata); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.responses[requestPath] = data
	return distribution.ReleaseComponent{ID: metadata.ID, Kind: metadata.Kind, Version: metadata.Version, OS: metadata.OS, Arch: metadata.Arch}
}

func (fixture *releaseFixture) signManifest(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(fixture.manifest)
	if err != nil {
		t.Fatal(err)
	}
	fixture.responses["/release-manifest.json"] = data
	fixture.responses["/release-manifest.json.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, data)))
}

func writeFixtureFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

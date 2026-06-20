package cli

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/pkg/manifest"
)

func TestInstallAllDependenciesRestoresMissingLockedCache(t *testing.T) {
	root := t.TempDir()
	mockRoot := filepath.Join(root, "mock")
	cachePath := manifest.CacheModulesDir(root)
	versionedModule := filepath.Join(cachePath, "github.com", "itsfuad", "peeper_test_lib@v0.0.1")
	staleModule := filepath.Join(cachePath, "github.com", "itsfuad", "peeper_test_lib@latest")

	mustWriteGetTest(t, filepath.Join(root, manifest.FileName), `name = "app"
build = "program"

[dependencies]
peeper_test_lib = "github.com/itsfuad/peeper_test_lib"

[dev]
mock_remote = true
mock_path = "./mock"
`)
	mustWriteGetTest(t, filepath.Join(root, manifest.LockfileName), `{
  "version": "1.0",
  "direct_deps": [
    "github.com/itsfuad/peeper_test_lib"
  ],
  "dependencies": {
    "github.com/itsfuad/peeper_test_lib": {
      "version": "v0.0.1",
      "resolved_url": "github.com/itsfuad/peeper_test_lib",
      "direct": true
    }
  }
}`)
	mustWriteGetTest(t, filepath.Join(mockRoot, "itsfuad", "peeper_test_lib-v0.0.1", manifest.FileName), `name = "peeper_test_lib"
build = "lib"
`)
	mustWriteGetTest(t, filepath.Join(staleModule, manifest.FileName), `name = "stale"
build = "lib"
`)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if chdirErr := os.Chdir(wd); chdirErr != nil {
			t.Fatal(chdirErr)
		}
	}()

	if err := installAllDependencies(); err != nil {
		t.Fatalf("installAllDependencies: %v", err)
	}
	if _, err := os.Stat(filepath.Join(versionedModule, manifest.FileName)); err != nil {
		t.Fatalf("expected restored versioned cache: %v", err)
	}
	loadedManifest, err := manifest.Load(filepath.Join(root, manifest.FileName))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if got := loadedManifest.Dependencies["peeper_test_lib"].Version; got != "v0.0.1" {
		t.Fatalf("expected dependency to be pinned to resolved version, got %q", got)
	}
}

func mustWriteGetTest(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

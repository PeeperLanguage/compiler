package cli

import (
	"bytes"
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

func TestPrepareInstallContextPropagatesMalformedLockfile(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, manifest.FileName)
	lockPath := filepath.Join(root, manifest.LockfileName)
	mustWriteGetTest(t, manifestPath, "name = \"app\"\nbuild = \"program\"\n")
	malformed := []byte("{not json")
	mustWriteGetTest(t, lockPath, string(malformed))
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
	if _, err := prepareInstallContext(); err == nil {
		t.Fatal("malformed lockfile was replaced with empty state")
	}
	after, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, malformed) {
		t.Fatalf("malformed lockfile changed: %q", after)
	}
}

func TestGetMultiplePackagesRollsBackDurableStateOnAnyFailure(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, manifest.FileName)
	lockPath := filepath.Join(root, manifest.LockfileName)
	mustWriteGetTest(t, manifestPath, `name = "app"
build = "program"

[dev]
mock_remote = true
mock_path = "./mock"
`)
	if err := manifest.SaveLockfile(root, manifest.NewLockfile()); err != nil {
		t.Fatal(err)
	}
	mustWriteGetTest(t, filepath.Join(root, "mock", "acme", "good-v1.0.0", manifest.FileName), "name = \"good\"\nbuild = \"lib\"\n")
	manifestBefore, _ := os.ReadFile(manifestPath)
	lockBefore, _ := os.ReadFile(lockPath)
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
	err = GetCommand([]string{"github.com/acme/good@v1.0.0", "github.com/acme/missing@v1.0.0"})
	if err == nil {
		t.Fatal("multi-package get succeeded despite missing package")
	}
	manifestAfter, _ := os.ReadFile(manifestPath)
	lockAfter, _ := os.ReadFile(lockPath)
	if !bytes.Equal(manifestAfter, manifestBefore) || !bytes.Equal(lockAfter, lockBefore) {
		t.Fatal("failed multi-package get changed durable dependency state")
	}
	cache := filepath.Join(manifest.CacheModulesDir(root), "github.com", "acme", "good@v1.0.0", manifest.FileName)
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("safe downloaded orphan cache missing: %v", err)
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

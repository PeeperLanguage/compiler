package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"compiler/pkg/manifest"
	"compiler/pkg/registry"
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
	lock, err := manifest.LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := lock.GetDependency("github.com/itsfuad/peeper_test_lib@v0.0.1")
	if !ok || entry.Checksum == "" {
		t.Fatalf("legacy dependency was not checksum-pinned: %#v", entry)
	}
}

func TestInstallDependencyPinsReusesAndRepairsCache(t *testing.T) {
	root := t.TempDir()
	mockPackage := filepath.Join(root, "mock", "acme", "pkg-v1.0.0")
	cachePackage := filepath.Join(manifest.CacheModulesDir(root), "github.com", "acme", "pkg@v1.0.0")
	mustWriteGetTest(t, filepath.Join(root, manifest.FileName), `name = "app"
build = "program"

[dependencies]
pkg = "github.com/acme/pkg"

[dev]
mock_remote = true
mock_path = "./mock"
`)
	mustWriteGetTest(t, filepath.Join(mockPackage, manifest.FileName), "name = \"pkg\"\nbuild = \"lib\"\n")
	mustWriteGetTest(t, filepath.Join(mockPackage, "src", "pkg.peep"), "original")
	mustWriteGetTest(t, filepath.Join(cachePackage, manifest.FileName), "name = \"stale\"\nbuild = \"lib\"\n")
	mustWriteGetTest(t, filepath.Join(cachePackage, "src", "pkg.peep"), "unlocked-cache")
	chdirForTest(t, root)

	if err := installAllDependencies(); err != nil {
		t.Fatal(err)
	}
	lock, err := manifest.LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := lock.GetDependency("github.com/acme/pkg@v1.0.0")
	if !ok || entry.Checksum == "" {
		t.Fatalf("initial install entry = %#v", entry)
	}
	if checksum, err := registry.ModuleChecksum(cachePackage); err != nil || checksum != entry.Checksum {
		t.Fatalf("cache checksum = %q, err=%v, want %q", checksum, err, entry.Checksum)
	}
	if data, err := os.ReadFile(filepath.Join(cachePackage, "src", "pkg.peep")); err != nil || string(data) != "original" {
		t.Fatalf("new resolution reused unpinned cache: %q, err=%v", data, err)
	}

	offlineMock := mockPackage + ".offline"
	if err := os.Rename(mockPackage, offlineMock); err != nil {
		t.Fatal(err)
	}
	if err := installAllDependencies(); err != nil {
		t.Fatalf("valid cache triggered refetch: %v", err)
	}
	if err := os.Rename(offlineMock, mockPackage); err != nil {
		t.Fatal(err)
	}

	cacheSource := filepath.Join(cachePackage, "src", "pkg.peep")
	if err := os.WriteFile(cacheSource, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installAllDependencies(); err != nil {
		t.Fatalf("tampered cache was not repaired: %v", err)
	}
	if data, err := os.ReadFile(cacheSource); err != nil || string(data) != "original" {
		t.Fatalf("repaired source = %q, err=%v", data, err)
	}

	if err := os.WriteFile(cacheSource, []byte("tampered-again"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mockPackage, "src", "pkg.peep"), []byte("moved-tag"), 0o644); err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.ReadFile(filepath.Join(root, manifest.LockfileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := installAllDependencies(); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("moved tag error = %v", err)
	}
	lockAfter, err := os.ReadFile(filepath.Join(root, manifest.LockfileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(lockAfter, lockBefore) {
		t.Fatal("moved tag changed lockfile")
	}
	if data, err := os.ReadFile(cacheSource); err != nil || string(data) != "tampered-again" {
		t.Fatalf("moved tag replaced prior cache: %q, err=%v", data, err)
	}
}

func TestInstallDependencyPinsTransitivePackages(t *testing.T) {
	root := t.TempDir()
	mustWriteGetTest(t, filepath.Join(root, manifest.FileName), `name = "app"
build = "program"

[dependencies]
parent = "github.com/acme/parent"

[dev]
mock_remote = true
mock_path = "./mock"
`)
	mustWriteGetTest(t, filepath.Join(root, "mock", "acme", "parent-v1.0.0", manifest.FileName), `name = "parent"
build = "lib"

[dependencies]
child = "github.com/acme/child"
`)
	mustWriteGetTest(t, filepath.Join(root, "mock", "acme", "child-v1.0.0", manifest.FileName), "name = \"child\"\nbuild = \"lib\"\n")
	chdirForTest(t, root)

	if err := installAllDependencies(); err != nil {
		t.Fatal(err)
	}
	lock, err := manifest.LoadLockfile(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, packageID := range []string{"github.com/acme/parent@v1.0.0", "github.com/acme/child@v1.0.0"} {
		entry, ok := lock.GetDependency(packageID)
		if !ok || entry.Checksum == "" {
			t.Fatalf("package %s entry = %#v", packageID, entry)
		}
	}
}

func TestLegacyChecksumMigrationRequiresMatchingRemote(t *testing.T) {
	tests := []struct {
		name          string
		remoteContent string
		wantError     bool
	}{
		{name: "matching", remoteContent: "cached"},
		{name: "disagreement", remoteContent: "moved", wantError: true},
		{name: "offline", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			cachePackage := filepath.Join(manifest.CacheModulesDir(root), "github.com", "acme", "pkg@v1.0.0")
			mustWriteGetTest(t, filepath.Join(root, manifest.FileName), `name = "app"
build = "program"

[dependencies]
pkg = "github.com/acme/pkg@v1.0.0"

[dev]
mock_remote = true
mock_path = "./mock"
`)
			mustWriteGetTest(t, filepath.Join(cachePackage, manifest.FileName), "name = \"pkg\"\nbuild = \"lib\"\n")
			mustWriteGetTest(t, filepath.Join(cachePackage, "src", "pkg.peep"), "cached")
			if test.remoteContent != "" {
				mockPackage := filepath.Join(root, "mock", "acme", "pkg-v1.0.0")
				mustWriteGetTest(t, filepath.Join(mockPackage, manifest.FileName), "name = \"pkg\"\nbuild = \"lib\"\n")
				mustWriteGetTest(t, filepath.Join(mockPackage, "src", "pkg.peep"), test.remoteContent)
			}
			lock := manifest.NewLockfile()
			packageID := "github.com/acme/pkg@v1.0.0"
			lock.SetDependency(packageID, manifest.LockfileEntry{Version: "v1.0.0", ResolvedURL: "github.com/acme/pkg", Direct: true})
			lock.SetDirectDependency("pkg", packageID)
			if err := manifest.SaveLockfile(root, lock); err != nil {
				t.Fatal(err)
			}
			lockBefore, err := os.ReadFile(filepath.Join(root, manifest.LockfileName))
			if err != nil {
				t.Fatal(err)
			}
			cacheBefore, err := registry.ModuleChecksum(cachePackage)
			if err != nil {
				t.Fatal(err)
			}
			chdirForTest(t, root)

			err = installAllDependencies()
			if test.wantError {
				if err == nil {
					t.Fatal("legacy migration succeeded")
				}
				lockAfter, readErr := os.ReadFile(filepath.Join(root, manifest.LockfileName))
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !bytes.Equal(lockAfter, lockBefore) {
					t.Fatal("failed legacy migration changed lockfile")
				}
				cacheAfter, hashErr := registry.ModuleChecksum(cachePackage)
				if hashErr != nil || cacheAfter != cacheBefore {
					t.Fatalf("failed legacy migration changed cache: %q, err=%v", cacheAfter, hashErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			migrated, err := manifest.LoadLockfile(root)
			if err != nil {
				t.Fatal(err)
			}
			entry, ok := migrated.GetDependency(packageID)
			if !ok || entry.Checksum != cacheBefore {
				t.Fatalf("migrated entry = %#v, want checksum %q", entry, cacheBefore)
			}
		})
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

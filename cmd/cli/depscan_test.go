package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"compiler/pkg/manifest"
)

func TestUpdateCommandReturnsRegistryScanFailureWithoutSaving(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, manifest.FileName)
	lockPath := filepath.Join(root, manifest.LockfileName)
	mustWriteGetTest(t, manifestPath, `name = "app"
build = "program"

[dependencies]
missing = "github.com/acme/missing@v1.0.0"

[dev]
mock_remote = true
mock_path = "./mock"
`)
	lock := manifest.NewLockfile()
	packageID := "github.com/acme/missing@v1.0.0"
	lock.SetDependency(packageID, manifest.LockfileEntry{Version: "v1.0.0", ResolvedURL: "github.com/acme/missing", Direct: true})
	lock.SetDirectDependency("missing", packageID)
	if err := manifest.SaveLockfile(root, lock); err != nil {
		t.Fatal(err)
	}
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
	var updateErr error
	output := captureStdout(t, func() { updateErr = UpdateCommand(nil) })
	if updateErr == nil {
		t.Fatal("registry scan failure returned success")
	}
	if bytes.Contains([]byte(output), []byte("up to date")) {
		t.Fatalf("failed update printed success: %s", output)
	}
	manifestAfter, _ := os.ReadFile(manifestPath)
	lockAfter, _ := os.ReadFile(lockPath)
	if !bytes.Equal(manifestAfter, manifestBefore) || !bytes.Equal(lockAfter, lockBefore) {
		t.Fatal("failed update scan changed durable dependency state")
	}
}

func TestUpdateConstraint(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: "latest"},
		{in: "latest", want: "latest"},
		{in: "v0.0.1", want: "latest"},
		{in: "^0.2.0", want: "^0.2.0"},
		{in: ">=0.2.0", want: ">=0.2.0"},
	}
	for _, tt := range tests {
		if got := updateConstraint(tt.in); got != tt.want {
			t.Fatalf("updateConstraint(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestListOrphanCandidatesIncludesLockAndStaleCache(t *testing.T) {
	root := t.TempDir()
	cachePath := manifest.CacheModulesDir(root)
	if err := os.MkdirAll(cachePath, 0o755); err != nil {
		t.Fatal(err)
	}

	lock := manifest.NewLockfile()
	lock.SetDependency("github.com/acme/used@v1.0.0", manifest.LockfileEntry{
		Version:     "v1.0.0",
		ResolvedURL: "github.com/acme/used",
		Direct:      true,
	})
	lock.SetDependency("github.com/acme/unused@v1.0.0", manifest.LockfileEntry{
		Version:     "v1.0.0",
		ResolvedURL: "github.com/acme/unused",
		Direct:      false,
	})
	lock.SetDirectDependency("used", "github.com/acme/used@v1.0.0")

	mustWriteGetTest(t, filepath.Join(cachePath, "github.com", "acme", "unused@v1.0.0", manifest.FileName), `name = "unused"
build = "lib"
`)
	mustWriteGetTest(t, filepath.Join(cachePath, "github.com", "acme", "stale@v9.9.9", manifest.FileName), `name = "stale"
build = "lib"
`)

	candidates, err := listOrphanCandidates(cachePath, lock)
	if err != nil {
		t.Fatalf("listOrphanCandidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 orphan candidates, got %d", len(candidates))
	}
	found := map[string]bool{}
	for _, candidate := range candidates {
		found[candidate.PackageID] = true
	}
	if !found["github.com/acme/unused@v1.0.0"] {
		t.Fatalf("expected unused lockfile package candidate, got %#v", candidates)
	}
	if !found["github.com/acme/stale@v9.9.9"] {
		t.Fatalf("expected stale cache candidate, got %#v", candidates)
	}
}

func TestListOrphanCandidatesRejectsInvalidLockIdentity(t *testing.T) {
	lock := manifest.NewLockfile()
	lock.SetDependency("../../outside@v1", manifest.LockfileEntry{
		Version:     "v1",
		ResolvedURL: "../../outside",
	})
	if _, err := listOrphanCandidates(t.TempDir(), lock); err == nil {
		t.Fatal("invalid lock identity produced cleanup candidate")
	}
}

func TestPruneUnusedDependenciesCascadesAndPreservesShared(t *testing.T) {
	root := t.TempDir()
	cachePath := manifest.CacheModulesDir(root)
	if err := os.MkdirAll(cachePath, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := manifest.NewLockfile()
	lock.SetDependency("github.com/acme/a@v1", manifest.LockfileEntry{
		Version:      "v1",
		ResolvedURL:  "github.com/acme/a",
		Direct:       false,
		Dependencies: []string{"github.com/acme/b@v1"},
	})
	lock.SetDependency("github.com/acme/e@v1", manifest.LockfileEntry{
		Version:      "v1",
		ResolvedURL:  "github.com/acme/e",
		Direct:       true,
		Dependencies: []string{"github.com/acme/b@v1"},
	})
	lock.SetDependency("github.com/acme/b@v1", manifest.LockfileEntry{
		Version:      "v1",
		ResolvedURL:  "github.com/acme/b",
		Direct:       false,
		Dependencies: []string{"github.com/acme/c@v1"},
		UsedBy:       []string{"github.com/acme/a@v1", "github.com/acme/e@v1"},
	})
	lock.SetDependency("github.com/acme/c@v1", manifest.LockfileEntry{
		Version:      "v1",
		ResolvedURL:  "github.com/acme/c",
		Direct:       false,
		Dependencies: []string{"github.com/acme/f@v1"},
		UsedBy:       []string{"github.com/acme/b@v1"},
	})
	lock.SetDependency("github.com/acme/f@v1", manifest.LockfileEntry{
		Version:     "v1",
		ResolvedURL: "github.com/acme/f",
		Direct:      false,
		UsedBy:      []string{"github.com/acme/c@v1"},
	})

	lock.RemoveDependency("github.com/acme/a@v1")
	removed, err := pruneUnusedDependencies(lock)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("expected shared branch to remain; removed=%#v", removed)
	}
	if _, ok := lock.GetDependency("github.com/acme/b@v1"); !ok {
		t.Fatalf("expected B to remain because E still uses it")
	}

	lock.RemoveDependency("github.com/acme/e@v1")
	removed, err = pruneUnusedDependencies(lock)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Fatalf("expected cascade removal of B/C/F, got %#v", removed)
	}
	if _, ok := lock.GetDependency("github.com/acme/b@v1"); ok {
		t.Fatal("expected B to be pruned")
	}
	if _, ok := lock.GetDependency("github.com/acme/c@v1"); ok {
		t.Fatal("expected C to be pruned")
	}
	if _, ok := lock.GetDependency("github.com/acme/f@v1"); ok {
		t.Fatal("expected F to be pruned")
	}
}

func TestCacheDeleteFailureLeavesPrunedMetadataAndRetryableCache(t *testing.T) {
	lock := manifest.NewLockfile()
	packageID := "github.com/acme/pkg@../../outside"
	lock.SetDependency(packageID, manifest.LockfileEntry{
		Version:     "../../outside",
		ResolvedURL: "github.com/acme/pkg",
	})

	removed, err := pruneUnusedDependencies(lock)
	if err != nil {
		t.Fatalf("prune metadata: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("pruned packages = %v, want one", removed)
	}
	if _, ok := lock.GetDependency(packageID); ok {
		t.Fatal("pruned metadata retained package")
	}
	if err := deletePrunedDependencies(t.TempDir(), removed); err == nil {
		t.Fatal("invalid cache deletion path succeeded")
	}
}

package xtest_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"compiler/internal/target"
	"compiler/internal/toolchain"
	"compiler/pkg/peeper"
)

func TestManagedObjectCacheFixture(t *testing.T) {
	binary := os.Getenv("PEEPER_BIN")
	if binary == "" {
		t.Skip("PEEPER_BIN not set; managed cache fixture requires bundled compiler")
	}
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Fatal(err)
	}
	tempRoot, err := os.MkdirTemp("/dev/shm", "peeper-link-fixture-")
	if err != nil {
		t.Skipf("/dev/shm unavailable: %v", err)
	}
	defer os.RemoveAll(tempRoot)
	projectRoot := t.TempDir()
	probe := filepath.Join(tempRoot, "probe.o")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(probe, filepath.Join(projectRoot, "probe.o")); err == nil {
		t.Skip("test project and /dev/shm share a filesystem")
	} else if !errors.Is(err, syscall.EXDEV) {
		t.Fatalf("cross-device probe = %v, want EXDEV", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(projectRoot, os.DirFS("managed_object_cache")); err != nil {
		t.Fatal(err)
	}
	installation := t.TempDir()
	profileDir := filepath.Join(installation, "toolchains", "native")
	for _, dir := range []string{filepath.Join(installation, "bin"), filepath.Join(profileDir, "link")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	installedBinary := filepath.Join(installation, "bin", "peeper")
	if err := os.WriteFile(installedBinary, data, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"libs", "runtime"} {
		if err := os.Symlink(filepath.Join(peeper.InstallationRootForExecutable(binary), dir), filepath.Join(installation, dir)); err != nil {
			t.Fatal(err)
		}
	}
	host := target.Host()
	systemTriple, err := target.SystemLLVMTriple(host.OS, host.Arch)
	if err != nil {
		t.Fatal(err)
	}
	// Installed profiles require the canonical musl triple, but this cache-only
	// fixture uses system Clang/libc. Select its host ABI so output does not depend
	// on a musl loader being installed on the runner. The final --target wins.
	// This tests real compilation, cross-filesystem caching, and execution with
	// the system ABI; it does not validate the managed musl ABI/toolchain.
	driver := "#!/bin/sh\nexec '" + strings.ReplaceAll(clang, "'", "'\"'\"'") + "' \"$@\" --target=" + systemTriple + "\n"
	for _, tool := range []string{"clang", filepath.Join("link", "clang")} {
		if err := os.WriteFile(filepath.Join(profileDir, tool), []byte(driver), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	profile := toolchain.Profile{
		SchemaVersion: toolchain.ProfileSchemaVersion, ProfileID: "cache-fixture",
		TargetOS: host.OS, TargetArch: host.Arch, LLVMTriple: host.LLVMTriple,
		ClangPath: "toolchains/native/clang", LinkerPath: "toolchains/native/link/clang",
		RuntimeArchive: "runtime/libpeeper_rt_v1.a", RuntimeABI: toolchain.RuntimeABIVersion,
		LinkMode: "system", DebugFormat: "dwarf",
	}
	data, err = json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "profile.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tempRoot)
	t.Setenv("CCACHE_DISABLE", "1")
	expectation := readFixtureExpectation(t, filepath.Join("managed_object_cache", "peeper.toml"))
	expectation.Dir = projectRoot
	runFixture(t, installedBinary, expectation)
	objects, err := filepath.Glob(filepath.Join(projectRoot, "build", "*", "*", "artifacts", "*", "*.o"))
	if err != nil || len(objects) == 0 {
		t.Fatalf("managed build did not publish objects: %v, %v", objects, err)
	}
	// Keep the profile/key and linker identical, but make object compilation
	// unavailable. A successful second build must consume published cache hits.
	if err := os.Remove(filepath.Join(profileDir, "clang")); err != nil {
		t.Fatal(err)
	}
	runFixture(t, installedBinary, expectation)
	entries, err := os.ReadDir(tempRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("link artifacts leaked: %v, %v", entries, err)
	}
	for _, object := range objects {
		entries, err := os.ReadDir(filepath.Dir(object))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				t.Fatalf("cache staging directory leaked: %s", entry.Name())
			}
		}
	}

	// A non-regular entry present before the next build must fail cache inspection,
	// rather than reach the compiler/linker or be removed as part of cache repair.
	if err := os.Remove(objects[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(objects[0], 0o755); err != nil {
		t.Fatal(err)
	}
	expectation.Mode = "build"
	expectation.Outcome = "failure"
	expectation.StderrContains = []string{"inspect cached object for", objects[0], "not a regular file"}
	runFixture(t, installedBinary, expectation)
	if info, err := os.Stat(objects[0]); err != nil || !info.IsDir() {
		t.Fatalf("cache inspection changed invalid entry: %v, %v", info, err)
	}
}

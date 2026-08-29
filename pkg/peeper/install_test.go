package peeper

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallationRootForExecutableResolvesBinParent(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(bin, "peeper")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := canonicalTestPath(t, root)
	if got := InstallationRootForExecutable(executable); got != want {
		t.Fatalf("InstallationRootForExecutable() = %q, want %q", got, want)
	}
}

func TestInstallationRootForExecutableResolvesSymlink(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(bin, "peeper")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "peeper")
	if err := os.Symlink(executable, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	want := canonicalTestPath(t, root)
	if got := InstallationRootForExecutable(link); got != want {
		t.Fatalf("InstallationRootForExecutable(symlink) = %q, want %q", got, want)
	}
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("canonicalize test path: %v", err)
	}
	return filepath.Clean(resolved)
}

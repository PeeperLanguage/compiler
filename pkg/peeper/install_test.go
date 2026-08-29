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
	if got := InstallationRootForExecutable(executable); got != root {
		t.Fatalf("InstallationRootForExecutable() = %q, want %q", got, root)
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
	if got := InstallationRootForExecutable(link); got != root {
		t.Fatalf("InstallationRootForExecutable(symlink) = %q, want %q", got, root)
	}
}

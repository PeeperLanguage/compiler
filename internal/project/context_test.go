package project

import (
	"path/filepath"
	"testing"

	"compiler/pkg/peeper"
)

func TestPackagedLibraryBaseForExecutableUsesSiblingLibsDir(t *testing.T) {
	exePath := filepath.Join("/tmp", "peeper", "build", "bin", "peeper")
	got := packagedLibraryBaseForExecutable(exePath)
	want := filepath.Join("/tmp", "peeper", "build", "libs")
	if got != want {
		t.Fatalf("packaged library base = %q, want %q", got, want)
	}
}

func TestLibraryRootUsesNamespaceSubdirectory(t *testing.T) {
	ctx := NewWithConfig(Config{
		RootDir:        t.TempDir(),
		Extension:      peeper.SourceExt,
		LibraryBaseDir: filepath.Join("/tmp", "peeper", "build", "libs"),
	}, nil)

	got, ok := ctx.LibraryRoot("vendor")
	if !ok {
		t.Fatal("LibraryRoot() returned no root")
	}
	want := filepath.Join("/tmp", "peeper", "build", "libs", "vendor")
	if got != want {
		t.Fatalf("LibraryRoot(vendor) = %q, want %q", got, want)
	}
}

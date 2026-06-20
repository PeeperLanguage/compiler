package project

import (
	"os"
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

func TestModuleOriginForFileDetectsBundledLibrarySource(t *testing.T) {
	root := t.TempDir()
	libraryBase := filepath.Join(root, "libs")
	libraryFile := filepath.Join(libraryBase, "core", peeper.SourceDirName, "global"+peeper.SourceExt)
	if err := os.MkdirAll(filepath.Dir(libraryFile), 0o755); err != nil {
		t.Fatalf("mkdir library dir: %v", err)
	}
	if err := os.WriteFile(libraryFile, []byte("const stdout: i32 = 1;\n"), 0o644); err != nil {
		t.Fatalf("write library file: %v", err)
	}

	ctx := NewWithConfig(Config{
		RootDir:        root,
		Extension:      peeper.SourceExt,
		LibraryBaseDir: libraryBase,
	}, nil)

	origin, namespace := ctx.ModuleOriginForFile(libraryFile)
	if origin != ModuleOriginStdlib {
		t.Fatalf("origin = %q, want %q", origin, ModuleOriginStdlib)
	}
	if namespace != "core" {
		t.Fatalf("namespace = %q, want %q", namespace, "core")
	}
}

func TestModuleOriginForFileLeavesProjectSourceLocal(t *testing.T) {
	root := t.TempDir()
	mainFile := filepath.Join(root, peeper.SourceDirName, "main"+peeper.SourceExt)
	if err := os.MkdirAll(filepath.Dir(mainFile), 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	if err := os.WriteFile(mainFile, []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	ctx := NewWithConfig(Config{
		RootDir:        root,
		ProjectName:    "app",
		Extension:      peeper.SourceExt,
		LibraryBaseDir: filepath.Join(root, "libs"),
	}, nil)

	origin, namespace := ctx.ModuleOriginForFile(mainFile)
	if origin != ModuleOriginLocal {
		t.Fatalf("origin = %q, want %q", origin, ModuleOriginLocal)
	}
	if namespace != "" {
		t.Fatalf("namespace = %q, want empty", namespace)
	}
}

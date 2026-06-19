package project

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/pkg/peeper"
)

func TestResolveImportPathUsesLibraryNamespaceRoots(t *testing.T) {
	root := t.TempDir()
	libraryBase := filepath.Join(root, "libs")
	libraryFile := filepath.Join(libraryBase, "vendor", "json"+peeper.SourceExt)
	if err := os.MkdirAll(filepath.Dir(libraryFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(libraryFile, []byte("fn Encode() -> i32 { return 0; }"), 0o644); err != nil {
		t.Fatalf("write library file: %v", err)
	}

	ctx := NewWithConfig(Config{
		RootDir:        root,
		Extension:      peeper.SourceExt,
		LibraryBaseDir: libraryBase,
	}, nil)

	resolved, err := ctx.ResolveImportPath(nil, "vendor:json")
	if err != nil {
		t.Fatalf("ResolveImportPath() error = %v", err)
	}
	if resolved.Namespace != "vendor" {
		t.Fatalf("resolved namespace = %q, want %q", resolved.Namespace, "vendor")
	}
	if resolved.FilePath != libraryFile {
		t.Fatalf("resolved file path = %q, want %q", resolved.FilePath, libraryFile)
	}
	if resolved.ImportPath != "json" {
		t.Fatalf("resolved import path = %q, want %q", resolved.ImportPath, "json")
	}
}

func TestResolveImportPathRequiresProjectConfigForLocalImports(t *testing.T) {
	root := t.TempDir()
	ctx := NewWithConfig(Config{
		RootDir:   root,
		Extension: peeper.SourceExt,
	}, nil)

	_, err := ctx.ResolveImportPath(nil, "app/util")
	if err == nil {
		t.Fatal("expected local import error without project config")
	}
	if got := err.Error(); got != "local imports require peeper.toml; run `peeper init` to create project config" {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestResolveImportPathStripsProjectPrefix(t *testing.T) {
	root := t.TempDir()
	utilPath := filepath.Join(root, "util"+peeper.SourceExt)
	if err := os.WriteFile(utilPath, []byte("fn Helper() -> i32 { return 0; }"), 0o644); err != nil {
		t.Fatalf("write util: %v", err)
	}

	ctx := NewWithConfig(Config{
		RootDir:     root,
		ProjectName: "app",
		Extension:   peeper.SourceExt,
	}, nil)

	resolved, err := ctx.ResolveImportPath(nil, "app/util")
	if err != nil {
		t.Fatalf("ResolveImportPath() error = %v", err)
	}
	if resolved.FilePath != utilPath {
		t.Fatalf("resolved file path = %q, want %q", resolved.FilePath, utilPath)
	}
	if resolved.ImportPath != "app/util" {
		t.Fatalf("resolved import path = %q, want %q", resolved.ImportPath, "app/util")
	}
}

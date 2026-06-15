package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveImportPathUsesLibraryNamespaceRoots(t *testing.T) {
	root := t.TempDir()
	libraryBase := filepath.Join(root, "libs")
	libraryFile := filepath.Join(libraryBase, "vendor", "json.peep")
	if err := os.MkdirAll(filepath.Dir(libraryFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(libraryFile, []byte("fn Encode() -> i32 { return 0; }"), 0o644); err != nil {
		t.Fatalf("write library file: %v", err)
	}

	ctx := NewWithConfig(Config{
		RootDir:        root,
		Extension:      ".peep",
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

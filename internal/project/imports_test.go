package project

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/internal/moduleid"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

func TestResolveImportPathUsesLibraryNamespaceRoots(t *testing.T) {
	root := t.TempDir()
	libraryBase := filepath.Join(root, "libs")
	libraryFile := filepath.Join(libraryBase, "vendor", peeper.SourceDirName, "json"+peeper.SourceExt)
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

	resolved, err := ctx.ResolveImportPath("vendor:json")
	if err != nil {
		t.Fatalf("ResolveImportPath() error = %v", err)
	}
	wantID := moduleid.ID{Origin: string(ModuleOriginStdlib), Namespace: "vendor", ImportPath: "json"}
	if resolved.ID != wantID {
		t.Fatalf("resolved ID = %#v, want %#v", resolved.ID, wantID)
	}
	if want := CanonicalPath(libraryFile); resolved.FilePath != want {
		t.Fatalf("resolved file path = %q, want %q", resolved.FilePath, want)
	}
}

func TestResolveImportPathRequiresProjectConfigForLocalImports(t *testing.T) {
	root := t.TempDir()
	ctx := NewWithConfig(Config{
		RootDir:   root,
		Extension: peeper.SourceExt,
	}, nil)

	_, err := ctx.ResolveImportPath("app/util")
	if err == nil {
		t.Fatal("expected local import error without project config")
	}
	if got := err.Error(); got != "local imports require "+manifest.FileName+"; run `peeper init` to create project config" {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestResolveImportPathStripsProjectPrefix(t *testing.T) {
	root := t.TempDir()
	utilPath := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	if err := os.MkdirAll(filepath.Dir(utilPath), 0o755); err != nil {
		t.Fatalf("mkdir util dir: %v", err)
	}
	if err := os.WriteFile(utilPath, []byte("fn Helper() -> i32 { return 0; }"), 0o644); err != nil {
		t.Fatalf("write util: %v", err)
	}

	ctx := NewWithConfig(Config{
		RootDir:     root,
		ProjectName: "app",
		Extension:   peeper.SourceExt,
	}, nil)

	resolved, err := ctx.ResolveImportPath("app/util")
	if err != nil {
		t.Fatalf("ResolveImportPath() error = %v", err)
	}
	wantID := moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "app/util"}
	if resolved.ID != wantID {
		t.Fatalf("resolved ID = %#v, want %#v", resolved.ID, wantID)
	}
	if want := CanonicalPath(utilPath); resolved.FilePath != want {
		t.Fatalf("resolved file path = %q, want %q", resolved.FilePath, want)
	}
}

func TestImportCandidatesEnumeratesRootsAndImmediateChildren(t *testing.T) {
	root := t.TempDir()
	libraryBase := filepath.Join(root, "libs")
	files := []string{
		filepath.Join(root, peeper.SourceDirName, "main"+peeper.SourceExt),
		filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt),
		filepath.Join(root, peeper.SourceDirName, "nested", "child"+peeper.SourceExt),
		filepath.Join(root, peeper.SourceDirName, ".hidden"+peeper.SourceExt),
		filepath.Join(root, peeper.SourceDirName, "notes.txt"),
		filepath.Join(libraryBase, "core", peeper.SourceDirName, "fmt"+peeper.SourceExt),
		filepath.Join(libraryBase, "vendor", peeper.SourceDirName, "json", "decode"+peeper.SourceExt),
	}
	for _, file := range files {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", file, err)
		}
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(libraryBase, ".hidden", peeper.SourceDirName), 0o755); err != nil {
		t.Fatalf("mkdir hidden library: %v", err)
	}

	ctx := NewWithConfig(Config{
		RootDir:        root,
		ProjectName:    "app",
		Extension:      peeper.SourceExt,
		LibraryBaseDir: libraryBase,
	}, nil)

	assertImportCandidates(t, ctx.ImportCandidates("", files[0]), []ImportCandidate{
		{ImportPath: "app/", Continuing: true},
		{ImportPath: "core:", Continuing: true},
		{ImportPath: "vendor:", Continuing: true},
	})
	assertImportCandidates(t, ctx.ImportCandidates("app/", files[0]), []ImportCandidate{
		{ImportPath: "app/nested/", Continuing: true},
		{ImportPath: "app/util", FilePath: CanonicalPath(files[1])},
	})
	assertImportCandidates(t, ctx.ImportCandidates("vendor:json/", files[0]), []ImportCandidate{
		{ImportPath: "vendor:json/decode", FilePath: CanonicalPath(files[6])},
	})
}

func TestImportCandidatesFiltersPartialRootsAndMissingNamespaces(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, peeper.SourceDirName), 0o755); err != nil {
		t.Fatalf("mkdir source root: %v", err)
	}
	ctx := NewWithConfig(Config{
		RootDir:     root,
		ProjectName: "app",
		LibraryRoots: map[string]string{
			"missing": filepath.Join(root, "missing"),
		},
	}, nil)

	assertImportCandidates(t, ctx.ImportCandidates("ap", ""), []ImportCandidate{{ImportPath: "app/", Continuing: true}})
	assertImportCandidates(t, ctx.ImportCandidates("missing:", ""), nil)
	assertImportCandidates(t, ctx.ImportCandidates("unknown:", ""), nil)
	assertImportCandidates(t, ctx.ImportCandidates("app/.hidden/", ""), nil)
}

func assertImportCandidates(t *testing.T, got, want []ImportCandidate) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ImportCandidates() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ImportCandidates()[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

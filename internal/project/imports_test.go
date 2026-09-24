package project

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/semantics/typeresolution"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

func TestQualifiedTypeQueryIsObservationalAndSourceResolutionPublishesUse(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	ctx := New(".", peeper.SourceExt, diag)
	dependencyID := moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "dep"}
	dependency := &module.Module{ID: dependencyID, ModuleScope: symbols.NewScope(nil)}
	target := symbols.New("Thing", symbols.SymbolType, nil, nil)
	target.IsPub = false
	target.BindType(&typeinfo.DefinedType{Name: "Thing", Identity: "dep::Thing", Kind: typeinfo.DefinedKindStruct})
	if err := dependency.ModuleScope.Declare(target); err != nil {
		t.Fatalf("declare imported type: %v", err)
	}
	module := &module.Module{
		ID:          moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "main"},
		ModuleScope: symbols.NewScope(nil),
		Bindings:    symbols.NewBindings(),
		Imports: map[string]module.ResolvedImport{
			"dep": {ID: dependencyID},
		},
	}
	alias := symbols.New("dep", symbols.SymbolImport, nil, nil)
	if err := module.ModuleScope.Declare(alias); err != nil {
		t.Fatalf("declare import alias: %v", err)
	}
	ctx.AddModule(dependency)
	ctx.AddModule(module)
	node := &ast.ScopeResolution{Segments: []ast.PathSegment{
		{Name: &ast.Ident{Name: "dep"}},
		{Name: &ast.Ident{Name: "Thing"}},
	}}

	ctx.TypeResolver.Query(module, node, typeresolution.Context{})
	ctx.TypeResolver.Resolve(ctx.Diagnostics, module, node, typeresolution.Context{})
	if alias.IsUsed() || target.IsUsed() || module.Bindings.Symbol(node) != nil {
		t.Fatal("private imported type published usage or binding")
	}

	target.IsPub = true
	if got := ctx.TypeResolver.Query(module, node, typeresolution.Context{}); got.Status != typeresolution.QueryAvailable || got.Type != target.Type {
		t.Fatalf("query result = %#v, want available imported type %#v", got, target.Type)
	}
	if alias.IsUsed() || target.IsUsed() {
		t.Fatal("qualified query published source usage")
	}
	if module.Bindings.Symbol(node) != nil {
		t.Fatal("qualified query published a source binding")
	}

	if got := ctx.TypeResolver.Resolve(ctx.Diagnostics, module, node, typeresolution.Context{}); got != target.Type {
		t.Fatalf("source type = %#v, want imported type %#v", got, target.Type)
	}
	if !alias.IsUsed() || !target.IsUsed() {
		t.Fatal("source resolution did not publish import alias and target usage")
	}
	if got := module.Bindings.Symbol(node); got != target {
		t.Fatalf("source binding = %#v, want imported target %#v", got, target)
	}
	if diag.HasErrors() {
		t.Fatalf("qualified type resolution emitted diagnostics: %s", diag.EmitAllToString())
	}
}

func TestQualifiedTypeResolutionDoesNotMarkInvalidQualifierUsed(t *testing.T) {
	ctx := New(".", peeper.SourceExt, diagnostics.NewDiagnosticBag())
	module := &module.Module{
		ID:          moduleid.ID{Origin: string(ModuleOriginLocal), ImportPath: "main"},
		ModuleScope: symbols.NewScope(nil),
		Imports:     make(map[string]module.ResolvedImport),
	}
	local := symbols.New("local", symbols.SymbolVar, nil, nil)
	if err := module.ModuleScope.Declare(local); err != nil {
		t.Fatalf("declare local symbol: %v", err)
	}
	node := &ast.ScopeResolution{Segments: []ast.PathSegment{
		{Name: &ast.Ident{Name: "local"}},
		{Name: &ast.Ident{Name: "Thing"}},
	}}

	ctx.TypeResolver.Resolve(ctx.Diagnostics, module, node, typeresolution.Context{})
	if local.IsUsed() {
		t.Fatal("invalid qualified type marked local qualifier used")
	}
}

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
		{ImportPath: "app/", CanContinue: true},
		{ImportPath: "core:", CanContinue: true},
		{ImportPath: "vendor:", CanContinue: true},
	})
	assertImportCandidates(t, ctx.ImportCandidates("app/", files[0]), []ImportCandidate{
		{ImportPath: "app/nested/", CanContinue: true},
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

	assertImportCandidates(t, ctx.ImportCandidates("ap", ""), []ImportCandidate{{ImportPath: "app/", CanContinue: true}})
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

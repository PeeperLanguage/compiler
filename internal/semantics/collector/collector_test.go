package collector

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/pkg/peeper"
)

func TestImportSymbolsKeepSourceLocation(t *testing.T) {
	const filePath = "collector_import_test" + peeper.SourceExt
	src := `import "external";

fn main() -> i32 {
	return 0;
}`

	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	if len(modAST.Imports) != 1 || modAST.Imports[0] == nil {
		t.Fatalf("expected one parsed import decl")
	}

	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "collector_import_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports: map[string]project.ResolvedImport{
			"external": {
				Key:        "local:external" + peeper.SourceExt,
				ImportPath: "external",
				FilePath:   "external" + peeper.SourceExt,
				Origin:     project.ModuleOriginLocal,
				Decl:       modAST.Imports[0],
			},
		},
	}

	Collect(ctx, module)

	sym, ok := module.ModuleScope.LookupLocal("external")
	if !ok || sym == nil {
		t.Fatalf("expected import symbol to be declared")
	}
	if sym.Location == nil {
		t.Fatalf("expected import symbol location to be preserved")
	}
}

func TestTargetOSDeclarationsStillCollide(t *testing.T) {
	const filePath = "collector_target_test" + peeper.SourceExt
	src := `#[target_os("linux")]
fn Platform() -> i32 {
	return 1;
}

#[target_os("darwin")]
fn Platform() -> i32 {
	return 2;
}
`
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt, TargetOS: "linux"}, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "collector_target_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]project.ResolvedImport),
	}

	Collect(ctx, module)

	if !diag.HasErrors() {
		t.Fatalf("expected redeclaration diagnostic")
	}
	if !strings.Contains(diag.EmitAllToString(), "Platform") {
		t.Fatalf("expected Platform redeclaration, got:\n%s", diag.EmitAllToString())
	}
}

func TestTargetOSImplMethodsStillCollide(t *testing.T) {
	const filePath = "collector_method_target_test" + peeper.SourceExt
	src := `struct Buffer {
	value: i32
}

impl Buffer {
	#[target_os("linux")]
	fn Platform(self: Self) -> i32 {
		return 1;
	}

	#[target_os("darwin")]
	fn Platform(self: Self) -> i32 {
		return 2;
	}
}
`
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt, TargetOS: "linux"}, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "collector_method_target_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]project.ResolvedImport),
	}

	Collect(ctx, module)

	if !diag.HasErrors() {
		t.Fatalf("expected redeclaration diagnostic")
	}
	if !strings.Contains(diag.EmitAllToString(), "method `Platform` already declared") {
		t.Fatalf("expected method redeclaration, got:\n%s", diag.EmitAllToString())
	}
}

package collector

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/peeper"
)

var _ func(*collector, ast.TypeDecl) = (*collector).collectConcreteTypeDecl
var _ func(*collector, *ast.Ident, symbols.Kind, ast.Node) = (*collector).collectModuleBinding

func TestCallableSymbolsKeepDefiningModuleKey(t *testing.T) {
	const filePath = "collector_callable_module_test" + peeper.SourceExt
	const src = `struct Counter { value: i32 }
fn Value() -> i32 { return 1; }
fn (self: Counter) Read() -> i32 { return self.value; }`
	diag := diagnostics.NewDiagnosticBag()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginDependency, filePath),
		ImportPath: "math/counter",
		FilePath:   filePath,
		Namespace:  "vendor",
		Origin:     project.ModuleOriginDependency,
		Dependency: "mathlib",
		Content:    src,
		AST:        parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx := project.New(".", peeper.SourceExt, diag)
	Collect(ctx, module)

	want := symbols.DefiningModuleKey{
		Origin:     string(project.ModuleOriginDependency),
		Namespace:  "vendor",
		Dependency: "mathlib",
		ImportPath: "math/counter",
	}
	function, ok := module.ModuleScope.LookupLocal("Value")
	if !ok || function == nil || function.DefiningModule != want {
		t.Fatalf("function defining module = %#v, want %#v", function, want)
	}
	methods := module.Semantics.MethodSets["Counter"]
	if len(methods) != 1 || methods[0] == nil || methods[0].DefiningModule != want {
		t.Fatalf("method defining module = %#v, want %#v", methods, want)
	}
}

func TestCollectedDefinedTypeKeepsDeclaringModuleIdentity(t *testing.T) {
	const filePath = "collector_type_identity_test" + peeper.SourceExt
	const src = `enum Status { Ready }`
	diag := diagnostics.NewDiagnosticBag()
	module := &project.Module{
		Key:      project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		FilePath: filePath,
		Content:  src,
		AST:      parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]project.ResolvedImport),
	}
	ctx := project.New(".", peeper.SourceExt, diag)
	Collect(ctx, module)

	sym, ok := module.ModuleScope.LookupLocal("Status")
	if !ok || sym == nil {
		t.Fatal("collected enum type missing")
	}
	defined, ok := sym.Type.(*typeinfo.DefinedType)
	if !ok || defined == nil {
		t.Fatalf("collected enum type = %T, want DefinedType", sym.Type)
	}
	want := module.Key + "::Status"
	if defined.Identity != want {
		t.Fatalf("collected enum identity = %q, want %q", defined.Identity, want)
	}
}

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

	#[target_os("linux")]
	fn (self: Buffer) Platform() -> i32 {
		return 1;
	}

	#[target_os("darwin")]
	fn (self: Buffer) Platform() -> i32 {
		return 2;
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

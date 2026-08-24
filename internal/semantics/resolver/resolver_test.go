package resolver

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/symbols"
	"compiler/pkg/peeper"
)

func checkResolveSource(t *testing.T, src string) (*project.Module, *diagnostics.DiagnosticBag) {
	t.Helper()
	const filePath = "resolver_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "resolver_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	Resolve(ctx, module)
	return module, diag
}

func TestUnresolvedIdentifierSuggestionPrefersNearestScope(t *testing.T) {
	src := `const for: i32 = 1;

fn main(foo: i32) -> i32 {
	return foa;
}`
	_, diag := checkResolveSource(t, src)
	out := diag.EmitAllToString()
	if !strings.Contains(out, "did you mean `foo`?") {
		t.Fatalf("expected nearest-scope suggestion, got:\n%s", out)
	}
	if strings.Contains(out, "did you mean `for`?") {
		t.Fatalf("unexpected outer-scope suggestion, got:\n%s", out)
	}
}

func TestResolveRejectsLexicalSelfInitialization(t *testing.T) {
	_, diag := checkResolveSource(t, `fn main() -> i32 {
	let value: i32 = value;
	return 0;
}`)
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrUseBeforeDecl {
			return
		}
	}
	t.Fatalf("expected use-before-declaration diagnostic:\n%s", diag.EmitAllToString())
}

func TestResolveEnumVariantPathsToChildSymbols(t *testing.T) {
	module, diag := checkResolveSource(t, `enum Result<T> {
	Ok: { value: T },
	Pending,
}
fn main() {
	let ok = Result<i32>::Ok{ value = 42 };
	let pending = Result<i32>::Pending;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[1].(*ast.FnDecl)
	okPath := fn.Body.Stmts[0].(*ast.LetDecl).Value.(*ast.VariantLit).Case
	pendingPath := fn.Body.Stmts[1].(*ast.LetDecl).Value.(*ast.ScopeResolution)
	for _, path := range []*ast.ScopeResolution{okPath, pendingPath} {
		sym := module.Semantics.ResolvedSymbols[path.ID()]
		if sym == nil {
			t.Fatalf("resolved %s = nil, want child variant symbol", path.TypeText())
		}
		_, variant := sym.ASTNode.(*ast.Ident)
		if sym.Kind != symbols.SymbolVariant || !variant || sym.Name != path.Segments[len(path.Segments)-1].Name.Name {
			t.Fatalf("resolved %s = %#v, want child variant symbol", path.TypeText(), sym)
		}
		if module.Semantics.ResolvedSymbols[path.Segments[len(path.Segments)-1].Name.ID()] != sym {
			t.Fatalf("final segment of %s does not resolve to variant symbol", path.TypeText())
		}
	}
}

func TestResolveRejectsAliasVariantNamespace(t *testing.T) {
	_, diag := checkResolveSource(t, `enum Result { Pending }
type Alias = Result;
fn main() { let value = Alias::Pending; }`)
	if out := diag.EmitAllToString(); !diag.HasErrors() || !strings.Contains(out, "only enum declarations can qualify variants") {
		t.Fatalf("expected alias namespace diagnostic, got:\n%s", out)
	}
}

func TestResolveRejectsUnknownBracedVariantQualifier(t *testing.T) {
	_, diag := checkResolveSource(t, `fn main() { let value = Missing::Ready{}; }`)
	if out := diag.EmitAllToString(); !diag.HasErrors() || !strings.Contains(out, "unknown import alias `Missing`") {
		t.Fatalf("expected unknown qualifier diagnostic, got:\n%s", out)
	}
}

func TestResolveMatchPatternBindingsInsideArmScope(t *testing.T) {
	module, diag := checkResolveSource(t, `enum Result {
	Ok: { value: i32 },
	Pending
}

fn Read(result: Result) -> i32 {
	match result {
		Result::Ok{ value = payload } => {
			return payload;
		}
		Result::Pending => {
			return 0;
		}
	}
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected match resolution diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[1].(*ast.FnDecl)
	match := fn.Body.Stmts[0].(*ast.MatchStmt)
	binding := match.Arms[0].Fields[0].Binding
	use := match.Arms[0].Body.Stmts[0].(*ast.ReturnStmt).Value.(*ast.Ident)
	bindingSymbol := module.Semantics.ResolvedSymbols[binding.ID()]
	if bindingSymbol == nil || module.Semantics.ResolvedSymbols[use.ID()] != bindingSymbol {
		t.Fatalf("pattern binding = %#v, use = %#v", bindingSymbol, module.Semantics.ResolvedSymbols[use.ID()])
	}
	if _, found := module.Semantics.BlockScopes[match.Arms[0].Body.ID()].Lookup("payload"); !found {
		t.Fatal("pattern binding missing from arm body scope")
	}
}

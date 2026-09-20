package resolver

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/symbols"
	"compiler/pkg/peeper"
)

func checkResolveSource(t *testing.T, src string) (*module.Module, *diagnostics.DiagnosticBag) {
	t.Helper()
	const filePath = "resolver_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "resolver_test"},
		FilePath: filePath,
		Content:  src,
		AST:      modAST,
		Imports:  make(map[string]module.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	Resolve(ctx, module)
	return module, diag
}

func TestRejectedPrivateImportDoesNotPublishUsage(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	dependencyID := moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "dep"}
	private := symbols.New("hidden", symbols.SymbolFunc, nil, nil)
	dependency := &module.Module{ID: dependencyID, ModuleScope: symbols.NewScope(nil)}
	if err := dependency.ModuleScope.Declare(private); err != nil {
		t.Fatalf("declare private imported symbol: %v", err)
	}
	module := &module.Module{
		ID:          moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "main"},
		ModuleScope: symbols.NewScope(nil),
		Imports:     map[string]module.ResolvedImport{"dep": {ID: dependencyID}},
	}
	alias := symbols.New("dep", symbols.SymbolImport, nil, nil)
	if err := module.ModuleScope.Declare(alias); err != nil {
		t.Fatalf("declare import alias: %v", err)
	}
	ctx.AddModule(dependency)
	ctx.AddModule(module)

	r := &resolver{ctx: ctx, module: module}
	if symbol, ok := r.lookupImportedMember(&ast.Ident{Name: "dep"}, &ast.Ident{Name: "hidden"}, &ast.Ident{Name: "hidden"}); ok || symbol != nil {
		t.Fatalf("private imported symbol = (%#v, %t), want rejected", symbol, ok)
	}
	if alias.IsUsed() || private.IsUsed() {
		t.Fatal("rejected private import published usage")
	}
	if !diag.HasErrors() || !strings.Contains(diag.EmitAllToString(), "not exported") {
		t.Fatalf("missing private import diagnostic:\n%s", diag.EmitAllToString())
	}
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

func TestResolvePublishesDiscardDeclarationSymbols(t *testing.T) {
	module, diag := checkResolveSource(t, `fn main() {
	let _ = 1;
	let _ = 2;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	first := fn.Body.Stmts[0].(*ast.LetDecl)
	second := fn.Body.Stmts[1].(*ast.LetDecl)
	firstSymbol := module.Bindings.Symbol(first.Name)
	secondSymbol := module.Bindings.Symbol(second.Name)
	if firstSymbol == nil || secondSymbol == nil || firstSymbol == secondSymbol {
		t.Fatalf("discard declaration symbols = (%#v, %#v), want distinct symbols", firstSymbol, secondSymbol)
	}
}

func TestResolvePublishesAssignmentTargetSymbol(t *testing.T) {
	module, diag := checkResolveSource(t, `fn main() {
	let mut value = 0;
	value = 1;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	declaration := fn.Body.Stmts[0].(*ast.LetDecl)
	target := fn.Body.Stmts[1].(*ast.AssignStmt).Target.(*ast.Ident)
	resolved := module.Bindings.Symbol(target)
	if resolved == nil || resolved.ASTNode != declaration {
		t.Fatalf("assignment target = %#v, want declaration symbol for %#v", resolved, declaration)
	}
}

func TestResolveEnumVariantPathsToChildSymbols(t *testing.T) {
	module, diag := checkResolveSource(t, `enum Result<T> {
	Ok: { value: T },
	Pending,
}
fn main() {
	let ok = Result<i32>::Ok with .{ value = 42 };
	let pending = Result<i32>::Pending;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[1].(*ast.FnDecl)
	okPath := fn.Body.Stmts[0].(*ast.LetDecl).Value.(*ast.VariantLit).Case
	pendingPath := fn.Body.Stmts[1].(*ast.LetDecl).Value.(*ast.ScopeResolution)
	for _, path := range []*ast.ScopeResolution{okPath, pendingPath} {
		sym := module.Bindings.Symbol(path)
		if sym == nil {
			t.Fatalf("resolved %s = nil, want child variant symbol", path.TypeText())
		}
		_, variant := sym.ASTNode.(*ast.Ident)
		if sym.Kind != symbols.SymbolVariant || !variant || sym.Name != path.Segments[len(path.Segments)-1].Name.Name {
			t.Fatalf("resolved %s = %#v, want child variant symbol", path.TypeText(), sym)
		}
		if module.Bindings.Symbol(path.Segments[len(path.Segments)-1].Name) != sym {
			t.Fatalf("final segment of %s does not resolve to variant symbol", path.TypeText())
		}
	}
}

func TestResolveAliasVariantNamespaceToCanonicalSymbol(t *testing.T) {
	module, diag := checkResolveSource(t, `enum Result<T> {
	Ok: { value: T },
	Pending
}
type Alias<T> = Result<T>;
type Chain<T> = Alias<T>;
fn main() {
	let pending = Alias<i32>::Pending;
	let ok = Chain<i32>::Ok with .{ value = 42 };
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected alias namespace diagnostic:\n%s", diag.EmitAllToString())
	}
	result, _ := module.ModuleScope.LookupLocal("Result")
	if result == nil || result.Scope == nil {
		t.Fatal("canonical enum symbol missing")
	}
	fn := module.AST.Stmts[3].(*ast.FnDecl)
	pendingPath := fn.Body.Stmts[0].(*ast.LetDecl).Value.(*ast.ScopeResolution)
	okPath := fn.Body.Stmts[1].(*ast.LetDecl).Value.(*ast.VariantLit).Case
	for _, path := range []*ast.ScopeResolution{pendingPath, okPath} {
		_, caseName, valid := path.EnumVariantMember()
		if !valid || caseName == nil {
			t.Fatalf("invalid variant path %s", path.TypeText())
		}
		canonical, _ := result.Scope.LookupLocal(caseName.Name)
		if got := module.Bindings.Symbol(path); got == nil || got != canonical {
			t.Fatalf("resolved %s = %#v, want canonical %#v", path.TypeText(), got, canonical)
		}
	}
}

func TestResolveRejectsNonEnumAliasVariantNamespace(t *testing.T) {
	for _, source := range []string{
		"type Alias = ?i32; fn main() { let value = Alias::Present; }",
		"struct Box {} type Alias = Box; fn main() { let value = Alias::Ready; }",
	} {
		_, diag := checkResolveSource(t, source)
		if out := diag.EmitAllToString(); !diag.HasErrors() || !strings.Contains(out, "variant qualifier must resolve to a named enum") {
			t.Fatalf("expected non-enum alias diagnostic, got:\n%s", out)
		}
	}
}

func TestResolveRejectsUnknownPayloadVariantQualifier(t *testing.T) {
	_, diag := checkResolveSource(t, `fn main() { let value = Missing::Ready with .{}; }`)
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
		Result::Ok with { value = payload } => {
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
	bindingSymbol := module.Bindings.Symbol(binding)
	if bindingSymbol == nil || module.Bindings.Symbol(use) != bindingSymbol {
		t.Fatalf("pattern binding = %#v, use = %#v", bindingSymbol, module.Bindings.Symbol(use))
	}
	if _, found := module.Bindings.Scope(match.Arms[0].Body).Lookup("payload"); !found {
		t.Fatal("pattern binding missing from arm body scope")
	}
}

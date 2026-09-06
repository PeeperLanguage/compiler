package consteval

import (
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/moduleid"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/peeper"
)

func constevalModule(t *testing.T, src string) (*project.Module, *diagnostics.DiagnosticBag) {
	t.Helper()
	const filePath = "consteval_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &project.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "consteval_test"},
		FilePath: filePath,
		Content:  src,
		AST:      parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	Evaluate(ctx, module)
	return module, diag
}

func TestEvaluateInitializesOnlyConstantResult(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	module := &project.Module{ModuleScope: symbols.NewScope(nil)}

	Evaluate(project.New(".", peeper.SourceExt, diag), module)

	if module.Constants == nil || module.Constants.ModuleValues == nil || module.Constants.QueryCache == nil {
		t.Fatal("Evaluate did not initialize constant result")
	}
	if module.Bindings != nil {
		t.Fatalf("Evaluate initialized Bindings: %#v", module.Bindings)
	}
}

func TestEvaluateTopLevelConstExpressions(t *testing.T) {
	module, diag := constevalModule(t, `const A = 1 + 2 * 3;
const B = A + 4;
const C = true && false;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "A", "7", "")
	assertIntConst(t, module, "B", "11", "")
	assertBoolConst(t, module, "C", false)
}

func TestEvaluateCanonicalizesIntegerLiteralBases(t *testing.T) {
	module, diag := constevalModule(t, `const Hex = 0x10;
const Octal = 0o10;
const Binary = 0b10;
const Padded = 01;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "Hex", "16", "")
	assertIntConst(t, module, "Octal", "8", "")
	assertIntConst(t, module, "Binary", "2", "")
	assertIntConst(t, module, "Padded", "1", "")
}

func TestEvaluateBitwiseConstExpressions(t *testing.T) {
	module, diag := constevalModule(t, `const And: u8 = 12u8 & 10u8;
const Or: u8 = 12u8 | 10u8;
const Xor: u8 = 12u8 ^ 10u8;
const Complement: u8 = ~0u8;
const Left: i8 = 127i8 << 1i8;
const Right: i8 = -8i8 >> 2i8;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "And", "8", "u8")
	assertIntConst(t, module, "Or", "14", "u8")
	assertIntConst(t, module, "Xor", "6", "u8")
	assertIntConst(t, module, "Complement", "255", "u8")
	assertIntConst(t, module, "Left", "-2", "i8")
	assertIntConst(t, module, "Right", "-2", "i8")
}

func TestEvaluateBitwiseConstExpressionsThroughIntegralAlias(t *testing.T) {
	module, diag := constevalModule(t, `type Flags = u8;
const Mask: Flags = ~0;
const Shifted: Flags = 1 << 2;
const Wrapped: Flags = 1 << (255 + 1);
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "Mask", "255", "u8")
	assertIntConst(t, module, "Shifted", "4", "u8")
	assertIntConst(t, module, "Wrapped", "1", "u8")
}

func TestEvaluateReportsConstCycle(t *testing.T) {
	_, diag := constevalModule(t, `const A = B;
const B = A;
`)
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrCircularDependency {
			return
		}
	}
	t.Fatalf("expected circular dependency diagnostic, got:\n%s", diag.EmitAllToString())
}

func TestEvaluateUsesDeclaredTypeForNumericConst(t *testing.T) {
	module, diag := constevalModule(t, `const A: i64 = 1;
const B = A + 2147483648;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "A", "1", "i64")
	assertIntConst(t, module, "B", "2147483649", "i64")
}

func TestEvaluateUsesDeclaredTypeForNumericExpression(t *testing.T) {
	module, diag := constevalModule(t, `const A: i64 = 1 + 2;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "A", "3", "i64")
}

func TestEvaluateUsesConstOperandTypeForSmallLiteral(t *testing.T) {
	module, diag := constevalModule(t, `const A: i64 = 1;
const B = A + 1;
const C = 1 + A;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "B", "2", "i64")
	assertIntConst(t, module, "C", "2", "i64")
}

func TestEvaluateRetypesCachedConstIdentifierForCommonType(t *testing.T) {
	module, diag := constevalModule(t, `const A = 1;
const W: i64 = 2;
const B = A + W;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "A", "1", "i32")
	assertIntConst(t, module, "B", "3", "i64")
}

func TestFinalizeValuesRecomputesConstantsWithFinalSymbolTypes(t *testing.T) {
	module, diag := constevalModule(t, `const Value = 1;
`)
	assertIntConst(t, module, "Value", "1", "i32")
	sym, ok := module.ModuleScope.LookupLocal("Value")
	if !ok || sym == nil {
		t.Fatal("missing symbol Value")
	}
	if _, found := module.Constants.QueryCache[sym.ID]; !found {
		t.Fatal("eager constant missing provisional query-cache value")
	}
	if _, found := module.Constants.ModuleValues[sym.ID]; found {
		t.Fatal("eager prepass published authoritative module value before typecheck")
	}
	sym.BindType(&typeinfo.IntegerType{Signed: true, Bits: 64})
	ctx := project.New(".", peeper.SourceExt, diag)
	ctx.AddModule(module)
	FinalizeValues(ctx, module)
	assertIntConst(t, module, "Value", "1", "i64")
	if _, found := module.Constants.QueryCache[sym.ID]; found {
		t.Fatal("finalized module constant remains duplicated in query cache")
	}
	if _, found := module.Constants.ModuleValues[sym.ID]; !found {
		t.Fatal("finalized module constant was not published")
	}
}

func TestEvaluateExprCachesLocalConstantsWithoutChangingPublishedValues(t *testing.T) {
	module, diag := constevalModule(t, `const Top = 1;
fn main() {
	const Local = 2;
	let value = Local;
}
`)
	ctx := project.New(".", peeper.SourceExt, diag)
	ctx.AddModule(module)
	FinalizeValues(ctx, module)
	fn := module.AST.Stmts[1].(*ast.FnDecl)
	local := fn.Body.Stmts[0].(*ast.ConstDecl)
	reference := fn.Body.Stmts[1].(*ast.LetDecl).Value.(*ast.Ident)
	scope := module.Bindings.BlockScopes[fn.Body.ID()]
	if _, ok := EvaluateExpr(ctx, module, scope, reference, nil); !ok {
		t.Fatal("failed to evaluate local constant reference")
	}
	localSymbol, found := scope.LookupLocal(local.Name.Name)
	if !found || localSymbol == nil {
		t.Fatal("missing local constant symbol")
	}
	if _, found := module.Constants.QueryCache[localSymbol.ID]; !found {
		t.Fatal("local constant missing query-cache entry")
	}
	if _, found := module.Constants.ModuleValues[localSymbol.ID]; found {
		t.Fatal("local constant leaked into authoritative module values")
	}
	top, _ := module.ModuleScope.LookupLocal("Top")
	if _, found := module.Constants.QueryCache[top.ID]; found {
		t.Fatal("published module constant was duplicated by local query")
	}
}

func TestEvaluateReadsForeignPublishedConstantWithoutConsumerCache(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	parse := func(filePath, importPath, src string) *project.Module {
		module := &project.Module{
			ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: importPath},
			FilePath: filePath,
			Content:  src,
			AST:      parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
			Imports:  make(map[string]project.ResolvedImport),
		}
		ctx.AddModule(module)
		return module
	}
	resolve := func(module *project.Module) {
		collector.Collect(ctx, module)
		binder.Bind(ctx, module)
		resolver.Resolve(ctx, module)
	}

	owner := parse("owner"+peeper.SourceExt, "owner", "const Shared: i32 = 7;")
	resolve(owner)
	Evaluate(ctx, owner)
	FinalizeValues(ctx, owner)
	shared, found := owner.ModuleScope.LookupLocal("Shared")
	if !found || shared == nil || shared.DefiningModule != owner.ID {
		t.Fatalf("foreign constant owner = %#v, want %v", shared, owner.ID)
	}
	if err := ctx.GlobalScope.Declare(shared); err != nil {
		t.Fatalf("publish shared constant: %v", err)
	}

	consumer := parse("consumer"+peeper.SourceExt, "consumer", "const Local = Shared;")
	resolve(consumer)
	Evaluate(ctx, consumer)
	local, found := consumer.ModuleScope.LookupLocal("Local")
	if !found || local == nil {
		t.Fatal("missing consumer constant")
	}
	value, ok := consumer.Constants.QueryCache[local.ID].(*constvalue.IntConst)
	if !ok || value == nil || value.Text() != "7" {
		t.Fatalf("consumer value = %#v, want 7", consumer.Constants.QueryCache[local.ID])
	}
	if _, found := consumer.Constants.QueryCache[shared.ID]; found {
		t.Fatal("foreign constant duplicated in consumer query cache")
	}
	if _, found := owner.Constants.ModuleValues[shared.ID]; !found {
		t.Fatal("owner lost published constant")
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestEvaluateUsesConstOperandTypeForNestedArithmetic(t *testing.T) {
	module, diag := constevalModule(t, `const A: i64 = 1;
const B = A + (1 + 2);
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "B", "4", "i64")
}

func TestEvaluateStringConst(t *testing.T) {
	module, diag := constevalModule(t, `const Name: cstr = c"puts";
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	sym, ok := module.ModuleScope.LookupLocal("Name")
	if !ok || sym == nil {
		t.Fatalf("missing symbol Name")
	}
	value := evaluatedConst(module, sym.ID)
	got, ok := value.(*constvalue.StringConst)
	if !ok || got == nil || got.Text() != "puts" || got.TypeText() != "cstr" {
		t.Fatalf("Name = %#v, want str puts cstr", value)
	}
}

func assertIntConst(t *testing.T, module *project.Module, name, want, wantType string) {
	t.Helper()
	sym, ok := module.ModuleScope.LookupLocal(name)
	if !ok || sym == nil {
		t.Fatalf("missing symbol %s", name)
	}
	value := evaluatedConst(module, sym.ID)
	got, ok := value.(*constvalue.IntConst)
	if !ok || got == nil || got.Text() != want || (wantType != "" && got.TypeText() != wantType) {
		t.Fatalf("%s = %#v, want int %s %s", name, value, want, wantType)
	}
}

func evaluatedConst(module *project.Module, id symbols.SymbolID) constvalue.Value {
	if value := module.Constants.ModuleValues[id]; value != nil {
		return value
	}
	return module.Constants.QueryCache[id]
}

func assertBoolConst(t *testing.T, module *project.Module, name string, want bool) {
	t.Helper()
	sym, ok := module.ModuleScope.LookupLocal(name)
	if !ok || sym == nil {
		t.Fatalf("missing symbol %s", name)
	}
	value := evaluatedConst(module, sym.ID)
	got, ok := value.(*constvalue.BoolConst)
	if !ok || got == nil || got.Bool() != want {
		t.Fatalf("%s = %#v, want bool %v", name, value, want)
	}
}

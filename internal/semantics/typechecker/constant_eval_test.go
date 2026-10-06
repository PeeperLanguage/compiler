package typechecker

import (
	"fmt"
	"math"
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir/thir"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/peeper"
)

func finalizeConstantValues(ctx *project.CompilerContext, module *module.Module, evidence *evidence) {
	newConstantEvaluator(ctx, module, evidence).finalizeModuleValues()
}

func constevalModule(t *testing.T, src string) (*module.Module, *diagnostics.DiagnosticBag) {
	t.Helper()
	const filePath = "consteval_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "consteval_test"},
		FilePath: filePath,
		Content:  src,
		AST:      parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]module.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	finalizeConstantValues(ctx, module, newEvidence())
	return module, diag
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

func TestEvaluateIntegerCastsBeforeConstantShifts(t *testing.T) {
	module, diag := checkTypeModule(t, `type Count = u8;
const Shifted: u8 = 128u8 >> (2u16 as u8);
const Truncated: u8 = 128u8 >> (258u16 as u8);
const Wrapped: u8 = 1u8 << (256u16 as u8);
const Nested: u8 = 1u8 << ((258u16 as Count) as u8);
const SourceOverflow: u16 = (255u8 + 1u8) as u16;
const SignedWiden: u16 = -1i8 as u16;
const SignedNarrow: i8 = 255u16 as i8;
const DeclaredWide: u32 = 258u16 as u8;
const ByteCount: byte = 2u16 as byte;
const FromByte: u32 = ByteCount as u32;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	for _, test := range []struct{ name, value, typ string }{
		{"Shifted", "32", "u8"}, {"Truncated", "32", "u8"},
		{"Wrapped", "1", "u8"}, {"Nested", "4", "u8"},
		{"SourceOverflow", "0", "u16"}, {"SignedWiden", "65535", "u16"},
		{"SignedNarrow", "-1", "i8"}, {"DeclaredWide", "2", "u32"},
		{"ByteCount", "2", "byte"}, {"FromByte", "2", "u32"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertIntConst(t, module, test.name, test.value, test.typ)
		})
	}
}

func TestEvaluateIntegerCastWithoutTypingEvidence(t *testing.T) {
	module, diag := constevalModule(t, `type Count = u8;
const Converted: Count = 258u16 as Count;
const Shifted: u8 = 128u8 >> Converted;
const Inferred = 258u16 as Count;
const Widened: u16 = 258u16 as Count;
const InferredByte = 258u16 as byte;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "Shifted", "32", "u8")
	assertIntConst(t, module, "Converted", "2", "u8")
	assertIntConst(t, module, "Inferred", "2", "u8")
	assertIntConst(t, module, "Widened", "2", "u16")
	assertIntConst(t, module, "InferredByte", "2", "byte")
}

func TestEvaluateCheckedNumericConversions(t *testing.T) {
	for _, test := range []struct{ name, source, value, typ string }{
		{"mixed operands", "const Value: i64 = 1i32 + 2i64;", "3", "i64"},
		{"explicit initializer", "const Value: i64 = 2i8;", "2", "i64"},
		{"signed widening", "const Value: u16 = -1i8;", "65535", "u16"},
		{"source overflow", "const Value: i64 = 127i8 + 1i8;", "-128", "i64"},
		{"nested overflow", "const Value: i64 = (127i8 + 1i8) + 1i64;", "-127", "i64"},
		{"unary overflow", "const Value: i64 = -(-128i8);", "-128", "i64"},
		{"implicit float widening", "const Value: f64 = 1.25f32;", "1.25", "f64"},
		{"mixed floats", "const Value: f64 = 1.25f32 + 2.5f64;", "3.75", "f64"},
		{"explicit float narrowing", "const Value: f32 = 1.25f64 as f32;", "1.25", "f32"},
		{"round then widen", "const Value: f64 = 16777217f64 as f32;", "1.6777216e+07", "f64"},
		{"aliased round then widen", "type Narrow = f32; type Wide = f64; const Value: Wide = 16777217f64 as Narrow;", "1.6777216e+07", "f64"},
		{"negative zero then widen", "const Value: f64 = -0f64 as f32;", "-0", "f64"},
		{"float intermediate width", "const Value: f64 = 16777216f32 + 1f32;", "1.6777216e+07", "f64"},
		{"integer to float", "const Value: f32 = -3i8 as f32;", "-3", "f32"},
		{"float to integer", "const Value: i8 = -3.75f64 as i8;", "-3", "i8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			module, diag := checkTypeModule(t, test.source)
			if diag.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
			}
			value := evaluatedConst(t, module, "Value")
			text, ok := value.(interface{ Text() string })
			if !ok || text.Text() != test.value || value.TypeText() != test.typ {
				t.Fatalf("published Value = %#v, want %s %s", value, test.value, test.typ)
			}
		})
	}
	module, diag := checkTypeModule(t, `const Less: bool = 1i8 < 2i64;
const Greater: bool = 2i64 > 1i8;`)
	if diag.HasErrors() {
		t.Fatalf("unexpected comparison diagnostics:\n%s", diag.EmitAllToString())
	}
	assertBoolConst(t, module, "Less", true)
	assertBoolConst(t, module, "Greater", true)
}

func TestEvaluateRejectsUnrepresentableFloatToIntegerConstants(t *testing.T) {
	for _, expression := range []string{
		"128f64 as i8", "-129f64 as i8", "-1f64 as u8", "256f64 as u8",
		"(1f64 / 0f64) as i32", "(0f64 / 0f64) as i32",
	} {
		t.Run(expression, func(t *testing.T) {
			module, diag := checkTypeModule(t, "const Value = "+expression+";")
			if diag.HasErrors() {
				t.Fatalf("unexpected typing diagnostics:\n%s", diag.EmitAllToString())
			}
			if value := evaluatedConst(t, module, "Value"); value != nil {
				t.Fatalf("unrepresentable float cast published %#v", value)
			}
		})
	}
}

func TestEvaluateIntegerToFloatRoundsAtDestinationWidth(t *testing.T) {
	for _, test := range []struct {
		name, expression, typ string
		want                  float64
	}{
		{"midpoint", "9223372586610589696u64 as f32", "f32", 9223372036854775808},
		{"above midpoint", "9223372586610589697u64 as f32", "f32", 9223373136366403584},
		{"negative above midpoint", "-9007199791611905i64 as f32", "f32", -9007200328482816},
		{"f32 infinity", "(1u256 << 128u256) as f32", "f32", math.Inf(1)},
		{"f64 infinity", "(1u2048 << 1024u2048) as f64", "f64", math.Inf(1)},
		{"negative infinity", "-(1i2048 << 1024i2048) as f64", "f64", math.Inf(-1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			module, diag := checkTypeModule(t, "const Value = "+test.expression+";")
			if diag.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
			}
			published := evaluatedConst(t, module, "Value")
			value, ok := published.(*constvalue.FloatConst)
			if !ok || value == nil || value.Float() != test.want || value.TypeText() != test.typ {
				t.Fatalf("published Value = %#v, want %g %s", published, test.want, test.typ)
			}
		})
	}
}

func TestExpectedNumericConstValueRejectsInvalidIntegers(t *testing.T) {
	for _, value := range []*constvalue.IntConst{nil, {}} {
		for _, typ := range []typeinfo.Type{&typeinfo.FloatType{Bits: 32}, &typeinfo.IntegerType{IsSigned: true, Bits: 64}} {
			if converted, ok := expectedNumericConstValue(value, typ); ok || converted != nil {
				t.Fatalf("invalid integer converted to %#v", converted)
			}
		}
	}
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

func TestFinalizeValuesRecomputesLazyConstantsWithFinalSymbolTypes(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	const filePath = "consteval_test" + peeper.SourceExt
	src := `const Value = 1;`
	diag.AddSourceContent(filePath, src)
	module := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "consteval_test"},
		FilePath: filePath,
		Content:  src,
		AST:      parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]module.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	sym, ok := module.ModuleScope.LookupLocal("Value")
	if !ok || sym == nil {
		t.Fatal("missing symbol Value")
	}
	evaluator := newConstantEvaluator(ctx, module, newEvidence())
	if _, ok := evaluator.evalConstSymbol(sym); !ok {
		t.Fatal("failed to lazily evaluate Value")
	}
	if _, found := evaluator.cache[sym.ID]; !found {
		t.Fatal("lazy constant missing operation-local cache value")
	}
	if module.SymbolIndex.ConstantValue(sym.ID) != nil {
		t.Fatal("lazy query published authoritative module value before finalization")
	}
	sym.BindType(&typeinfo.IntegerType{IsSigned: true, Bits: 64})
	evaluator.finalizeModuleValues()
	assertIntConst(t, module, "Value", "1", "i64")
	if _, found := evaluator.cache[sym.ID]; found {
		t.Fatal("finalized module constant remains duplicated in evaluator cache")
	}
	if module.SymbolIndex.ConstantValue(sym.ID) == nil {
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
	fn := module.AST.Stmts[1].(*ast.FnDecl)
	local := fn.Body.Stmts[0].(*ast.ConstDecl)
	reference := fn.Body.Stmts[1].(*ast.LetDecl).Value.(*ast.Ident)
	scope := module.SymbolIndex.Scope(fn.Body)
	evaluator := newConstantEvaluator(ctx, module, newEvidence())
	if _, ok := evaluator.evalExpr(reference, nil); !ok {
		t.Fatal("failed to evaluate local constant reference")
	}
	localSymbol, found := scope.LookupLocal(local.Name.Name)
	if !found || localSymbol == nil {
		t.Fatal("missing local constant symbol")
	}
	if _, found := evaluator.cache[localSymbol.ID]; !found {
		t.Fatal("local constant missing evaluator-cache entry")
	}
	if module.SymbolIndex.ConstantValue(localSymbol.ID) != nil {
		t.Fatal("local constant leaked into authoritative module values")
	}
	top, _ := module.ModuleScope.LookupLocal("Top")
	if _, found := evaluator.cache[top.ID]; found {
		t.Fatal("published module constant was duplicated by local query")
	}
	if module.SymbolIndex.ConstantValue(top.ID) == nil {
		t.Fatal("top-level constant lost authoritative value")
	}
}

func TestEvaluateUsesResolvedConstantBindings(t *testing.T) {
	for _, shadow := range []string{"const", "let"} {
		t.Run(shadow, func(t *testing.T) {
			module, diag := checkTypeModule(t, fmt.Sprintf(`const index: i32 = 0;
fn main() {
	const saved: i32 = index;
	%s index: i32 = 2;
	let values = [3]i32{10, 20, 30};
	let selected = values[saved];
	if saved == 0 { print(selected); }
}`, shadow))
			if diag.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
			}
			fn := module.AST.Stmts[1].(*ast.FnDecl)
			index := fn.Body.Stmts[3].(*ast.LetDecl).Value.(*ast.IndexExpr)
			checkedIndex := module.THIR.Node(index.ID()).(*thir.Index)
			if checkedIndex.Constant == nil || checkedIndex.Constant.Text != "0" {
				t.Errorf("constant index = %#v, want resolved outer value 0", checkedIndex.Constant)
			}
			condition := fn.Body.Stmts[4].(*ast.IfStmt)
			checkedCondition := module.THIR.Node(condition.ID()).(*thir.If)
			if checkedCondition.ConstantCondition == nil || !*checkedCondition.ConstantCondition {
				t.Errorf("constant condition = %v, want true from resolved outer value", checkedCondition.ConstantCondition)
			}
		})
	}
}

func TestEvaluateRejectsUnboundConstantReferences(t *testing.T) {
	module, diag := constevalModule(t, `const Value: i32 = 7;`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	ctx := project.New(".", peeper.SourceExt, diag)
	ctx.AddModule(module)
	evaluator := newConstantEvaluator(ctx, module, newEvidence())
	for _, expr := range []ast.Expr{
		&ast.Ident{Name: "Value"},
		&ast.ScopeResolution{Segments: []ast.PathSegment{{Name: &ast.Ident{Name: "owner"}}, {Name: &ast.Ident{Name: "Value"}}}},
	} {
		if value, ok := evaluator.evalExpr(expr, nil); ok {
			t.Fatalf("unbound %T evaluated to %#v through name lookup", expr, value)
		}
	}
}

func TestEvaluateReadsForeignPublishedConstantWithoutConsumerCache(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	parse := func(filePath, importPath, src string) *module.Module {
		module := &module.Module{
			ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: importPath},
			FilePath: filePath,
			Content:  src,
			AST:      parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
			Imports:  make(map[string]module.ResolvedImport),
		}
		ctx.AddModule(module)
		return module
	}
	resolve := func(module *module.Module) {
		collector.Collect(ctx, module)
		binder.Bind(ctx, module)
		resolver.Resolve(ctx, module)
	}

	owner := parse("owner"+peeper.SourceExt, "owner", `const Shared: i32 = 7;
type Number = i32;
fn Read() -> i32 { return 7; }`)
	resolve(owner)
	finalizeConstantValues(ctx, owner, newEvidence())
	shared, found := owner.ModuleScope.LookupLocal("Shared")
	if !found || shared == nil || shared.DefiningModule != owner.ID {
		t.Fatalf("foreign constant owner = %#v, want %v", shared, owner.ID)
	}
	if err := ctx.GlobalScope.Declare(shared); err != nil {
		t.Fatalf("publish shared constant: %v", err)
	}

	consumer := parse("consumer"+peeper.SourceExt, "consumer", `const Local = Shared;
const Qualified = owner::Shared;
fn Probe() {
	let Mutable: i32 = 7;
	const Variable = Mutable;
	const Function = owner::Read;
	const Type = owner::Number;
}`)
	consumer.Imports["owner"] = module.ResolvedImport{ID: owner.ID, FilePath: owner.FilePath}
	resolve(consumer)
	if diag.HasErrors() {
		t.Fatalf("foreign binding setup failed:\n%s", diag.EmitAllToString())
	}
	// Foreign reads must use the published value, not re-evaluate changed owner AST.
	shared.ASTNode.(*ast.ConstDecl).Value = &ast.NumberLit{Value: "99"}
	evaluator := newConstantEvaluator(ctx, consumer, newEvidence())
	for _, name := range []string{"Local", "Qualified"} {
		local, found := consumer.ModuleScope.LookupLocal(name)
		if !found || local == nil {
			t.Fatalf("missing consumer constant %s", name)
		}
		if _, ok := evaluator.evalConstSymbol(local); !ok {
			t.Fatalf("failed to lazily evaluate %s", name)
		}
		cached := evaluator.cache[local.ID]
		value, ok := cached.(*constvalue.IntConst)
		if !ok || value == nil || value.Text() != "7" {
			t.Fatalf("%s consumer value = %#v, want published 7", name, cached)
		}
	}
	if _, found := evaluator.cache[shared.ID]; found {
		t.Fatal("foreign constant duplicated in consumer query cache")
	}
	if value := owner.SymbolIndex.ConstantValue(shared.ID).(*constvalue.IntConst); value.Text() != "7" {
		t.Fatalf("owner published value changed to %s", value.Text())
	}
	probe := consumer.AST.Stmts[2].(*ast.FnDecl)
	for _, stmt := range probe.Body.Stmts[1:] {
		decl := stmt.(*ast.ConstDecl)
		expr := decl.Value
		bound := consumer.SymbolIndex.Symbol(expr)
		if bound == nil {
			t.Fatalf("nonconstant %s has no canonical binding", decl.Name.Name)
		}
		evaluator.cache[bound.ID], _ = constvalue.NewIntText("99", "i32")
		if value, ok := evaluator.evalExpr(expr, nil); ok {
			t.Fatalf("nonconstant %s evaluated to %#v", decl.Name.Name, value)
		}
	}
	owner.SymbolIndex.ClearConstants()
	evaluator.cache[shared.ID], _ = constvalue.NewIntText("99", "i32")
	for _, stmt := range consumer.AST.Stmts[:2] {
		expr := stmt.(*ast.ConstDecl).Value
		if value, ok := evaluator.evalExpr(expr, nil); ok {
			t.Fatalf("unpublished foreign %T evaluated to %#v through consumer cache or owner AST", expr, value)
		}
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
	value := evaluatedConst(t, module, "Name")
	got, ok := value.(*constvalue.StringConst)
	if !ok || got == nil || got.Text() != "puts" || got.TypeText() != "cstr" {
		t.Fatalf("Name = %#v, want str puts cstr", value)
	}
}

func assertIntConst(t *testing.T, module *module.Module, name, want, wantType string) {
	t.Helper()
	value := evaluatedConst(t, module, name)
	got, ok := value.(*constvalue.IntConst)
	if !ok || got == nil || got.Text() != want || (wantType != "" && got.TypeText() != wantType) {
		t.Fatalf("%s = %#v, want int %s %s", name, value, want, wantType)
	}
}

func evaluatedConst(t *testing.T, module *module.Module, name string) constvalue.Value {
	t.Helper()
	if module == nil || module.ModuleScope == nil || module.SymbolIndex == nil {
		t.Fatal("constant publication setup missing")
	}
	sym, ok := module.ModuleScope.LookupLocal(name)
	if !ok || sym == nil {
		t.Fatalf("missing constant %s", name)
	}
	return module.SymbolIndex.ConstantValue(sym.ID)
}

func assertBoolConst(t *testing.T, module *module.Module, name string, want bool) {
	t.Helper()
	value := evaluatedConst(t, module, name)
	got, ok := value.(*constvalue.BoolConst)
	if !ok || got == nil || got.Bool() != want {
		t.Fatalf("%s = %#v, want bool %v", name, value, want)
	}
}

func TestConstantQueryCacheIsPrivateUntilFinalization(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	const filePath = "constant_cache_test" + peeper.SourceExt
	src := `const Value = 1;`
	diag.AddSourceContent(filePath, src)
	mod := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "constant_cache_test"},
		FilePath: filePath,
		Content:  src,
		AST:      parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]module.ResolvedImport),
	}
	ctx.AddModule(mod)
	collector.Collect(ctx, mod)
	binder.Bind(ctx, mod)
	resolver.Resolve(ctx, mod)
	sym, ok := mod.ModuleScope.LookupLocal("Value")
	if !ok || sym == nil {
		t.Fatal("missing Value symbol")
	}

	evaluator := newConstantEvaluator(ctx, mod, newEvidence())
	if _, ok := evaluator.evalConstSymbol(sym); !ok {
		t.Fatal("lazy constant evaluation failed")
	}
	if got := mod.SymbolIndex.ConstantValue(sym.ID); got != nil {
		t.Fatalf("lazy query published authoritative value: %#v", got)
	}
	if _, ok := evaluator.cache[sym.ID]; !ok {
		t.Fatal("lazy value missing from operation-local evaluator cache")
	}

	finalizeConstantValues(ctx, mod, newEvidence())
	if got := mod.SymbolIndex.ConstantValue(sym.ID); got == nil {
		t.Fatal("finalization did not publish authoritative constant")
	}
}

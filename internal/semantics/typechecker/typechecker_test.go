package typechecker

import (
	"fmt"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
	"compiler/pkg/peeper"
)

func checkTypeSource(t *testing.T, src string) *diagnostics.DiagnosticBag {
	t.Helper()
	const filePath = "typechecker_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "typechecker_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	Check(ctx, module)
	return diag
}

func checkTypeSourceWithExternalImport(t *testing.T, src string) (*project.Module, *diagnostics.DiagnosticBag) {
	t.Helper()
	const (
		filePath     = "typechecker_test" + peeper.SourceExt
		externalPath = "external" + peeper.SourceExt
		externalSrc  = `fn GetValue() -> i32 { return 42; }`
	)
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	diag.AddSourceContent(externalPath, externalSrc)
	ctx := project.New(".", peeper.SourceExt, diag)

	extAST := parser.New(externalPath, lexer.New(externalPath, externalSrc, diag).Tokenize(), diag).ParseModule()
	extModule := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, externalPath),
		ImportPath: "external",
		FilePath:   externalPath,
		Content:    externalSrc,
		AST:        extAST,
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(extModule)
	collector.Collect(ctx, extModule)
	binder.Bind(ctx, extModule)
	resolver.Resolve(ctx, extModule)
	Check(ctx, extModule)

	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "typechecker_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports: map[string]project.ResolvedImport{
			"external": {
				Key:        extModule.Key,
				ImportPath: "external",
				FilePath:   externalPath,
				Origin:     project.ModuleOriginLocal,
			},
		},
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	Check(ctx, module)
	return module, diag
}

func TestDefaultRangeWithOmittedBoundsClones(t *testing.T) {
	diag := checkTypeSource(t, `fn First(values: &[..]i32, view: &[..]i32 = values[..]) -> i32 {
	return view[0];
}
fn main() -> i32 {
	let values = []i32{7};
	return First(values[..]);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipeAdaptsFirstArgumentOnly(t *testing.T) {
	diag := checkTypeSource(t, `fn Read(values: &[..]i32) -> i32 { return values[0]; }
fn Write(values: &mut [..]i32, value: i32) { values[0] = value; }
fn Consume(value: []i32) -> usize { return value |> len(); }
fn main() -> i32 {
	let mut values = []i32{1};
	values |> Write(2);
	let first = values |> Read();
	let count = values |> Consume();
	return first + count as i32;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected pipe diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDirectCallDoesNotImplicitlyBorrowFirstArgument(t *testing.T) {
	diag := checkTypeSource(t, `fn Read(values: &[..]i32) {}
fn main() { let values = []i32{1}; Read(values); }`)
	if !hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
		t.Fatalf("expected explicit direct-borrow diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestPipeArityCountsOnlyExplicitArguments(t *testing.T) {
	diag := checkTypeSource(t, `fn Write(value: &mut i32, replacement: i32) {}
fn main() { let mut value = 1; value |> Write(); }`)
	if out := diag.EmitAllToString(); !strings.Contains(out, "wrong number of arguments: got 0, want 1") ||
		!strings.Contains(out, "expected parameters: (i32)") {
		t.Fatalf("unexpected pipe arity diagnostic:\n%s", out)
	}
}

func TestAllocArityReportsSourceArguments(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{name: "missing value", src: `fn main() { alloc(); }`, want: "wrong number of arguments: got 0, want 1"},
		{name: "direct excess", src: `fn main() { alloc(1, 2, 3); }`, want: "wrong number of arguments: got 3, want 2"},
		{name: "piped excess", src: `fn main() { 1 |> alloc(2, 3); }`, want: "wrong number of arguments: got 2, want 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diag := checkTypeSource(t, tt.src)
			if out := diag.EmitAllToString(); !strings.Contains(out, tt.want) {
				t.Fatalf("unexpected alloc arity diagnostic:\n%s", out)
			}
		})
	}
}

func checkTypeModule(t *testing.T, src string) (*project.Module, *diagnostics.DiagnosticBag) {
	t.Helper()
	const filePath = "typechecker_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "typechecker_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	Check(ctx, module)
	return module, diag
}

func TestEnumDeclarationValidation(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "empty enum", source: `enum Empty {}`, want: "enum requires at least one variant"},
		{name: "lowercase variant", source: `enum Status { ready }`, want: "variant name must be PascalCase"},
		{name: "duplicate field", source: `enum Result { Ok: { value: i32, value: i64 } }`, want: "variant field `value` already declared"},
		{name: "unsized field", source: `iface Reader { fn (&Self) read() -> i32 } enum Event { Read: { reader: Reader } }`, want: "enum variant field requires a sized type"},
		{name: "method field collision", source: `enum Result { Ok: { value: i32 } } fn (self: Result) value() {}`, want: "method `value` conflicts with enum variant data field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diag := checkTypeSource(t, test.source)
			if out := diag.EmitAllToString(); !diag.HasErrors() || !strings.Contains(out, test.want) {
				t.Fatalf("expected %q diagnostic, got:\n%s", test.want, out)
			}
		})
	}
}

func TestEnumConstructorsRecordOrderedSemanticEvidence(t *testing.T) {
	module, diag := checkTypeModule(t, `enum Result<T> {
	Ok: { value: T, code: i32 },
	Pending,
}
fn main() {
	let ok = Result<i32>::Ok{ code = 7, value = 42 };
	let pending = Result<i32>::Pending;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[1].(*ast.FnDecl)
	ok := fn.Body.Stmts[0].(*ast.LetDecl).Value
	pending := fn.Body.Stmts[1].(*ast.LetDecl).Value
	okEvidence, found := module.Semantics.VariantConstructions[ok.ID()]
	if !found || typeinfo.TypeText(okEvidence.EnumType) != "Result<i32>" || okEvidence.Case != 0 ||
		okEvidence.Payload == nil || len(okEvidence.Fields) != 2 || ast.ExprText(okEvidence.Fields[0]) != "42" || ast.ExprText(okEvidence.Fields[1]) != "7" {
		t.Fatalf("Ok construction evidence = %#v", okEvidence)
	}
	pendingEvidence, found := module.Semantics.VariantConstructions[pending.ID()]
	if !found || typeinfo.TypeText(pendingEvidence.EnumType) != "Result<i32>" || pendingEvidence.Case != 1 ||
		pendingEvidence.Payload != nil || len(pendingEvidence.Fields) != 0 {
		t.Fatalf("Pending construction evidence = %#v", pendingEvidence)
	}
}

func TestEnumConstructorDiagnostics(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "missing field", source: `enum Result { Ok: { value: i32 } } fn main() { let value = Result::Ok{}; }`, want: "missing enum variant literal field `value`"},
		{name: "duplicate field", source: `enum Result { Ok: { value: i32 } } fn main() { let value = Result::Ok{ value = 1, value = 2 }; }`, want: "duplicate enum variant literal field `value`"},
		{name: "unknown field", source: `enum Result { Ok: { value: i32 } } fn main() { let value = Result::Ok{ item = 1 }; }`, want: "unknown enum variant literal field `item`"},
		{name: "payloadless braces", source: `enum Result { Pending } fn main() { let value = Result::Pending{}; }`, want: "payloadless enum variant `Pending` does not accept braces"},
		{name: "data without braces", source: `enum Result { Ok: { value: i32 } } fn main() { let value = Result::Ok; }`, want: "data enum variant `Ok` requires a braced field initializer"},
		{name: "missing generic arguments", source: `enum Result<T> { Pending } fn main() { let value = Result::Pending; }`, want: "expects 1 type argument, got 0"},
		{name: "variant call", source: `enum Result { Pending } fn main() { Result::Pending(); }`, want: "enum variants are not callable"},
		{name: "variant type", source: `enum Result { Pending } fn Read(value: Result::Pending) {}`, want: "not lowerable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diag := checkTypeSource(t, test.source)
			if out := diag.EmitAllToString(); !diag.HasErrors() || !strings.Contains(out, test.want) {
				t.Fatalf("expected %q diagnostic, got:\n%s", test.want, out)
			}
		})
	}
}

func TestNamedEnumMatchRecordsExhaustiveArmEvidence(t *testing.T) {
	module, diag := checkTypeModule(t, `enum Result {
	Ok: { value: i32, code: i32 },
	Error: { message: str },
	Pending
}

fn Read(result: Result) -> i32 {
	match result {
		Result::Ok{ value = payload } => {
			return payload;
		}
		Result::Error{ message = _ } => {
			return 1;
		}
		Result::Pending => {
			return 0;
		}
	}
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected match diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[1].(*ast.FnDecl)
	match := fn.Body.Stmts[0].(*ast.MatchStmt)
	evidence, found := module.Semantics.Matches[match.ID()]
	if !found || evidence.SubjectID != match.Subject.ID() || typeinfo.TypeText(evidence.EnumType) != "Result" || len(evidence.Arms) != 3 {
		t.Fatalf("match evidence = %#v", evidence)
	}
	if evidence.Arms[0].Case != 0 || len(evidence.Arms[0].Fields) != 1 || evidence.Arms[0].Fields[0].Field != 0 || evidence.Arms[0].Fields[0].Binding == nil {
		t.Fatalf("Ok arm evidence = %#v", evidence.Arms[0])
	}
	if evidence.Arms[1].Case != 1 || !evidence.Arms[1].Fields[0].Discard || evidence.Arms[2].Case != 2 {
		t.Fatalf("remaining arm evidence = %#v", evidence.Arms[1:])
	}
}

func TestNamedEnumMatchDiagnostics(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "missing case", source: `enum Result { Ok, Pending } fn Read(value: Result) { match value { Result::Ok => {} } }`, want: "match is missing case `Pending`"},
		{name: "duplicate case", source: `enum Result { Ok, Pending } fn Read(value: Result) { match value { Result::Ok => {} Result::Ok => {} Result::Pending => {} } }`, want: "duplicate match arm for `Ok`"},
		{name: "data without braces", source: `enum Result { Ok: { value: i32 } } fn Read(value: Result) { match value { Result::Ok => {} } }`, want: "data match case `Ok` requires braces"},
		{name: "payloadless braces", source: `enum Result { Pending } fn Read(value: Result) { match value { Result::Pending{} => {} } }`, want: "payloadless match case `Pending` does not accept braces"},
		{name: "duplicate field", source: `enum Result { Ok: { value: i32 } } fn Read(value: Result) { match value { Result::Ok{ value = first, value = second } => {} } }`, want: "duplicate match pattern field `value`"},
		{name: "unknown field", source: `enum Result { Ok: { value: i32 } } fn Read(value: Result) { match value { Result::Ok{ missing = item } => {} } }`, want: "unknown match pattern field `missing`"},
		{name: "foreign case", source: `enum Result { Ok } enum Other { Ok } fn Read(value: Result) { match value { Other::Ok => {} } }`, want: "match arm requires Result, got Other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diag := checkTypeSource(t, test.source)
			if out := diag.EmitAllToString(); !diag.HasErrors() || !strings.Contains(out, test.want) {
				t.Fatalf("expected %q diagnostic, got:\n%s", test.want, out)
			}
		})
	}
}

func hasTypeCode(diag *diagnostics.DiagnosticBag, code string) bool {
	if diag == nil {
		return false
	}
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}

func TestExplicitNumericLiteralConversionClasses(t *testing.T) {
	valid := checkTypeSource(t, `fn main() -> i32 {
	let min: i8 = -128i8;
	let wide_signed: u16 = 7i8;
	let wide_unsigned: i16 = 7u8;
	let wide_float: f64 = 2.4f32;
	let raw: byte = 65;
	let number: u8 = raw as u8;
	return (min as i32) + (wide_signed as i32) + (wide_unsigned as i32) + (wide_float as i32) + (number as i32);
}`)
	if valid.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", valid.EmitAllToString())
	}

	invalid := checkTypeSource(t, `fn main() -> i32 {
	let same_width_sign: u8 = 1i8;
	let cross_class: f32 = 1i8;
	let byte_number: u8 = 65 as byte;
	return 0;
}`)
	if !invalid.HasErrors() {
		t.Fatalf("expected explicit-cast diagnostics")
	}
}

func TestBitwiseOperatorsRequireIntegralOperands(t *testing.T) {
	valid := checkTypeSource(t, `fn main() -> i32 {
	let a: u8 = 12u8;
	let b: u8 = 10u8;
	let and: u8 = a & b;
	let or: u8 = a | b;
	let xor: u8 = a ^ b;
	let complement: u8 = ~a;
	let left: u8 = a << 2u8;
	let signed: i8 = -8i8;
	let right: i8 = signed >> 2i8;
	let wrapped_count: u8 = 1u8 << (255u8 + 1u8);
	let mixed_left: i64 = 1i64 << 3u8;
	let mixed_right: u8 = 128u8 >> 2u16;
	return 0;
}`)
	if valid.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", valid.EmitAllToString())
	}

	invalid := checkTypeSource(t, `fn main() -> i32 {
	let float_and = 1.0 & 2.0;
	let bool_or = true | false;
	let bool_not = ~true;
	let float_shift = 1i8 << 1.0;
	let bool_shift = 1i8 >> true;
	return 0;
}`)
	out := invalid.EmitAllToString()
	if !invalid.HasErrors() || !strings.Contains(out, "unsupported operand type for operator `&`") ||
		!strings.Contains(out, "unsupported operand type for operator `|`") ||
		!strings.Contains(out, "unsupported unary operand type for operator `~`") ||
		strings.Count(out, "shift count must be integral") != 2 {
		t.Fatalf("expected integral-only diagnostics, got:\n%s", out)
	}
}

func TestBitwiseShiftRejectsConstantCountOutsideTypeWidth(t *testing.T) {
	diag := checkTypeSource(t, `fn main() -> i32 {
	let negative: i8 = 1i8 << -1i8;
	let too_wide: u8 = 1u8 >> 8u8;
	return 0;
}`)
	out := diag.EmitAllToString()
	if !diag.HasErrors() || strings.Count(out, "shift count must be between 0 and 7") != 2 {
		t.Fatalf("expected checked shift-count diagnostics, got:\n%s", out)
	}
}

func TestByteTypeIsLowerableInFunctionSignature(t *testing.T) {
	diag := checkTypeSource(t, `fn identity(value: byte) -> byte {
	return value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReceiverFunctionRejectsBuiltinTarget(t *testing.T) {
	src := `fn (self: i32) abs() -> i32 {
		return self;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidMethodReceiver) {
		t.Fatalf("expected invalid receiver diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestNoneAssignsToOptional(t *testing.T) {
	src := `fn main() {
	let x: ?i32 = none;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestNumberAssignsToOptional(t *testing.T) {
	src := `fn main() {
	let x: ?i32 = 7;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestNoneRejectedForNonOptional(t *testing.T) {
	src := `fn main() {
	let x: i32 = none;
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "`none` requires optional context") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestNoneRejectedWithoutExpectedType(t *testing.T) {
	src := `fn main() {
	let x = none;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidExpression) {
		t.Fatalf("expected none context diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestOptionalCompareWithNoneAccepted(t *testing.T) {
	src := `fn main() -> i32 {
	let x: ?i32 = none;
	if x == none {
		return 0;
	}
	return 1;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestStringBuiltinAcceptedInTypedBinding(t *testing.T) {
	src := `fn main() {
	let name: string;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestUnknownTypeAttributeRejected(t *testing.T) {
	src := `#[weird]
struct Buffer {
	ptr: *u8
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "unknown attribute") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestObsoleteCopyAttributesRejected(t *testing.T) {
	src := `#[no_copy]
#[allow_copy]
struct Buffer {
	ptr: *u8
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "unknown attribute `#[no_copy]`") || !strings.Contains(out, "unknown attribute `#[allow_copy]`") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDuplicateAttributeRejected(t *testing.T) {
	src := `#[extern("puts")]
#[extern("printf")]
fn puts(msg: cstr) -> i32;
`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "duplicate attribute `#[extern]`") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDuplicateAttributePointsAtRepeatedAttribute(t *testing.T) {
	src := `#[target_os("linux")]
#[target_os("linux")]
struct Buffer {
	value: i32
}
`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "previous attribute here") || !strings.Contains(out, "2 | ") {
		t.Fatalf("expected duplicate diagnostic on repeated attribute, got:\n%s", out)
	}
}

func TestInvalidAttributeShapeRejected(t *testing.T) {
	src := `#[extern(123)]
fn ext();
`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "invalid arguments for attribute `#[extern]`") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestAttributeRejectsConstBindingArgument(t *testing.T) {
	src := `const name: cstr = c"puts";

#[extern(name)]
fn puts(msg: cstr) -> i32;
`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "invalid arguments for attribute `#[extern]`") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestAttributeRejectsNonConstantBindingArgument(t *testing.T) {
	src := `fn symbol_name() -> cstr { return c"puts"; }
const name: cstr = symbol_name();

#[extern(name)]
fn puts(msg: cstr) -> i32;
`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "invalid arguments for attribute `#[extern]`") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestInvalidAttributeTargetRejected(t *testing.T) {
	src := `#[extern]
struct Buffer {}
`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "attribute `#[extern]` cannot be used on this declaration") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestUnknownNoMangleAttributeRejected(t *testing.T) {
	src := `#[no_mangle]
fn ext();
`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "unknown attribute `#[no_mangle]`") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMalformedTargetOSStillReportsInvalidAttribute(t *testing.T) {
	src := `#[target_os(123)]
fn disabled() -> i32 {
	return missing_name;
}
`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "invalid arguments for attribute `#[target_os]`") {
		t.Fatalf("expected invalid target_os diagnostic, got:\n%s", out)
	}
	if hasTypeCode(diag, diagnostics.WarnIgnoredTargetOS) {
		t.Fatalf("did not expect target_os warning for malformed attribute, got:\n%s", out)
	}
}

func TestValidTargetOSWarnsAndDoesNotFilterSemantics(t *testing.T) {
	src := `#[target_os("linux")]
struct Buffer {
	value: i32
}

#[target_os("darwin")]
fn disabled() -> i32 {
	return missing_name;
}

	#[target_os("linux")]
	fn (self: Buffer) disabled() -> i32 {
		return missing_method;
	}
`
	diag := checkTypeSource(t, src)
	out := diag.EmitAllToString()
	if !diag.HasErrors() {
		t.Fatalf("expected unresolved names because target_os is ignored")
	}
	if !strings.Contains(out, "missing_name") || !strings.Contains(out, "missing_method") {
		t.Fatalf("expected ignored target_os to keep bodies active, got:\n%s", out)
	}
	count := 0
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.WarnIgnoredTargetOS {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("expected 3 target_os warnings, got %d:\n%s", count, out)
	}
}

func TestImplMethodAttributesValidated(t *testing.T) {
	src := `struct Buffer {
	value: i32
}

	#[target_oz("linux")]
	fn (self: Buffer) bad() {}
`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "unknown attribute `#[target_oz]`") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDuplicateImplMethodAttributesRejected(t *testing.T) {
	src := `struct Buffer {
	value: i32
}

	#[target_os("linux")]
	#[target_os("linux")]
	fn (self: Buffer) bad() {}
`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "duplicate attribute `#[target_os]`") || !strings.Contains(out, "previous attribute here") {
		t.Fatalf("unexpected diagnostics:\n%s", out)
	}
	if hasTypeCode(diag, diagnostics.WarnIgnoredTargetOS) {
		t.Fatalf("did not expect target_os warning for duplicate attributes, got:\n%s", out)
	}
}

func TestExternDefinitionRejectedButBodyRemainsTyped(t *testing.T) {
	src := `#[extern("puts")]
fn puts(msg: cstr) -> i32 {
	return missing_name;
}
`
	diag := checkTypeSource(t, src)
	out := diag.EmitAllToString()
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(out, "attribute `#[extern]` requires a body-less function declaration") {
		t.Fatalf("expected extern definition diagnostic, got:\n%s", out)
	}
	if !strings.Contains(out, "remove body to declare extern function") || !strings.Contains(out, "remove `#[extern]` to keep local definition") {
		t.Fatalf("expected extern definition help text, got:\n%s", out)
	}
	if !strings.Contains(out, "missing_name") {
		t.Fatalf("expected function body to keep typechecking, got:\n%s", out)
	}
}

func TestImplicitBindingTransfersNoCopyValue(t *testing.T) {
	diag := checkTypeSource(t, `struct Buffer {
	ptr: *u8
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	let next = current;
	destroy(next);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReassignmentClearsMovedLocal(t *testing.T) {
	diag := checkTypeSource(t, `struct Buffer {
	value: i32
}

fn destroy(data: Buffer) {}

fn main() {
	let mut current: Buffer = .{ value = 1 };
	destroy(current);
	current = .{ value = 2 };
	destroy(current);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}
func TestReceiverDiagnosticUsesReceiverTypeSite(t *testing.T) {
	diag := checkTypeSource(t, `struct Counter {
	value: i32
}

	fn (value: i32) bump() {}
`)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	var targetDiag *diagnostics.Diagnostic
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrInvalidMethodReceiver {
			targetDiag = item
			break
		}
	}
	if targetDiag == nil || len(targetDiag.Labels) == 0 || targetDiag.Labels[0].Location == nil || targetDiag.Labels[0].Location.Start == nil || targetDiag.Labels[0].Location.End == nil {
		t.Fatalf("missing receiver diagnostic location: %#v", targetDiag)
	}
	if targetDiag.Labels[0].Location.Start.Line != 5 || targetDiag.Labels[0].Location.Start.Column != 13 {
		t.Fatalf("primary label start = %v, want line 5 col 13", *targetDiag.Labels[0].Location.Start)
	}
	if targetDiag.Labels[0].Location.End.Line != 5 || targetDiag.Labels[0].Location.End.Column != 16 {
		t.Fatalf("primary label end = %v, want line 5 col 16", *targetDiag.Labels[0].Location.End)
	}
}

func TestMutablePointerFieldDefaultsTypeToNoCopy(t *testing.T) {
	module, diag := checkTypeModule(t, `struct Buffer {
	ptr: *u8
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	sym, ok := module.ModuleScope.LookupLocal("Buffer")
	if !ok || sym == nil {
		t.Fatalf("missing Buffer symbol")
	}
	typ, ok := symbols.GetSymbolType(sym)
	if !ok || typ == nil {
		t.Fatalf("missing Buffer type")
	}
	if !typeinfo.IsNoCopyType(typ) {
		t.Fatalf("Buffer should default to no-copy")
	}
}

func TestAddressOfMutableLocalAssignsMutablePointer(t *testing.T) {
	diag := checkTypeSource(t, `fn main() -> i32 {
	let mut value: i32 = 1;
	let ptr: rawptr = @value;
	return 0;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestAddressOfImmutableLocalAssignsConstPointer(t *testing.T) {
	diag := checkTypeSource(t, `fn main() -> i32 {
	let value: i32 = 1;
	let ptr: rawptr = @value;
	return 0;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestAddressOfImmutableLocalRejectsMutablePointer(t *testing.T) {
	diag := checkTypeSource(t, `fn main() -> i32 {
	let value: i32 = 1;
	let ptr: *i32 = @value;
	return 0;
}`)
	if !hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
		t.Fatalf("expected type mismatch diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestAddressOperatorRequiresAddressableStorage(t *testing.T) {
	diag := checkTypeSource(t, `fn main() -> i32 {
	let value: i32 = 1;
	let ptr = @(value + 1);
	return 0;
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidExpression) {
		t.Fatalf("expected invalid expression diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestRawPointerFieldStructSupportsExplicitCopy(t *testing.T) {
	module, diag := checkTypeModule(t, `struct View {
	ptr: rawptr,
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	sym, ok := module.ModuleScope.LookupLocal("View")
	if !ok || sym == nil {
		t.Fatalf("missing View symbol")
	}
	typ, ok := symbols.GetSymbolType(sym)
	if !ok || typ == nil {
		t.Fatalf("missing View type")
	}
	if typeinfo.IsImplicitCopyType(typ) || typeinfo.IsNoCopyType(typ) {
		t.Fatalf("View should implicitly and support explicit copy")
	}
}

func TestReceiverFunctionAllowsAnyReceiverName(t *testing.T) {
	src := `struct Number { value: i32 }
fn (value: Number) abs() -> Number {
		return value;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestIfaceAndReceiverFunctionMatch(t *testing.T) {
	src := `iface Reader {
	fn (&Self) read(buf: cstr) -> i32
}

struct File {}

	fn (self: &File) read(buf: cstr) -> i32 {
		return 0;
	}
`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestSelfOutsideIfaceReceiverIsRejected(t *testing.T) {
	src := `fn bad(value: Self) -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) {
		t.Fatalf("expected invalid type diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReceiverFunctionCallResolves(t *testing.T) {
	src := `struct Number { value: i32 }
fn (self: Number) abs() -> Number {
		return self;
}

fn main() -> i32 {
	let x: Number = .{ value = 1 };
	return x.abs().value;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestFunctionCallResolvesAcrossDeclarationOrder(t *testing.T) {
	src := `fn main() -> i32 {
	return later();
}

fn later() -> i32 {
	return 7;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestImportedFunctionCallKeepsExplicitBindingType(t *testing.T) {
	src := `import "external";

fn main() -> i32 {
	let myval: i32 = external::GetValue();
	return myval;
}`
	module, diag := checkTypeSourceWithExternalImport(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	sym, ok := module.ModuleScope.Lookup("main")
	if !ok || sym == nil || sym.Scope == nil {
		t.Fatalf("expected main function scope")
	}
	funcScope := sym.Scope
	myval, ok := funcScope.LookupLocal("myval")
	if !ok || myval == nil {
		t.Fatalf("expected myval local symbol")
	}
	got, ok := symbols.GetSymbolType(myval)
	if !ok || got == nil {
		t.Fatalf("expected myval type")
	}
	if got.Text() != "i32" {
		t.Fatalf("myval type = %q, want i32", got.Text())
	}
}

func TestExplicitBindingTypeSurvivesImportedInitializerMismatch(t *testing.T) {
	src := `import "external";

fn main() -> i32 {
	let myval: bool = external::GetValue();
	return 0;
}`
	module, diag := checkTypeSourceWithExternalImport(t, src)
	if !hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
		t.Fatalf("expected type mismatch diagnostic, got:\n%s", diag.EmitAllToString())
	}
	sym, ok := module.ModuleScope.Lookup("main")
	if !ok || sym == nil || sym.Scope == nil {
		t.Fatalf("expected main function scope")
	}
	funcScope := sym.Scope
	myval, ok := funcScope.LookupLocal("myval")
	if !ok || myval == nil {
		t.Fatalf("expected myval local symbol")
	}
	got, ok := symbols.GetSymbolType(myval)
	if !ok || got == nil {
		t.Fatalf("expected myval type")
	}
	if got.Text() != "bool" {
		t.Fatalf("myval type = %q, want bool", got.Text())
	}
}

func TestTopLevelConstInitializerRejectsLaterBinding(t *testing.T) {
	src := `const first: i32 = second;
const second: i32 = 2;

fn main() -> i32 {
	return second;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrUseBeforeDecl) {
		t.Fatalf("expected use-before-declaration diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestFunctionBodySeesLaterTopLevelBinding(t *testing.T) {
	src := `fn main() -> i32 {
	return answer;
}

const answer: i32 = 42;`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestStructFieldAccessResolves(t *testing.T) {
	src := `struct Point {
		x: i32
	}

	fn main() -> i32 {
		let p: Point = .{ x = 1 };
		return p.x;
	}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestUnknownMemberSuggestsOnlyHighConfidenceMatch(t *testing.T) {
	src := `struct Point {
		length: i32
	}

	fn main(p: Point) -> i32 {
		return p.lenght;
	}`
	diag := checkTypeSource(t, src)
	out := diag.EmitAllToString()
	if !strings.Contains(out, "did you mean `length`?") {
		t.Fatalf("expected confident member suggestion, got:\n%s", out)
	}
}

func TestUnknownMemberSuppressesAmbiguousSuggestion(t *testing.T) {
	src := `struct Point {
		cost: i32
		count: i32
	}

	fn main(p: Point) -> i32 {
		return p.cotn;
	}`
	diag := checkTypeSource(t, src)
	out := diag.EmitAllToString()
	if strings.Contains(out, "did you mean") {
		t.Fatalf("expected ambiguous member suggestion to be suppressed, got:\n%s", out)
	}
}

func TestPointerRecursiveStructBindingResolves(t *testing.T) {
	src := `struct Node {
	next: *Node,
}

#[extern]
fn next_node() -> *Node;

fn main() -> i32 {
	let node: Node = .{ next = next_node() };
	let next: *Node = node.next;
	return 0;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestArrayIndexExprReturnsElementType(t *testing.T) {
	src := `fn first(xs: [4]i32) -> i32 {
	return xs[0];
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestArrayIndexExprRejectsConstantOutOfBounds(t *testing.T) {
	src := `fn first(xs: [4]i32) -> i32 {
	return xs[4];
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrArrayOutOfBounds) {
		t.Fatalf("expected array out-of-bounds diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestArrayIndexExprRejectsTopLevelConstOutOfBounds(t *testing.T) {
	src := `const I: i32 = 4;

fn first(xs: [4]i32) -> i32 {
	return xs[I];
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrArrayOutOfBounds) {
		t.Fatalf("expected array out-of-bounds diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestArrayIndexExprAcceptsRuntimeIndex(t *testing.T) {
	src := `fn first(xs: [4]i32, i: i32) -> i32 {
	return xs[i];
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayIndexExprReturnsElementType(t *testing.T) {
	src := `fn first(xs: []i32, i: usize) -> i32 {
	return xs[i];
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestRecursiveDynamicArrayParameterRejectedWithoutRecursingForever(t *testing.T) {
	src := `struct Node {
	children: []Node
}

fn count(node: Node) -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) {
		t.Fatalf("expected recursive runtime type diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "requires a sized type") {
		t.Fatalf("expected recursive sizedness diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestBorrowedViewComparisonsRejectedBeforeLowering(t *testing.T) {
	for _, view := range []struct {
		name     string
		typeText string
		message  string
	}{
		{name: "slice", typeText: "&[..]i32", message: "slice-view comparison is not supported"},
		{name: "string", typeText: "&str", message: "string-view comparison is not supported"},
	} {
		for _, op := range []string{"==", "!=", "<", "<=", ">", ">="} {
			t.Run(view.name+"/"+op, func(t *testing.T) {
				src := "fn compare(left: " + view.typeText + ", right: " + view.typeText + ") -> bool { return left " + op + " right; }"
				diag := checkTypeSource(t, src)
				if !hasTypeCode(diag, diagnostics.ErrInvalidOperation) {
					t.Fatalf("expected %s comparison diagnostic, got:\n%s", view.name, diag.EmitAllToString())
				}
				if !strings.Contains(diag.EmitAllToString(), view.message) {
					t.Fatalf("expected %s comparison limitation, got:\n%s", view.name, diag.EmitAllToString())
				}
			})
		}
	}
}

func TestNestedReferenceParameterRejected(t *testing.T) {
	src := `fn direct(value: &mut &i32) -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) {
		t.Fatalf("expected nested reference diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestAliasedNestedReferenceParameterRejected(t *testing.T) {
	src := `type Shared = &i32;

fn aliased(value: &mut Shared) -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) {
		t.Fatalf("expected aliased nested reference diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestSliceViewIndexExprReturnsElementType(t *testing.T) {
	src := `fn first(xs: &[..]i32, i: usize) -> i32 {
	return xs[i];
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMutableSliceViewIndexAssignmentAccepted(t *testing.T) {
	src := `fn fill(xs: &mut [..]i32, i: usize) {
	xs[i] = 1;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestIndexedElementReferencesAllowed(t *testing.T) {
	src := `struct Token { value: i32 }
fn inspect(_: &Token) {}
fn update(_: &mut Token) {}
fn keep(_: rawptr) {}

fn use(mut values: []Token) {
	inspect(&values[0]);
	update(&mut values[0]);
	keep(@values[0]);
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected indexed reference diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestSharedSliceViewRejectsMutableIndexedReference(t *testing.T) {
	src := `struct Token { value: i32 }
fn invalid(values: &[..]Token) {
	let reference = &mut values[0];
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidExpression) {
		t.Fatalf("expected shared-view mutable reference diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "value is behind an immutable reference") {
		t.Fatalf("unexpected shared-view mutable reference diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestRawAddressRejectsRangeTemporary(t *testing.T) {
	src := `fn keep(_: rawptr) {}
fn invalid(values: []i32) {
	keep(@values[..]);
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidExpression) {
		t.Fatalf("expected range-address diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "address operator requires addressable storage") {
		t.Fatalf("unexpected range-address diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestSharedSliceViewRejectsIndexAssignment(t *testing.T) {
	src := `fn fill(xs: &[..]i32, i: usize) {
	xs[i] = 1;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) {
		t.Fatalf("expected shared-view mutation diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "index assignment requires mutable array or mutable slice view") {
		t.Fatalf("unexpected shared-view mutation diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestRangeExprCreatesSliceViews(t *testing.T) {
	src := `fn ranges(fixed: [4]i32, owner: []i32, shared: &[..]i32, mutable: &mut [..]i32) {
	let prefix = fixed[..2];
	let suffix = owner[1..];
	let full = shared[..];
	let inclusive = mutable[1..=2];
	inclusive[0] = 9;
}

fn mutate(mut fixed: [4]i32) {
	let middle = fixed[1..3];
	middle[0] = 7;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected range slicing diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestRangeExprRejectsNonAddressableFixedArray(t *testing.T) {
	src := `fn invalid() {
	let _ = [_]i32{1, 2, 3}[..];
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidExpression) {
		t.Fatalf("expected addressable slicing diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "slicing requires addressable array storage") {
		t.Fatalf("unexpected addressable slicing diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestRangeExprRejectsNonIntegerBounds(t *testing.T) {
	src := `fn first(xs: [4]i32, flag: bool) -> &[..]i32 {
	return xs[flag..3];
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidOperation) {
		t.Fatalf("expected invalid range bound diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestUnsizedArrayTypeRequiresReference(t *testing.T) {
	src := `fn first(xs: [i32]) -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidTypeInParser) {
		t.Fatalf("expected parser diagnostic for unsupported [T], got:\n%s", diag.EmitAllToString())
	}
}

func TestIndexExprRejectsNonIntegerIndex(t *testing.T) {
	src := `fn first(xs: [4]i32, flag: bool) -> i32 {
	return xs[flag];
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidOperation) {
		t.Fatalf("expected invalid index diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestIndexExprRejectsFloatPostfixBeforeConstEvaluation(t *testing.T) {
	src := `fn first(xs: [4]i32) -> i32 {
	return xs[1f32];
}`
	module, diag := checkTypeModule(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidOperation) {
		t.Fatalf("expected invalid index diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if hasTypeCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("invalid index reached ownership analysis:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	ret := fn.Body.Stmts[0].(*ast.ReturnStmt)
	index := ret.Value.(*ast.IndexExpr)
	if !typeinfo.IsInvalidOrUnknown(module.Semantics.ExprTypes[index.ID()]) {
		t.Fatalf("index expression should have invalid semantic type")
	}
}

func TestIndexExprRejectsNonIndexableBase(t *testing.T) {
	src := `fn first(x: i32) -> i32 {
	return x[0];
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidExpression) {
		t.Fatalf("expected invalid base diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestArrayIndexAssignmentRequiresMutableBinding(t *testing.T) {
	src := `fn main() {
	let xs = [_]i32{1, 2, 3, 4};
	xs[0] = 1;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) {
		t.Fatalf("expected invalid assignment diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "index assignment requires mutable array or slice binding") {
		t.Fatalf("expected mutability diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestArrayIndexAssignmentOnMutableBindingAccepted(t *testing.T) {
	src := `fn main() {
	let mut xs = [_]i32{1, 2, 3, 4};
	xs[0] = 1;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestArrayLiteralTypechecksExplicitLength(t *testing.T) {
	src := `fn main() {
	let arr = [3]i32{1, 2, 3};
}`
	module, diag := checkTypeModule(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	letDecl := fn.Body.Stmts[0].(*ast.LetDecl)
	got := module.Semantics.ExprTypes[letDecl.Value.ID()]
	if typeinfo.TypeText(got) != "[3]i32" {
		t.Fatalf("array literal type = %s, want [3]i32", typeinfo.TypeText(got))
	}
}

func TestArrayLiteralTypechecksInferredLength(t *testing.T) {
	src := `fn main() {
	let arr = [_]i32{1, 2, 3};
}`
	module, diag := checkTypeModule(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	letDecl := fn.Body.Stmts[0].(*ast.LetDecl)
	got := module.Semantics.ExprTypes[letDecl.Value.ID()]
	if typeinfo.TypeText(got) != "[3]i32" {
		t.Fatalf("array literal type = %s, want [3]i32", typeinfo.TypeText(got))
	}
}

func TestArrayLiteralTypechecksDynamicArray(t *testing.T) {
	src := `fn main() {
	let arr = []i32{1, 2, 3};
}`
	module, diag := checkTypeModule(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	letDecl := fn.Body.Stmts[0].(*ast.LetDecl)
	got := module.Semantics.ExprTypes[letDecl.Value.ID()]
	if typeinfo.TypeText(got) != "[]i32" {
		t.Fatalf("array literal type = %s, want []i32", typeinfo.TypeText(got))
	}
}

func TestDynamicArrayOwnerOperationsTypecheck(t *testing.T) {
	src := `fn main() {
	let mut values = []i32{};
	append(&mut values, 1);
	values |> reserve(8);
	values |> resize(4, 0);
	values |> shrink(2);
}`
	module, diag := checkTypeModule(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	wantParams := [][]string{
		{"&mut []i32", "i32"},
		{"&mut []i32", fmt.Sprintf("u%d", target.Host().IndexBits)},
		{"&mut []i32", fmt.Sprintf("u%d", target.Host().IndexBits), "i32"},
		{"&mut []i32", fmt.Sprintf("u%d", target.Host().IndexBits)},
	}
	for i, stmt := range fn.Body.Stmts[1:] {
		call := stmt.(*ast.ExprStmt).Expr.(*ast.CallExpr)
		fnType, ok := module.Semantics.ExprTypes[call.Callee.ID()].(*typeinfo.FuncType)
		if !ok {
			t.Fatalf("operation %d callee type = %#v, want function", i, module.Semantics.ExprTypes[call.Callee.ID()])
		}
		if fnType.Return != nil {
			t.Fatalf("operation %d return = %s, want void", i, typeinfo.TypeText(fnType.Return))
		}
		if len(fnType.Params) != len(wantParams[i]) {
			t.Fatalf("operation %d parameter count = %d, want %d", i, len(fnType.Params), len(wantParams[i]))
		}
		for paramIndex, want := range wantParams[i] {
			if got := typeinfo.TypeText(fnType.Params[paramIndex]); got != want {
				t.Fatalf("operation %d parameter %d = %s, want %s", i, paramIndex, got, want)
			}
		}
	}
}

func TestDynamicArrayShrinkAcceptsMoveOnlyElements(t *testing.T) {
	diag := checkTypeSource(t, `struct Point { x: i32 }
fn main() {
	let mut points = []Point{.Point{x = 1}};
	points |> shrink(0);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected move-only shrink diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayOwnerOperationRequiresOwner(t *testing.T) {
	diag := checkTypeSource(t, `fn extend(values: &[..]i32) {
	append(values, 1);
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) ||
		!strings.Contains(diag.EmitAllToString(), "requires a dynamic-array owner") {
		t.Fatalf("expected owner diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayOwnerOperationRequiresMutableBinding(t *testing.T) {
	diag := checkTypeSource(t, `fn main() {
	let values = []i32{};
	values |> append(1);
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) {
		t.Fatalf("expected immutable-owner diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayOwnerOperationDoesNotProduceValue(t *testing.T) {
	diag := checkTypeSource(t, `fn main() {
	let mut values = []i32{};
	let result = values |> append(1);
}`)
	if !strings.Contains(diag.EmitAllToString(), "initializer requires a value-producing expression") {
		t.Fatalf("expected void-operation diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayResizeRejectsMoveOnlyElements(t *testing.T) {
	diag := checkTypeSource(t, `struct Point { x: i32 }
fn main() {
	let mut points = []Point{};
	points |> resize(2, .Point{x = 0});
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidCopy) ||
		!strings.Contains(diag.EmitAllToString(), "grow Category B arrays with append") {
		t.Fatalf("expected move-only resize diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestUserFunctionShadowsDynamicArrayOwnerOperation(t *testing.T) {
	diag := checkTypeSource(t, `fn append(left: i32, right: i32) -> i32 {
	return left + right;
}
fn main() -> i32 {
	return append(20, 22);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected shadowing diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayShrinkRequiresOwner(t *testing.T) {
	diag := checkTypeSource(t, `fn shorten(values: &[..]i32) {
	shrink(values, 1);
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) ||
		!strings.Contains(diag.EmitAllToString(), "requires a dynamic-array owner") {
		t.Fatalf("expected shrink owner diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayLiteralRejectsReferenceElementWithoutBinding(t *testing.T) {
	diag := checkTypeSource(t, `fn main() {
	let value = 1;
	[]&i32{&value};
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) ||
		!strings.Contains(diag.EmitAllToString(), "references cannot be stored in dynamic arrays") {
		t.Fatalf("expected stored-reference diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayLiteralRejectsNonLowerableElement(t *testing.T) {
	diag := checkTypeSource(t, `fn main() {
	[]void{};
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) ||
		!strings.Contains(diag.EmitAllToString(), "dynamic array element type is not lowerable") {
		t.Fatalf("expected non-lowerable element diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestAllocTypecheck(t *testing.T) {
	src := `fn main() {
	let x = 42;
	let p = alloc(x);
}`
	module, diag := checkTypeModule(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	letDecl := fn.Body.Stmts[1].(*ast.LetDecl)
	if got := typeinfo.TypeText(module.Semantics.ExprTypes[letDecl.Value.ID()]); got != "*i32" {
		t.Fatalf("alloc type = %s, want *i32", got)
	}
}

func TestAllocAcceptsAllocatorParameterAndEquality(t *testing.T) {
	diag := checkTypeSource(t, `fn make(value: i32, allocator: Allocator) -> *i32 {
	let same = allocator == allocator;
	if same {
		return alloc(value, allocator);
	}
	return alloc(value);
}
fn main() {}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected allocator diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestAllocRejectsStoredReference(t *testing.T) {
	diag := checkTypeSource(t, `fn main() {
	let value = 1;
	let p = alloc(&value);
}`)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for alloc with stored reference, got none")
	}
}

func TestArrayLiteralRejectsWrongExplicitLength(t *testing.T) {
	src := `fn main() {
	let arr = [3]i32{1, 2};
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
		t.Fatalf("expected explicit length mismatch, got:\n%s", diag.EmitAllToString())
	}
}

func TestArrayLiteralRejectsFloatPostfixLengthBeforeElementChecks(t *testing.T) {
	src := `fn main() {
	let arr = [3f64]i32{1, true, 3};
}`
	module, diag := checkTypeModule(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) {
		t.Fatalf("expected invalid array length diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
		t.Fatalf("invalid array literal continued into element checks:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	letDecl := fn.Body.Stmts[0].(*ast.LetDecl)
	if !typeinfo.IsInvalidOrUnknown(module.Semantics.ExprTypes[letDecl.Value.ID()]) {
		t.Fatalf("array literal should have invalid semantic type")
	}
}

func TestArrayTypeRejectsFloatPostfixLength(t *testing.T) {
	diag := checkTypeSource(t, `fn first(xs: [3f32]i32) -> i32 {
	return 0;
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) {
		t.Fatalf("expected invalid array length diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestArrayLiteralRejectsWrongElementType(t *testing.T) {
	src := `fn main() {
	let arr = [2]i32{1, true};
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
		t.Fatalf("expected element type mismatch, got:\n%s", diag.EmitAllToString())
	}
}

func TestDirectStructCycleIsRejected(t *testing.T) {
	src := `struct A {
	b: B,
}

struct B {
	a: A,
}

fn main() -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrCircularDependency) {
		t.Fatalf("expected circular dependency diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestSelfRecursiveStructValueIsRejected(t *testing.T) {
	src := `struct Node {
	next: Node
}

fn main() -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrCircularDependency) {
		t.Fatalf("expected circular dependency diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestSelfRecursiveStructThroughPointersAccepted(t *testing.T) {
	src := `struct OwnedNode {
	next: *OwnedNode
}

struct RawNode {
	next: *RawNode
}

fn main() -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestRecursiveTypeAliasIsRejected(t *testing.T) {
	src := `type Loop = Loop;

fn main() -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrCircularDependency) {
		t.Fatalf("expected circular dependency diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestOwnedInterfaceAssignmentAcceptsDirectCarrier(t *testing.T) {
	src := `iface Reader {
	fn (&Self) read(buf: cstr) -> i32
}

struct File {}

	fn (self: &File) read(buf: cstr) -> i32 {
		return 0;
	}

fn main(file: *File) -> i32 {
	let reader: *Reader = file;
	return reader.read(c"ok");
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected owned-interface conversion diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestOwnedInterfaceAssignmentRejectsStackValue(t *testing.T) {
	src := `iface Reader {
	fn (&Self) read(buf: cstr) -> i32
}

struct File {}

	fn (self: &File) read(buf: cstr) -> i32 {
		return 0;
	}

fn main() -> i32 {
	let file: File = .{};
	let reader: *Reader = file;
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
		t.Fatalf("expected type mismatch diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReferenceSelfInterfaceAssignmentAcceptsBorrow(t *testing.T) {
	src := `iface Reader {
	fn (&Self) read() -> i32
}

struct Counter {
	value: i32
}

	fn (self: &Counter) read() -> i32 {
		return self.value;
	}

fn main() -> i32 {
	let counter: Counter = .{ value = 7 };
	let reader: &Reader = &counter;
	return reader.read();
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestBorrowedInterfaceArgumentsAcceptConcreteReferences(t *testing.T) {
	src := `iface Reader {
	fn (&Self) read() -> i32
}

iface Writer {
	fn (&mut Self) write(value: i32)

}
struct Counter {
	value: i32
}

	fn (self: &Counter) read() -> i32 {
		return self.value;
	}

	fn (self: &mut Counter) write(value: i32) {
		self.value = value;
	}

fn read(reader: &Reader) -> i32 {
	return reader.read();
}

fn write(writer: &mut Writer) {
	writer.write(7);
}

fn main() -> i32 {
	let mut counter: Counter = .{ value = 5 };
	write(&mut counter);
	return read(&counter);
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMutableReferenceExpressionRejectsImmutableStorage(t *testing.T) {
	src := `fn main() -> i32 {
	let value: i32 = 5;
	let reference = &mut value;
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidExpression) ||
		!strings.Contains(diag.EmitAllToString(), "mutable reference requires mutable addressable storage") {
		t.Fatalf("expected mutable borrow diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestBareInterfaceRejectsBorrowedConcreteValue(t *testing.T) {
	src := `iface Reader {
	fn (&Self) read() -> i32
}

struct Counter {
	value: i32
}

	fn (self: &Counter) read() -> i32 {
		return self.value;
	}

fn main() -> i32 {
	let counter: Counter = .{ value = 5 };
	let reader: Reader = &counter;
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) || !strings.Contains(diag.EmitAllToString(), "unsized") {
		t.Fatalf("expected bare interface sizedness diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMutableInterfaceReceiverRejectsSharedBorrow(t *testing.T) {
	src := `iface Writer {
	fn (&mut Self) write(value: i32)
}

fn write(writer: &Writer) {
	writer.write(7);
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrTypeMismatch) ||
		!strings.Contains(diag.EmitAllToString(), "cannot implicitly convert &Writer to &mut Writer") {
		t.Fatalf("expected mutable interface mismatch, got:\n%s", diag.EmitAllToString())
	}
}

func TestMutableInterfaceBorrowSupportsReceiverCall(t *testing.T) {
	src := `iface Writer {
	fn (&mut Self) write(value: i32)
}

fn write(writer: &mut Writer, mut value: i32) -> i32 {
	writer.write(value);
	value = 9;
	return value;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestIfaceAllowsConsumingSelfReceiver(t *testing.T) {
	diag := checkTypeSource(t, `iface Reader { fn (Self) read() -> i32 }`)
	if diag.HasErrors() {
		t.Fatalf("unexpected consuming receiver diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestConsumingInterfaceMethodRequiresOwnedCarrier(t *testing.T) {
	invalid := checkTypeSource(t, `iface Consumer { fn (Self) consume() }
fn bad(value: &Consumer) { value.consume(); }`)
	if !hasTypeCode(invalid, diagnostics.ErrTypeMismatch) {
		t.Fatalf("expected borrowed consuming receiver mismatch, got:\n%s", invalid.EmitAllToString())
	}
	valid := checkTypeSource(t, `iface Consumer { fn (Self) consume() }
fn take(value: *Consumer) { value.consume(); }`)
	if valid.HasErrors() {
		t.Fatalf("unexpected owned consuming receiver diagnostic:\n%s", valid.EmitAllToString())
	}
}

func TestMutableInterfaceCarrierCallsSharedReceiver(t *testing.T) {
	diag := checkTypeSource(t, `iface Reader { fn (&Self) read() -> i32 }
fn read(value: &mut Reader) -> i32 { return value.read(); }`)
	if diag.HasErrors() {
		t.Fatalf("unexpected shared receiver diagnostic on mutable carrier:\n%s", diag.EmitAllToString())
	}
}

func TestOwnedInterfaceAssignmentAcceptsNestedOwnerTarget(t *testing.T) {
	diag := checkTypeSource(t, `iface Reader { fn (&Self) read() -> i32 }
struct Resource { child: *i32 }
fn (self: &Resource) read() -> i32 { return 0; }
fn bad(resource: *Resource) { let reader: *Reader = resource; }`)
	if diag.HasErrors() {
		t.Fatalf("unexpected nested-owner erasure diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestFreeAcceptsOwnedPointers(t *testing.T) {
	valid := checkTypeSource(t, `iface Reader { fn (&Self) read() -> i32 }
fn release_value(value: *i32) { free(value); }
fn release_interface(reader: *Reader) { free(reader); }`)
	if valid.HasErrors() {
		t.Fatalf("unexpected free diagnostics:\n%s", valid.EmitAllToString())
	}
	invalid := checkTypeSource(t, `fn bad(raw: rawptr, value: i32) { free(raw); free(value); }`)
	if count := strings.Count(invalid.EmitAllToString(), "free requires an owned pointer"); count != 2 {
		t.Fatalf("expected two invalid free diagnostics, got %d:\n%s", count, invalid.EmitAllToString())
	}
}

func TestPrintRejectsIntegersWiderThan64Bits(t *testing.T) {
	diag := checkTypeSource(t, `fn show(value: u128) { print(value); }`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) ||
		!strings.Contains(diag.EmitAllToString(), "print supports integers up to 64 bits") {
		t.Fatalf("expected print width diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestPrintAcceptsPrimitiveScalars(t *testing.T) {
	diag := checkTypeSource(t, `fn show(mut value: i32, raw: rawptr) {
	print(value);
	print(42u32);
	print(2.5f64);
	print(true);
	print(value as byte);
	print(c"hello");
	print(raw);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected print diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPrintRejectsComposite(t *testing.T) {
	diag := checkTypeSource(t, `struct Point { x: i32 }
fn show(point: Point) { print(point); }`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) || !strings.Contains(diag.EmitAllToString(), "print requires a primitive scalar") {
		t.Fatalf("expected invalid print operand diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestImmutableParamReassignmentIsRejected(t *testing.T) {
	src := `fn rewrite(value: i32) -> i32 {
	value = 9;
	return value;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) ||
		!strings.Contains(diag.EmitAllToString(), "cannot assign to immutable binding `value`") {
		t.Fatalf("expected immutable parameter assignment diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestPointerSelfMethodCallResolvesOnPointerValue(t *testing.T) {
	src := `struct File {}

	fn (self: *File) read(buf: cstr) -> i32 {
		return 0;
	}

fn main(file: *File) -> i32 {
	return file.read(c"ok");
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestOwnedPointerFieldAssignmentAccepted(t *testing.T) {
	src := `struct Box {
	value: i32
}

fn main(ptr: *Box) {
	ptr.value = 2;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestRawPointerCannotBeSelfReceiver(t *testing.T) {
	src := `fn (receiver: rawptr) to_str() -> cstr {
		return "ok";
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidMethodReceiver) {
		t.Fatalf("expected invalid raw pointer receiver diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestOwnedPointerSelfMethodCallRejectsStackValue(t *testing.T) {
	src := `struct Number { value: i32 }
fn (receiver: *Number) to_str() -> cstr {
		return "ok";
}

fn main() -> i32 {
	let mut i: Number = .{ value = 42 };
	let s: cstr = i.to_str();
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
		t.Fatalf("expected type mismatch diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMutableReferenceSelfMethodCallRejectsConstValue(t *testing.T) {
	src := `struct Counter {
	value: i32
}

	fn (self: &mut Counter) bump() -> i32 {
		self.value = self.value + 1;
		return self.value;
	}

fn main() -> i32 {
	const c: Counter = .{ value = 0 };
	return c.bump();
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) {
		t.Fatalf("expected invalid assignment diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "is const") {
		t.Fatalf("expected const receiver diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReferenceSelfMethodsAcceptAddressableValue(t *testing.T) {
	src := `struct Counter {
	value: i32
}

	fn (self: &Counter) get() -> i32 {
		return self.value;
	}

	fn (self: &Counter) twice() -> i32 {
		return self.get() + self.get();
	}

	fn (self: &mut Counter) bump() -> i32 {
		self.value = self.value + 1;
		return self.value;
	}

	fn (self: &mut Counter) touch() -> i32 {
		self.bump();
		return self.get();
	}

fn main() -> i32 {
	let mut c: Counter = .{ value = 0 };
	c.touch();
	return c.twice();
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestSharedReferenceSelfMethodRejectsFieldMutation(t *testing.T) {
	src := `struct Counter {
	value: i32
}

	fn (self: &Counter) bump() {
		self.value = self.value + 1;
	}

fn main() -> i32 {
	let c: Counter = .{ value = 0 };
	c.bump();
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) {
		t.Fatalf("expected invalid assignment diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestSharedReferenceAliasRejectsFieldMutation(t *testing.T) {
	src := `struct Counter {
	value: i32
}

fn bad(shared: &Counter) {
	let mut alias = shared;
	alias.value = 1;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) {
		t.Fatalf("expected shared-reference mutation diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "cannot assign through immutable reference") ||
		!strings.Contains(diag.EmitAllToString(), "use `&mut Counter`") {
		t.Fatalf("expected immutable-reference mutation guidance, got:\n%s", diag.EmitAllToString())
	}
}

func TestNestedSharedReferenceAliasRejectsFieldMutation(t *testing.T) {
	src := `struct Inner {
	value: i32
}

struct Outer {
	inner: Inner
}

fn bad(shared: &Outer) {
	let mut alias = shared;
	alias.inner.value = 1;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) {
		t.Fatalf("expected nested shared-reference mutation diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReferenceReturnRequiresFromClause(t *testing.T) {
	src := `struct Box {
	value: i32
}

	fn (self: &Box) reference() -> &Box {
		return self;
	}
`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidReturn) {
		t.Fatalf("expected missing reference-return contract diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "requires a `from` clause") {
		t.Fatalf("expected missing from-clause diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReferenceReturnContractsAcceptedAcrossCallableForms(t *testing.T) {
	src := `struct Box {
	value: i32
}

fn first(value: &Box) -> &Box from value {
	return value;
}

#[extern]
fn External(value: &Box) -> &Box from value;

fn (self: &Box) reference() -> &Box from self {
	return self;
}

iface Reader {
	fn (&Self) current(fallback: &Box) -> &Box from(self, fallback)
}

fn useCallback(callback: fn(value: &Box) -> &Box from value, value: &Box) -> &Box from value {
	return callback(value);
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected reference-return contract diagnostics:\n%s", diag.EmitAllToString())
	}
	if strings.Contains(diag.EmitAllToString(), "requires a `from` clause") {
		t.Fatalf("valid contracts reported missing:\n%s", diag.EmitAllToString())
	}
}

func TestInvalidReferenceReturnContractsRejected(t *testing.T) {
	src := `
fn missing(value: &i32) -> &i32;
fn unknown(value: &i32) -> &i32 from other;
fn owned(value: i32) -> &i32 from value;
fn duplicate(value: &i32) -> &i32 from(value, value);
fn mutable(value: &i32) -> &mut i32 from value;
fn scalar(value: &i32) -> i32 from value;
const callback: fn(&i32) -> &i32 = missing;
`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidReturn) {
		t.Fatalf("expected invalid return-contract diagnostics, got:\n%s", diag.EmitAllToString())
	}
	out := diag.EmitAllToString()
	for _, want := range []string{
		"requires a `from` clause",
		"must name a borrowed parameter or `self` receiver",
		"source must be a borrowed parameter",
		"duplicate source",
		"mutable reference return requires mutable borrowed sources",
		"only valid on reference returns",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q diagnostic, got:\n%s", want, out)
		}
	}
}

func TestReferenceContainingStructFieldRejected(t *testing.T) {
	src := `struct Box {
	value: i32
}

type SharedBox = &Box;

struct Holder {
	references: [2]SharedBox
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) {
		t.Fatalf("expected reference-storage diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "references cannot be stored in struct fields") {
		t.Fatalf("expected reference-storage message, got:\n%s", diag.EmitAllToString())
	}
}

func TestReferenceStorageRejectedAcrossTypeAndBindingBoundaries(t *testing.T) {
	src := `struct Box {
	value: i32
}

type References = [2]&Box;

const GlobalReference: &Box = none;

fn StoreReferences(_: []&Box) {
	let _: [2]&Box;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) {
		t.Fatalf("expected reference-storage diagnostics, got:\n%s", diag.EmitAllToString())
	}
	if strings.Count(diag.EmitAllToString(), "references cannot be stored") < 4 {
		t.Fatalf("expected alias, global, parameter, and local storage diagnostics, got:\n%s", diag.EmitAllToString())
	}
}

func TestCallableReferenceMetadataAcceptedInStorageAndReturns(t *testing.T) {
	src := `struct Counter {
	value: i32
}

struct Handler {
	callback: fn(&Counter) -> i32
}

#[extern]
fn GetCallback() -> fn(&Counter) -> i32;

fn UseCallback(_: fn(&Counter) -> i32) -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMutableReferenceSelfMethodRejectsImmutableValue(t *testing.T) {
	src := `struct Counter {
	value: i32
}

	fn (self: &mut Counter) bump() -> i32 {
		self.value = self.value + 1;
		return self.value;
	}

fn main() -> i32 {
	let c: Counter = .{ value = 0 };
	return c.bump();
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) {
		t.Fatalf("expected invalid assignment diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "is immutable") {
		t.Fatalf("expected immutable receiver diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestVoidLikeFunctionAllowsBareReturn(t *testing.T) {
	src := `fn log() {
	return;
}

fn main() -> i32 {
	log();
	return 0;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestVoidLikeFunctionRejectsReturnedValue(t *testing.T) {
	src := `fn log() {
	return 1;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidReturn) {
		t.Fatalf("expected invalid return diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestVoidLikeCallCannotBeUsedAsValue(t *testing.T) {
	src := `fn log() {
	return;
}

fn main() -> i32 {
	let x = log();
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidExpression) {
		t.Fatalf("expected invalid expression diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestStructLiteralAssignsToNamedStruct(t *testing.T) {
	src := `struct Point {
	x: i32,
	y: i32,
}

fn main() -> i32 {
	let p: Point = .{ x = 1, y = 2 };
	return p.x;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestGenericNamedTypesReachConcreteTypechecking(t *testing.T) {
	src := `struct Box<T> { value: T }
type Maybe<T> = ?T;
iface Reader<T> { fn (&Self) read() -> T }

fn Read(box: &Box<i32>) -> i32 { return box.value; }
fn main() -> i32 {
	let box: Box<i32> = .{ value = 42 };
	let maybe: Maybe<i32> = box.value;
	return Read(&box);
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected generic type diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestTypedStructLiteralInfersNamedStruct(t *testing.T) {
	src := `struct Point {
	x: i32,
	y: i32,
}

fn main() -> i32 {
	let p = .Point{ x = 1, y = 2 };
	return p.x;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestAnonymousStructLiteralInfersShape(t *testing.T) {
	src := `fn main() -> i32 {
	let p = .{ x = 1, y = 2 };
	return p.x;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestAssignmentRequiresMutableBinding(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i32 = 1;
	x = 2;
	return x;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) {
		t.Fatalf("expected invalid assignment diagnostic, got:\n%s", diag.EmitAllToString())
	}
	var targetDiag *diagnostics.Diagnostic
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrInvalidAssignment {
			targetDiag = item
			break
		}
	}
	if targetDiag == nil {
		t.Fatalf("expected ErrInvalidAssignment")
	}
	if targetDiag.Message != "modification to immutable symbol" {
		t.Fatalf("expected title 'modification to immutable symbol', got %q", targetDiag.Message)
	}
	if len(targetDiag.Labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(targetDiag.Labels))
	}
	// Verify primary label
	if targetDiag.Labels[0].Style != diagnostics.Primary {
		t.Fatalf("expected first label to be primary")
	}
	if targetDiag.Labels[0].Message != "cannot assign to immutable binding `x`" {
		t.Fatalf("expected primary label msg, got %q", targetDiag.Labels[0].Message)
	}
	// Verify secondary label
	if targetDiag.Labels[1].Style != diagnostics.Secondary {
		t.Fatalf("expected second label to be secondary")
	}
	if targetDiag.Labels[1].Message != "make this binding mutable" {
		t.Fatalf("expected secondary label msg, got %q", targetDiag.Labels[1].Message)
	}
}

func TestPointerFieldAssignmentResolves(t *testing.T) {
	src := `struct Counter {
	value: i32
}

	fn (self: *Counter) bump() -> i32 {
		self.value = self.value + 1;
		return self.value;
	}
`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestIfConditionRejectsNumericTruthiness(t *testing.T) {
	src := `fn main() -> i32 {
	if 1 {
		return 1;
	}
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidOperation) {
		t.Fatalf("expected invalid operation diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "use `as bool`") {
		t.Fatalf("expected explicit cast guidance, got:\n%s", diag.EmitAllToString())
	}
}

func TestUnaryNotRejectsNumericTruthiness(t *testing.T) {
	src := `fn main() -> i32 {
	if !1 {
		return 1;
	}
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidOperation) {
		t.Fatalf("expected invalid operation diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestLogicalAndRejectsNumericTruthiness(t *testing.T) {
	src := `fn main() -> i32 {
	if 1 && 2 {
		return 1;
	}
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidOperation) {
		t.Fatalf("expected invalid operation diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestExplicitNumericToBoolCastAllowed(t *testing.T) {
	src := `fn main() -> i32 {
	if (1 as bool) {
		return 1;
	}
	return 0;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMutableLocalFieldAssignmentResolves(t *testing.T) {
	src := `struct Counter {
	value: i32
}

fn main() -> i32 {
	let mut c: Counter = .{ value = 0 };
	c.value = 100;
	return c.value;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestTemporaryBorrowsAllowedForCallDuration(t *testing.T) {
	src := `struct Box { value: i32 }
fn Make() -> Box { return .{ value = 1 }; }
fn Read(_: &Box) {}
fn Write(_: &mut Box) {}
fn valid() {
	Read(&Make());
	Write(&mut Make());
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestTemporaryBorrowEscapeRejected(t *testing.T) {
	src := `struct Box { value: i32 }
fn Make() -> Box { return .{ value = 1 }; }
fn binding() {
	let reference = &Make();
}
fn assignment(seed: Box) {
	let mut reference = &seed;
	reference = &Make();
}
fn raw() {
	let pointer = @Make();
}`
	diag := checkTypeSource(t, src)
	out := diag.EmitAllToString()
	if strings.Count(out, "reference to temporary cannot escape") != 2 {
		t.Fatalf("expected binding and assignment escape diagnostics, got:\n%s", out)
	}
	if !strings.Contains(out, "address operator requires addressable storage") {
		t.Fatalf("expected raw temporary address rejection, got:\n%s", out)
	}
}

func TestTemporaryBorrowEscapeThroughReturnContractRejected(t *testing.T) {
	tests := map[string]string{
		"binding":               `fn bad() { let reference = Identity(&Make()); }`,
		"assignment":            `fn bad(seed: Box) { let mut reference = &seed; reference = Identity(&Make()); }`,
		"return":                `fn bad(seed: &Box) -> &Box from seed { return Identity(&Make()); }`,
		"nested call":           `fn bad() { let reference = Identity(Identity(&Make())); }`,
		"possible multi origin": `fn bad(seed: Box) { let reference = Choose(true, &seed, &Make()); }`,
		"optional return":       `fn bad() { let reference = Maybe(&Make()); }`,
	}
	prefix := `struct Box { value: i32 }
fn Make() -> Box { return .{ value = 1 }; }
fn Identity(value: &Box) -> &Box from value { return value; }
fn Choose(cond: bool, left: &Box, right: &Box) -> &Box from(left, right) {
	if cond { return left; }
	return right;
}
fn Maybe(value: ?&Box) -> ?&Box from value { return value; }
`
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			diag := checkTypeSource(t, prefix+src)
			if !strings.Contains(diag.EmitAllToString(), "reference to temporary cannot escape") {
				t.Fatalf("expected temporary contract escape diagnostic, got:\n%s", diag.EmitAllToString())
			}
		})
	}
}

func TestTemporaryStringViewEscapeRejected(t *testing.T) {
	diag := checkTypeSource(t, `fn MakeText() -> str { return "abc"; }
fn binding() {
	let bytes = MakeText() |> as_bytes();
}
fn assignment(seed: &[..]byte) {
	let mut bytes = seed;
	bytes = MakeText() |> as_bytes();
}
fn returning(seed: &str) -> &[..]byte from seed {
	return MakeText() |> as_bytes();
}`)
	out := diag.EmitAllToString()
	if strings.Count(out, "reference to temporary cannot escape") != 3 {
		t.Fatalf("expected string view escape diagnostics, got:\n%s", out)
	}
}

func TestIntrinsicFunctionResolutionStoredForLaterPhases(t *testing.T) {
	module, diag := checkTypeModule(t, `fn main() -> usize {
	let text: str = "hello";
	return text |> len();
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	var call *ast.CallExpr
	var callee *ast.Ident
	for _, stmt := range module.AST.Stmts {
		ast.Inspect(stmt, func(node ast.Node) bool {
			candidate, ok := node.(*ast.CallExpr)
			if ok && candidate.Piped {
				call = candidate
				callee, _ = candidate.Callee.(*ast.Ident)
			}
			return callee == nil
		})
		if callee != nil {
			break
		}
	}
	if callee == nil || callee.Name != "len" {
		t.Fatal("len function missing from parsed module")
	}
	resolved := module.Semantics.ResolvedSymbols[callee.ID()]
	if resolved == nil || resolved.CompilerOp != symbols.CompilerOpLen {
		t.Fatalf("resolved function = %#v, want len intrinsic", resolved)
	}
	evidence, ok := module.Semantics.CompilerCalls[call.ID()]
	if !ok || evidence.Operation != symbols.CompilerOpLen || evidence.Kind != intrinsics.FunctionCollection {
		t.Fatalf("compiler call evidence = %#v, want collection len", evidence)
	}
}

func TestCompilerOwnedFunctionsAreNotMethods(t *testing.T) {
	diag := checkTypeSource(t, `fn main() -> usize {
	let text: str = "hello";
	return text.len();
}`)
	if !hasTypeCode(diag, diagnostics.ErrMethodNotFound) {
		t.Fatalf("expected builtin selector rejection:\n%s", diag.EmitAllToString())
	}
}

func TestInterfaceImplementationEvidenceStoredForLowering(t *testing.T) {
	module, diag := checkTypeModule(t, `iface Reader { fn (&Self) read() -> i32 }
struct Counter { value: i32 }
fn (self: &Counter) read() -> i32 { return self.value; }
fn main() {
	let counter: Counter = .{ value = 7 };
	let reader: &Reader = &counter;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	var conversion ast.Expr
	for _, stmt := range module.AST.Stmts {
		ast.Inspect(stmt, func(node ast.Node) bool {
			binding, ok := node.(*ast.LetDecl)
			if ok && binding.Name != nil && binding.Name.Name == "reader" {
				conversion = binding.Value
			}
			return conversion == nil
		})
		if conversion != nil {
			break
		}
	}
	if conversion == nil {
		t.Fatal("reader interface conversion missing from parsed module")
	}
	implementations := module.Semantics.InterfaceImplementations[conversion.ID()]
	if len(implementations) != 1 {
		t.Fatalf("implementation evidence = %#v, want one method", implementations)
	}
	implementation := implementations[0]
	if implementation.MethodName != "read" || implementation.Symbol == nil ||
		implementation.CallableType == nil || implementation.OwnerKey != "Counter" {
		t.Fatalf("implementation evidence = %#v, want exact Counter.read symbol and type", implementation)
	}
}

func TestDefaultExpansionKeepsDistinctInterfaceImplementationEvidence(t *testing.T) {
	module, diag := checkTypeModule(t, `iface ReadA { fn (&Self) read_a() -> i32 }
iface ReadB { fn (&Self) read_b() -> i32 }
struct Counter { value: i32 }
fn (self: &Counter) read_a() -> i32 { return self.value; }
fn (self: &Counter) read_b() -> i32 { return self.value + 1; }
fn use(value: &Counter, first: &ReadA = value, second: &ReadB = value) -> i32 {
	return first.read_a() + second.read_b();
}
fn main() -> i32 {
	let counter: Counter = .{ value = 20 };
	return use(&counter);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	var call *ast.CallExpr
	for _, stmt := range module.AST.Stmts {
		ast.Inspect(stmt, func(node ast.Node) bool {
			candidate, ok := node.(*ast.CallExpr)
			if ok && len(candidate.Args) == 3 {
				call = candidate
			}
			return call == nil
		})
		if call != nil {
			break
		}
	}
	if call == nil {
		t.Fatal("expanded use call not found")
	}
	first := module.Semantics.InterfaceImplementations[call.Args[1].ID()]
	second := module.Semantics.InterfaceImplementations[call.Args[2].ID()]
	if call.Args[1].ID() == call.Args[2].ID() || len(first) != 1 || len(second) != 1 {
		t.Fatalf("default evidence IDs/evidence = %d:%#v %d:%#v", call.Args[1].ID(), first, call.Args[2].ID(), second)
	}
	if first[0].MethodName != "read_a" || second[0].MethodName != "read_b" {
		t.Fatalf("default evidence overwritten: %#v %#v", first, second)
	}
}

func TestIntrinsicMethodDoesNotSatisfyInterface(t *testing.T) {
	diag := checkTypeSource(t, `iface Lenner {
	fn (&Self) len() -> usize
}

fn main() -> i32 {
	let text: str = "abc";
	let value: &Lenner = &text;
	return value |> len() as i32;
}`)
	if !hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
		t.Fatalf("expected intrinsic interface conformance rejection, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "missing methods: len") {
		t.Fatalf("expected missing intrinsic method hint, got:\n%s", diag.EmitAllToString())
	}
}

func TestTemporaryBorrowContractScalarProjectionAllowed(t *testing.T) {
	diag := checkTypeSource(t, `struct Box { value: i32 }
fn Make() -> Box { return .{ value = 1 }; }
fn Identity(value: &Box) -> &Box from value { return value; }
fn valid() -> i32 {
	return Identity(&Make()).value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected scalar projection diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestCanAdaptFirstCallArgumentUsesCallConversionRules(t *testing.T) {
	ctx := project.New(".", peeper.SourceExt, diagnostics.NewDiagnosticBag())
	module := &project.Module{Semantics: project.NewSemanticInfo()}
	element, ok := typeinfo.NumericTypeFromName("i32", ctx.Target)
	if !ok {
		t.Fatal("missing i32 type")
	}
	owner := &typeinfo.ArrayType{Shape: typeinfo.ArrayOwner, Elem: element}
	mutableOwner := &typeinfo.RefType{Target: owner, Mutable: true}
	if !CanAdaptFirstCallArgument(ctx, module, mutableOwner, owner) {
		t.Fatal("owner should adapt to mutable owner reference")
	}
	slice := &typeinfo.ArrayType{Shape: typeinfo.ArraySlice, Elem: element}
	if CanAdaptFirstCallArgument(ctx, module, mutableOwner, slice) {
		t.Fatal("slice must not adapt to mutable owner reference")
	}
	if CanAdaptFirstCallArgument(ctx, module, &typeinfo.BoolType{}, element) {
		t.Fatal("numeric value must not adapt to bool")
	}
}

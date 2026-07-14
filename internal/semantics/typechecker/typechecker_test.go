package typechecker

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
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
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
	return 0;
}`)
	if valid.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", valid.EmitAllToString())
	}

	invalid := checkTypeSource(t, `fn main() -> i32 {
	let float_and = 1.0 & 2.0;
	let bool_or = true | false;
	let bool_not = ~true;
	return 0;
}`)
	out := invalid.EmitAllToString()
	if !invalid.HasErrors() || !strings.Contains(out, "unsupported operand type for operator `&`") ||
		!strings.Contains(out, "unsupported operand type for operator `|`") ||
		!strings.Contains(out, "unsupported unary operand type for operator `~`") {
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
	src := `const name: cstr = "puts";

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
	src := `fn symbol_name() -> cstr { return "puts"; }
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
	funcScope := sym.Scope.(*table.Scope)
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
	funcScope := sym.Scope.(*table.Scope)
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
		x: i32,
	}

	fn main() -> i32 {
		let p: Point;
		return p.x;
	}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized variable diagnostic, got:\n%s", diag.EmitAllToString())
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

func TestUninitializedLocalReadIsRejected(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i32;
	return x;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized variable diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestAssignmentInitializesLocal(t *testing.T) {
	src := `fn main() -> i32 {
	let mut x: i32;
	x = 1;
	return x;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestIfSingleBranchAssignmentDoesNotDefinitelyInitialize(t *testing.T) {
	src := `fn main(flag: bool) -> i32 {
	let mut x: i32;
	if flag {
		x = 1;
	}
	return x;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized variable diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestIfBothBranchesAssignmentDefinitelyInitializes(t *testing.T) {
	src := `fn main(flag: bool) -> i32 {
	let mut x: i32;
	if flag {
		x = 1;
	} else {
		x = 2;
	}
	return x;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
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

func TestArrayIndexExprRejectsDynamicIndexUntilBoundsPolicy(t *testing.T) {
	src := `fn first(xs: [4]i32, i: i32) -> i32 {
	return xs[i];
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrArrayIndexNotConst) {
		t.Fatalf("expected const-index diagnostic, got:\n%s", diag.EmitAllToString())
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

func TestSliceViewComparisonsRejectedBeforeLowering(t *testing.T) {
	for _, op := range []string{"==", "!=", "<", "<=", ">", ">="} {
		t.Run(op, func(t *testing.T) {
			src := "fn compare(left: &[]i32, right: &[]i32) -> bool { return left " + op + " right; }"
			diag := checkTypeSource(t, src)
			if !hasTypeCode(diag, diagnostics.ErrInvalidOperation) {
				t.Fatalf("expected slice-view comparison diagnostic, got:\n%s", diag.EmitAllToString())
			}
			if !strings.Contains(diag.EmitAllToString(), "slice-view comparison is not supported") {
				t.Fatalf("expected slice-view comparison limitation, got:\n%s", diag.EmitAllToString())
			}
		})
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
	src := `fn first(xs: &[]i32, i: usize) -> i32 {
	return xs[i];
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMutableSliceViewIndexAssignmentAccepted(t *testing.T) {
	src := `fn fill(xs: &mut []i32, i: usize) {
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
fn invalid(values: &[]Token) {
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
	src := `fn fill(xs: &[]i32, i: usize) {
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
	src := `fn ranges(fixed: [4]i32, owner: []i32, shared: &[]i32, mutable: &mut []i32) {
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
	src := `fn first(xs: [4]i32, flag: bool) -> &[]i32 {
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
	if hasTypeCode(diag, diagnostics.ErrArrayIndexNotConst) {
		t.Fatalf("invalid index reached constant evaluation:\n%s", diag.EmitAllToString())
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
	let appended = append([]i32{}, 1);
	let reserved = reserve(appended, 8);
	let resized = resize(reserved, 4, 0);
}`
	module, diag := checkTypeModule(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	for _, stmt := range fn.Body.Stmts {
		binding := stmt.(*ast.LetDecl)
		if got := typeinfo.TypeText(module.Semantics.ExprTypes[binding.Value.ID()]); got != "[]i32" {
			t.Fatalf("%s result type = %s, want []i32", binding.Name.Name, got)
		}
	}
}

func TestDynamicArrayOwnerOperationRequiresOwner(t *testing.T) {
	diag := checkTypeSource(t, `fn extend(values: &[]i32) {
	append(values, 1);
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) ||
		!strings.Contains(diag.EmitAllToString(), "requires a dynamic-array owner") {
		t.Fatalf("expected owner diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayResizeRejectsMoveOnlyElements(t *testing.T) {
	diag := checkTypeSource(t, `struct Point { x: i32 }
fn main() {
	resize([]Point{}, 2, .Point{x = 0});
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
	return reader.read("ok");
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

func TestPrintReservesPrintfLinkedSymbol(t *testing.T) {
	for _, src := range []string{
		`fn printf() {} fn main() { print(1); }`,
		`#[extern("printf")] fn output(value: cstr); fn main() { print(1); }`,
		`struct Printer {} #[extern("printf")] fn (_: &Printer) output(value: cstr); fn main() { print(1); }`,
	} {
		diag := checkTypeSource(t, src)
		if !hasTypeCode(diag, diagnostics.ErrRedeclaredSymbol) ||
			!strings.Contains(diag.EmitAllToString(), "linked symbol `printf` is reserved") {
			t.Fatalf("expected reserved printf diagnostic, got:\n%s", diag.EmitAllToString())
		}
	}
	valid := checkTypeSource(t, `struct Printer {} fn (_: Printer) printf() {} fn main() { print(1); }`)
	if valid.HasErrors() {
		t.Fatalf("receiver method named printf must use its mangled symbol:\n%s", valid.EmitAllToString())
	}
}

func TestRuntimeSymbolsReservedAcrossModules(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	parseModule := func(path, importPath, src string) *project.Module {
		module := &project.Module{
			Key:        project.ModuleKeyFor(project.ModuleOriginLocal, path),
			ImportPath: importPath,
			FilePath:   path,
			Content:    src,
			AST:        parser.New(path, lexer.New(path, src, diag).Tokenize(), diag).ParseModule(),
			Imports:    make(map[string]project.ResolvedImport),
		}
		ctx.AddModule(module)
		return module
	}
	user := parseModule("user.peep", "user", `fn main() { print(42); let _ = append([]i32{}, 1); }`)
	hijack := parseModule("hijack.peep", "hijack", `fn printf(value: i32) {} #[extern("malloc")] fn BadMalloc(size: i32) -> rawptr; #[extern("free")] fn BadFree(value: i32);`)
	for _, module := range []*project.Module{user, hijack} {
		collector.Collect(ctx, module)
		binder.Bind(ctx, module)
		resolver.Resolve(ctx, module)
		Check(ctx, module)
	}
	if !hasTypeCode(diag, diagnostics.ErrRedeclaredSymbol) ||
		!strings.Contains(diag.EmitAllToString(), "linked symbol `printf` is reserved") {
		t.Fatalf("expected cross-module printf reservation, got:\n%s", diag.EmitAllToString())
	}
	if count := strings.Count(diag.EmitAllToString(), "linked symbol `printf` is reserved"); count != 1 {
		t.Fatalf("expected one printf reservation diagnostic, got %d:\n%s", count, diag.EmitAllToString())
	}
	if count := strings.Count(diag.EmitAllToString(), "linked symbol `malloc` is reserved"); count != 1 {
		t.Fatalf("expected one malloc reservation diagnostic, got %d:\n%s", count, diag.EmitAllToString())
	}
	if count := strings.Count(diag.EmitAllToString(), "linked symbol `free` is reserved"); count != 1 {
		t.Fatalf("expected one free reservation diagnostic, got %d:\n%s", count, diag.EmitAllToString())
	}
}

func TestDynamicArrayLiteralAllowsCompatibleMallocDeclaration(t *testing.T) {
	diag := checkTypeSource(t, `#[extern("malloc")]
fn Allocate(size: usize) -> rawptr;

#[extern("free")]
fn Release(value: rawptr);

fn main() {
	let _ = []i32{1};
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected compatible malloc diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestEmptyDynamicArrayLiteralDoesNotReserveMalloc(t *testing.T) {
	diag := checkTypeSource(t, `#[extern("malloc")]
fn Unrelated(value: i32) -> rawptr;

fn main() {
	[]i32{};
}`)
	if diag.HasErrors() {
		t.Fatalf("empty dynamic literal must not reserve malloc:\n%s", diag.EmitAllToString())
	}
}

func TestShadowedDynamicArrayOperationDoesNotReserveAllocatorSymbols(t *testing.T) {
	diag := checkTypeSource(t, `#[extern("malloc")]
fn UnrelatedAllocate(value: i32) -> rawptr;

#[extern("free")]
fn UnrelatedRelease(value: i32);

fn add(left: i32, right: i32) -> i32 {
	return left + right;
}

fn main() {
	let append = add;
	let _ = append(1, 2);
}`)
	if diag.HasErrors() {
		t.Fatalf("shadowed append must remain an ordinary call:\n%s", diag.EmitAllToString())
	}
}

func TestPrintAcceptsPrimitiveScalars(t *testing.T) {
	diag := checkTypeSource(t, `fn show(mut value: i32, raw: rawptr) {
	print(value);
	print(42u32);
	print(2.5f64);
	print(true);
	print(value as byte);
	print("hello");
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
	return file.read("ok");
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

func TestReferenceReturnRejectedUntilOriginTrackingExists(t *testing.T) {
	src := `struct Box {
	value: i32
}

	fn (self: &Box) reference() -> &Box {
		return self;
	}

fn leak() -> &Box {
	let box: Box = .{ value = 1 };
	return box.reference();
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidReturn) {
		t.Fatalf("expected unsupported reference-return diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "returning references requires origin tracking") {
		t.Fatalf("expected reference-origin diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestBodylessReferenceReturnsRejected(t *testing.T) {
	src := `struct Box {
	value: i32
}

#[extern]
fn ExternalLeak() -> &Box;

	#[extern]
	fn (self: &Box) ExternalReference() -> &Box;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidReturn) {
		t.Fatalf("expected bodyless reference-return diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if strings.Count(diag.EmitAllToString(), "returning references requires origin tracking") != 2 {
		t.Fatalf("expected top-level and method return diagnostics, got:\n%s", diag.EmitAllToString())
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

package typechecker

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
)

func checkTypeSource(t *testing.T, src string) *diagnostics.DiagnosticBag {
	t.Helper()
	const filePath = "typechecker_test.em"
	diag := diagnostics.NewDiagnosticBag(filePath)
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", ".em", diag)
	modAST := parser.ParseModule(filePath, lexer.Lex(filePath, src, diag), diag)
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
	resolver.Resolve(ctx, module)
	Check(ctx, module)
	return diag
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

func TestImplMethodAllowsSelfForBuiltinTarget(t *testing.T) {
	src := `impl i32 {
	fn abs(self: Self) -> Self {
		return self;
	}
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestImplMethodAllowsNonSelfReceiverName(t *testing.T) {
	src := `impl i32 {
	fn abs(value: Self) -> Self {
		return value;
	}
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestInterfaceAndImplAllowSelf(t *testing.T) {
	src := `interface Reader {
	read(^Self, buf: cstr): i32,
}

struct File {}

impl File {
	fn read(self: ^Self, buf: cstr) -> i32 {
		return 0;
	}
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestSelfOutsideInterfaceOrImplIsRejected(t *testing.T) {
	src := `fn bad(value: Self) -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) {
		t.Fatalf("expected invalid type diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestBuiltinMethodCallResolvesThroughImpl(t *testing.T) {
	src := `impl i32 {
	fn abs(self: Self) -> Self {
		return self;
	}
}

fn main() -> i32 {
	let x: i32 = 1;
	return x.abs();
}`
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
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPointerSelfInterfaceAssignmentRequiresPointerValue(t *testing.T) {
	src := `interface Reader {
	read(^Self, buf: cstr): i32,
}

struct File {}

impl File {
	fn read(self: ^Self, buf: cstr) -> i32 {
		return 0;
	}
}

fn main(file: ^File) -> i32 {
	let reader: Reader = file;
	return reader.read("ok");
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPointerSelfInterfaceAssignmentRejectsValue(t *testing.T) {
	src := `interface Reader {
	read(^Self, buf: cstr): i32,
}

struct File {}

impl File {
	fn read(self: ^Self, buf: cstr) -> i32 {
		return 0;
	}
}

fn main() -> i32 {
	let file: File;
	let reader: Reader = file;
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
		t.Fatalf("expected type mismatch diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestPointerSelfMethodCallResolvesOnPointerValue(t *testing.T) {
	src := `struct File {}

impl File {
	fn read(self: ^Self, buf: cstr) -> i32 {
		return 0;
	}
}

fn main(file: ^File) -> i32 {
	return file.read("ok");
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPointerSelfMethodCallResolvesOnAddressableValue(t *testing.T) {
	src := `impl i32 {
	fn to_str(receiver: ^Self) -> cstr {
		return "ok";
	}
}

fn main() -> i32 {
	let mut i: i32 = 42;
	let s: cstr = i.to_str();
	return 0;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPointerSelfMethodCallRejectsImmutableValue(t *testing.T) {
	src := `impl i32 {
	fn to_str(receiver: ^Self) -> cstr {
		return "ok";
	}
}

	fn main() -> i32 {
	let i: i32 = 42;
	let s: cstr = i.to_str();
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) {
		t.Fatalf("expected invalid assignment diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "mutable binding") {
		t.Fatalf("expected mutable binding diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestPointerSelfMethodCallRejectsConstValue(t *testing.T) {
	src := `struct Counter {
	value: i32,
}

impl Counter {
	fn bump(self: ^Self) -> i32 {
		self.value = self.value + 1;
		return self.value;
	}
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
	value: i32,
}

impl Counter {
	fn bump(self: ^Self) -> i32 {
		self.value = self.value + 1;
		return self.value;
	}
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMutableLocalFieldAssignmentResolves(t *testing.T) {
	src := `struct Counter {
	value: i32,
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

package typechecker

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
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
	ptr: ^u8,
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "unknown type attribute") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestConflictingTypeAttributesRejected(t *testing.T) {
	src := `#[no_copy]
#[allow_copy]
struct Buffer {
	ptr: ^u8,
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "conflicting type attributes") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

<<<<<<< HEAD
func TestMoveExprTransfersNoCopyBinding(t *testing.T) {
	diag := checkTypeSource(t, `#[no_copy]
struct Buffer {
	ptr: ^u8,
}

fn get_buffer() -> Buffer;
fn destroy(move data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	let next = move current;
	destroy(next);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestCopyOfNoCopyBindingRejected(t *testing.T) {
	diag := checkTypeSource(t, `#[no_copy]
struct Buffer {
	ptr: ^u8,
}

fn get_buffer() -> Buffer;
fn destroy(move data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	let next = current;
	destroy(next);
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected invalid copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestUseAfterMoveRejected(t *testing.T) {
	diag := checkTypeSource(t, `#[no_copy]
struct Buffer {
	ptr: ^u8,
}

fn get_buffer() -> Buffer;
fn destroy(move data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	let next = move current;
	destroy(current);
	destroy(next);
}`)
	if !hasTypeCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-move diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestFieldAssignOnMovedRootRejected(t *testing.T) {
	diag := checkTypeSource(t, `#[no_copy]
struct Buffer {
	ptr: ^u8,
}

struct Holder {
	buf: Buffer,
}

fn get_buffer() -> Buffer;
fn destroy(move holder: Holder) {}

fn main() {
	let mut holder: Holder = .{ buf = get_buffer() };
	destroy(holder);
	holder.buf = get_buffer();
}`)
	if !hasTypeCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-move diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveParamConsumesArgument(t *testing.T) {
	diag := checkTypeSource(t, `#[no_copy]
struct Buffer {
	ptr: ^u8,
}

fn get_buffer() -> Buffer;
fn destroy(move data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	destroy(current);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReassignmentClearsMovedLocal(t *testing.T) {
	diag := checkTypeSource(t, `#[no_copy]
struct Buffer {
	value: i32,
}

fn destroy(move data: Buffer) {}

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

func TestNoCopyArgumentToPlainParamRejected(t *testing.T) {
	diag := checkTypeSource(t, `#[no_copy]
struct Buffer {
	ptr: ^u8,
}

fn get_buffer() -> Buffer;
fn inspect(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	inspect(current);
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected invalid copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestNoCopyInterfaceConversionRequiresMove(t *testing.T) {
	diag := checkTypeSource(t, `interface Reader {
	read(self: Self) -> i32
}

#[no_copy]
struct Buffer {
	ptr: ^u8
}

fn get_buffer() -> Buffer;

impl Buffer {
	fn read(self: Self) -> i32 {
		return 0;
	}
}

fn main() {
	let current: Buffer = get_buffer();
	let reader: Reader = current;
}`)
	if !hasTypeCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected invalid copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveNoCopyInterfaceConversionAccepted(t *testing.T) {
	diag := checkTypeSource(t, `interface Reader {
	read(self: Self) -> i32
}

#[no_copy]
struct Buffer {
	ptr: ^u8
}

fn get_buffer() -> Buffer;

impl Buffer {
	fn read(self: Self) -> i32 {
		return 0;
	}
}

fn main() {
	let current: Buffer = get_buffer();
	let reader: Reader = move current;
	reader.read();
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestInterfaceMoveParamRejectedForNow(t *testing.T) {
	diag := checkTypeSource(t, `interface Reader {
	read(move self: Self);
}`)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "interface methods cannot use `move` parameters") {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMutablePointerFieldDefaultsTypeToNoCopy(t *testing.T) {
	module, diag := checkTypeModule(t, `struct Buffer {
	ptr: ^u8,
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
	if typeinfo.IsCopyType(typ) {
		t.Fatalf("Buffer should default to no-copy")
	}
}

func TestAddressOfMutableLocalAssignsMutablePointer(t *testing.T) {
	diag := checkTypeSource(t, `fn main() -> i32 {
	let mut value: i32 = 1;
	let ptr: ^i32 = @value;
	return 0;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestAddressOfImmutableLocalAssignsConstPointer(t *testing.T) {
	diag := checkTypeSource(t, `fn main() -> i32 {
	let value: i32 = 1;
	let ptr: ^const i32 = @value;
	return 0;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestAddressOfImmutableLocalRejectsMutablePointer(t *testing.T) {
	diag := checkTypeSource(t, `fn main() -> i32 {
	let value: i32 = 1;
	let ptr: ^i32 = @value;
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

func TestConstPointerFieldStaysCopyable(t *testing.T) {
	module, diag := checkTypeModule(t, `struct View {
	ptr: ^const u8,
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
	if !typeinfo.IsCopyType(typ) {
		t.Fatalf("View should stay copyable")
	}
}

func TestAllowCopyOverridesMutablePointerDefault(t *testing.T) {
	module, diag := checkTypeModule(t, `#[allow_copy]
struct Cursor {
	ptr: ^u8,
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	sym, ok := module.ModuleScope.LookupLocal("Cursor")
	if !ok || sym == nil {
		t.Fatalf("missing Cursor symbol")
	}
	typ, ok := symbols.GetSymbolType(sym)
	if !ok || typ == nil {
		t.Fatalf("missing Cursor type")
	}
	if !typeinfo.IsCopyType(typ) {
		t.Fatalf("allow_copy should override default no-copy")
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
	read(^Self, buf: cstr) -> i32,
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
	next: ^Node,
}

#[extern]
fn next_node() -> ^Node;

fn main() -> i32 {
	let node: Node = .{ next = next_node() };
	let next: ^Node = node.next;
	return 0;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
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

func TestPointerSelfInterfaceAssignmentRequiresPointerValue(t *testing.T) {
	src := `interface Reader {
	read(^Self, buf: cstr) -> i32,
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
	read(^Self, buf: cstr) -> i32,
}

struct File {}

impl File {
	fn read(self: ^Self, buf: cstr) -> i32 {
		return 0;
	}
}

fn main() -> i32 {
	let file: File = .{};
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

func TestConstPointerFieldAssignmentRejected(t *testing.T) {
	src := `struct Box {
	value: i32,
}

fn main(ptr: ^const Box) {
	ptr.value = 2;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) {
		t.Fatalf("expected invalid assignment diagnostic, got:\n%s", diag.EmitAllToString())
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

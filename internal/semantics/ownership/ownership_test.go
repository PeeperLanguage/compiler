package ownership

import (
	"slices"
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
	"compiler/internal/semantics/typechecker"
	"compiler/pkg/peeper"
)

type ownershipResult struct {
	*diagnostics.DiagnosticBag
	module *project.Module
}

func checkOwnershipSource(t *testing.T, src string) *ownershipResult {
	t.Helper()
	const filePath = "ownership_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "ownership_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	typechecker.Check(ctx, module)
	Check(ctx, module)
	return &ownershipResult{DiagnosticBag: diag, module: module}
}

func hasOwnershipCode(result *ownershipResult, code string) bool {
	if result == nil {
		return false
	}
	for _, item := range result.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}

func cleanupSymbolNames(cleanup []*symbols.Symbol) []string {
	names := make([]string, 0, len(cleanup))
	for _, sym := range cleanup {
		if sym != nil {
			names = append(names, sym.Name)
		}
	}
	return names
}

func TestOwnedPointerBindingMovesImplicitly(t *testing.T) {
	diag := checkOwnershipSource(t, `fn make() -> *byte;

fn main() {
	let ptr: *byte = make();
	let duplicate = ptr;
	let invalid = ptr;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestRawPointerCopyAllowed(t *testing.T) {
	diag := checkOwnershipSource(t, `fn main() {
	let value: i32 = 1;
	let ptr: rawptr = @value;
	let duplicate = ptr;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestLiveOwnerFieldOverwritePlansDrop(t *testing.T) {
	result := checkOwnershipSource(t, `struct Holder { value: *i32 }
fn make() -> *i32;
fn bad(mut holder: Holder) {
	let next = make();
	holder.value = next;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected overwrite diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[2].(*ast.FnDecl)
	assign := fn.Body.Stmts[1].(*ast.AssignStmt)
	if _, ok := result.module.Semantics.DropBeforeAssign[assign.ID()]; !ok {
		t.Fatalf("missing drop-before-assignment plan")
	}
}

func TestLiveOwnerIndexedOverwritePlansDrop(t *testing.T) {
	result := checkOwnershipSource(t, `fn make() -> *i32;
fn replace(mut values: [1]*i32) {
	values[0] = make();
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected overwrite diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[1].(*ast.FnDecl)
	assign := fn.Body.Stmts[0].(*ast.AssignStmt)
	if _, ok := result.module.Semantics.DropBeforeAssign[assign.ID()]; !ok {
		t.Fatalf("missing indexed drop-before-assignment plan")
	}
}

func TestFieldAssignmentRejectsRHSMoveOfTargetBase(t *testing.T) {
	result := checkOwnershipSource(t, `struct Box { ptr: *i32 }
fn replace(_: Box) -> *i32;
fn bad(mut box: Box) {
	box.ptr = replace(box);
}`)
	if !hasOwnershipCode(result, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected moved assignment-base diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestCleanupPlanUsesReverseLexicalOrder(t *testing.T) {
	result := checkOwnershipSource(t, `fn make() -> *i32;
fn main() {
	let first = make();
	{
		let nested = make();
	}
	let last = make();
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected cleanup diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[1].(*ast.FnDecl)
	nested := fn.Body.Stmts[1].(*ast.BlockStmt)
	if got := cleanupSymbolNames(result.module.Semantics.CleanupAfterBlock[nested.ID()]); !slices.Equal(got, []string{"nested"}) {
		t.Fatalf("nested cleanup = %v, want [nested]", got)
	}
	if got := cleanupSymbolNames(result.module.Semantics.CleanupAfterBlock[fn.Body.ID()]); !slices.Equal(got, []string{"last", "first"}) {
		t.Fatalf("function cleanup = %v, want [last first]", got)
	}
}

func TestReturnCleanupSuppressesMovedResult(t *testing.T) {
	result := checkOwnershipSource(t, `fn pass(value: *i32, spare: *i32) -> *i32 {
	return value;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected return diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[0].(*ast.FnDecl)
	ret := fn.Body.Stmts[0].(*ast.ReturnStmt)
	if got := cleanupSymbolNames(result.module.Semantics.CleanupBeforeReturn[ret.ID()]); !slices.Equal(got, []string{"spare"}) {
		t.Fatalf("return cleanup = %v, want [spare]", got)
	}
}

func TestReturnCleanupClearsStateBeforeExitMerge(t *testing.T) {
	result := checkOwnershipSource(t, `fn make() -> *i32;
fn main(cond: bool) {
	let value = make();
	if cond {
		return;
	}
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected return diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[1].(*ast.FnDecl)
	ret := fn.Body.Stmts[1].(*ast.IfStmt).Then.Stmts[0].(*ast.ReturnStmt)
	if got := cleanupSymbolNames(result.module.Semantics.CleanupBeforeReturn[ret.ID()]); !slices.Equal(got, []string{"value"}) {
		t.Fatalf("return cleanup = %v, want [value]", got)
	}
}

func TestReturnCleanupKeepsReplacementOwnerLive(t *testing.T) {
	result := checkOwnershipSource(t, `fn make() -> *i32;
fn main() -> i32 {
	let mut first = make();
	{
		let nested = make();
	}
	let next = make();
	first = next;
	let early = make();
	free(early);
	make();
	return 0;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected return diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[1].(*ast.FnDecl)
	ret := fn.Body.Stmts[7].(*ast.ReturnStmt)
	if got := cleanupSymbolNames(result.module.Semantics.CleanupBeforeReturn[ret.ID()]); !slices.Equal(got, []string{"first"}) {
		t.Fatalf("return cleanup = %v, want [first]", got)
	}
}

func TestBranchOwnershipStateMustConverge(t *testing.T) {
	result := checkOwnershipSource(t, `fn consume(_: *i32) {}
fn bad(cond: bool, value: *i32) {
	if cond {
		consume(value);
	}
}`)
	if !hasOwnershipCode(result, diagnostics.ErrInvalidAssignment) ||
		!strings.Contains(result.EmitAllToString(), "ownership state differs across control-flow paths") {
		t.Fatalf("expected branch convergence diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestLoopOwnershipStateMustConverge(t *testing.T) {
	result := checkOwnershipSource(t, `fn consume(_: *i32) {}
fn bad(cond: bool, value: *i32) {
	for cond {
		consume(value);
	}
}`)
	if !hasOwnershipCode(result, diagnostics.ErrInvalidAssignment) ||
		!strings.Contains(result.EmitAllToString(), "ownership state differs across control-flow paths") {
		t.Fatalf("expected loop convergence diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestOwnershipTrackedModuleBindingRejected(t *testing.T) {
	result := checkOwnershipSource(t, `fn make() -> *i32;
const Global = make();`)
	if !hasOwnershipCode(result, diagnostics.ErrInvalidAssignment) ||
		!strings.Contains(result.EmitAllToString(), "ownership-tracked module bindings are not supported") {
		t.Fatalf("expected owned-global diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestMoveOnlyModuleBindingWithoutDropRejected(t *testing.T) {
	result := checkOwnershipSource(t, `struct Token { value: i32 }
const Global = .Token{ value = 1 };`)
	if !hasOwnershipCode(result, diagnostics.ErrInvalidAssignment) ||
		!strings.Contains(result.EmitAllToString(), "ownership-tracked module bindings are not supported") {
		t.Fatalf("expected move-only global diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestArrayLiteralConsumesCompositeElements(t *testing.T) {
	for _, literal := range []string{"[1]Point{point}", "[]Point{point}"} {
		diag := checkOwnershipSource(t, `struct Point { value: i32 }
fn consume(point: Point) {}
fn bad(point: Point) {
	let points = `+literal+`;
	consume(point);
}`)
		if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
			t.Fatalf("expected %s insertion to consume point, got:\n%s", literal, diag.EmitAllToString())
		}
	}
}

func TestUserCopyMethodWithValueReceiverConsumesCaller(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Point { value: i32 }
	fn (self: Point) copy() -> Point { return self; }
fn bad(point: Point) -> i32 {
	let duplicate = point.copy();
	return point.value + duplicate.value;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected copy method value receiver to consume caller, got:\n%s", diag.EmitAllToString())
	}
}

func TestConsumingOwnedInterfaceMethodMovesCarrier(t *testing.T) {
	diag := checkOwnershipSource(t, `iface Consumer { fn (Self) consume() }
struct Resource {}
fn (self: Resource) consume() {}
fn bad(resource: *Resource) {
	let consumer: *Consumer = resource;
	consumer.consume();
	free(consumer);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected consuming interface call to move carrier, got:\n%s", diag.EmitAllToString())
	}
}

func TestBorrowedCopyMethodCannotExtractOwnedField(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Owner { value: *i32 }
	fn (self: &Owner) copy() -> Owner { return .{ value = self.value }; }
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected borrowed copy method owner extraction rejection, got:\n%s", diag.EmitAllToString())
	}
}

func TestDiscardedOwnedProjectionFromTemporaryRejected(t *testing.T) {
	result := checkOwnershipSource(t, `struct Pair { first: *i32, second: *i32 }
fn make() -> Pair;
fn bad() {
	make().first;
}`)
	if !hasOwnershipCode(result, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected temporary owner-projection diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestMoveOnlyIndexedElementCopyRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn first(values: []*i32, index: usize) -> *i32 {
	return values[index];
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected indexed move-only copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "cannot be used by value") {
		t.Fatalf("expected permanent indexed move rejection, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveOnlySliceViewElementCopyRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn first(values: &[]*i32, index: usize) -> *i32 {
	return values[index];
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected indexed move-only copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "cannot be used by value") {
		t.Fatalf("expected permanent indexed move rejection, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveOnlyIndexedElementCannotBeConsumedRepeatedly(t *testing.T) {
	diag := checkOwnershipSource(t, `fn consume(value: *i32) {}

fn duplicate(values: []*i32, index: usize) {
	consume(values[index]);
	consume(values[index]);
}`)
	out := diag.EmitAllToString()
	if count := strings.Count(out, "move-only indexed element"); count != 2 {
		t.Fatalf("expected two indexed move-only consume diagnostics, got %d:\n%s", count, out)
	}
}

func TestCopyableIndexedElementReadAllowed(t *testing.T) {
	diag := checkOwnershipSource(t, `fn first(values: []i32, index: usize) -> i32 {
	return values[index];
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReferenceReceiverDoesNotCopyMoveOnlyOwner(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Counter {
	value: i32
}

	fn (self: &Counter) get() -> i32 {
		return self.value;
	}

fn main() -> i32 {
	let c: Counter = .{ value = 1 };
	c.get();
	return c.get();
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMutableReferenceArgumentIsReborrowed(t *testing.T) {
	diag := checkOwnershipSource(t, `fn sink(value: &mut i32) {}

fn forward(value: &mut i32) {
	sink(value);
	sink(value);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMutableReferenceBindingTransfersImplicitly(t *testing.T) {
	diag := checkOwnershipSource(t, `fn duplicate(reference: &mut i32) {
	let alias = reference;
	let invalid = reference;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMutableReferenceBindingCanMove(t *testing.T) {
	diag := checkOwnershipSource(t, `fn transfer(reference: &mut i32) {
	let alias = reference;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestImplicitBindingTransfersNoCopyValue(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
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

func TestNoCopyBindingMovesImplicitly(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	let next = current;
	destroy(current);
	destroy(next);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestUseAfterMoveRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	let next = current;
	destroy(current);
	destroy(next);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestValueParamConsumesArgument(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	destroy(current);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPlainValueParamConsumesArgument(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn inspect(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	inspect(current);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReassignmentClearsMovedLocal(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	value: i32,
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

func TestDynamicArrayOwnerOperationsConsumeAndReinitializeOwner(t *testing.T) {
	diag := checkOwnershipSource(t, `fn main() {
	let mut values = []i32{};
	values = append(values, 1);
	values = reserve(values, 8);
	values = resize(values, 4, 0);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayAppendConsumesOwner(t *testing.T) {
	diag := checkOwnershipSource(t, `fn main() {
	let values = []i32{};
	let extended = append(values, 1);
	print(values[0]);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected moved-owner diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayAppendConsumesCompositeElement(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Point { x: i32 }
fn consume(point: Point) {}
fn main() {
	let point = .Point{x = 1};
	let values = append([]Point{}, point);
	consume(point);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected moved-element diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestNoCopyArgumentToPlainParamMoves(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn inspect(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	inspect(current);
	inspect(current);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestFreeConsumesOwnedPointer(t *testing.T) {
	diag := checkOwnershipSource(t, `fn get_value() -> *i32;
fn main() {
	let value = get_value();
	free(value);
	free(value);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-free diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestValueReceiverConsumesBinding(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;

	fn (self: Buffer) close() {}

fn main() {
	let current: Buffer = get_buffer();
	current.close();
	current.close();
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestNoCopyFieldSubexpressionRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

struct Holder {
	buf: Buffer,
}

fn get_buffer() -> Buffer;

fn main() {
	let holder: Holder = .{ buf = get_buffer() };
	let next = holder.buf;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected invalid copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveInBranchRejectsUseAfterJoin(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main(flag: bool) {
	let current: Buffer = get_buffer();
	if flag {
		destroy(current);
	}
	destroy(current);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveInLoopRejectsLaterUse(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main(flag: bool) {
	let current: Buffer = get_buffer();
	for flag {
		destroy(current);
	}
	destroy(current);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnAddressOfLocalRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn bad() -> rawptr {
	let value: i32 = 1;
	return @value;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrPointerEscape) {
		t.Fatalf("expected pointer escape diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnAddressOfLocalIndexedElementRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn bad() -> rawptr {
	let values = [_]i32{1};
	return @values[0];
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrPointerEscape) {
		t.Fatalf("expected indexed pointer escape diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnLocalPointerBindingRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn bad() -> rawptr {
	let value: i32 = 1;
	let ptr: rawptr = @value;
	return ptr;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrPointerEscape) {
		t.Fatalf("expected pointer escape diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnAddressOfModuleGlobalAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `const global: i32 = 1;

fn get() -> rawptr {
	return @global;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReturnModuleGlobalPointerBindingAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `const global: i32 = 1;

fn get() -> rawptr {
	let ptr: rawptr = @global;
	return ptr;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReturnPointerParamAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `fn identity(ptr: rawptr) -> rawptr {
	return ptr;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReturnExternPointerAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `#[extern]
fn open_value() -> rawptr;

fn get() -> rawptr {
	return open_value();
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

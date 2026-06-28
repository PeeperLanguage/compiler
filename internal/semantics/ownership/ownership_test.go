package ownership

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typechecker"
	"compiler/pkg/peeper"
)

func checkOwnershipSource(t *testing.T, src string) *diagnostics.DiagnosticBag {
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
	return diag
}

func hasOwnershipCode(diag *diagnostics.DiagnosticBag, code string) bool {
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

func TestMoveExprTransfersNoCopyBinding(t *testing.T) {
	diag := checkOwnershipSource(t, `#[no_copy]
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
	diag := checkOwnershipSource(t, `#[no_copy]
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
	if !hasOwnershipCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected invalid copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestUseAfterMoveRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `#[no_copy]
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
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-move diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveParamConsumesArgument(t *testing.T) {
	diag := checkOwnershipSource(t, `#[no_copy]
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
	diag := checkOwnershipSource(t, `#[no_copy]
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
	diag := checkOwnershipSource(t, `#[no_copy]
struct Buffer {
	ptr: ^u8,
}

fn get_buffer() -> Buffer;
fn inspect(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	inspect(current);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected invalid copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestNoCopyInterfaceConversionRequiresMove(t *testing.T) {
	diag := checkOwnershipSource(t, `interface Reader {
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
	if !hasOwnershipCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected invalid copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveNoCopyInterfaceConversionAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `interface Reader {
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

func TestMoveReceiverConsumesBinding(t *testing.T) {
	diag := checkOwnershipSource(t, `#[no_copy]
struct Buffer {
	ptr: ^u8,
}

fn get_buffer() -> Buffer;

impl Buffer {
	fn close(move self: Self) {}
}

fn main() {
	let current: Buffer = get_buffer();
	current.close();
	current.close();
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-move diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestNoCopyFieldSubexpressionRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `#[no_copy]
struct Buffer {
	ptr: ^u8,
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
	diag := checkOwnershipSource(t, `#[no_copy]
struct Buffer {
	ptr: ^u8,
}

fn get_buffer() -> Buffer;
fn destroy(move data: Buffer) {}

fn main(flag: bool) {
	let current: Buffer = get_buffer();
	if flag {
		destroy(current);
	}
	destroy(current);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-move diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveInLoopRejectsLaterUse(t *testing.T) {
	diag := checkOwnershipSource(t, `#[no_copy]
struct Buffer {
	ptr: ^u8,
}

fn get_buffer() -> Buffer;
fn destroy(move data: Buffer) {}

fn main(flag: bool) {
	let current: Buffer = get_buffer();
	for flag {
		destroy(current);
	}
	destroy(current);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-move diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnAddressOfLocalRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn bad() -> ^const i32 {
	let value: i32 = 1;
	return @value;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrPointerEscape) {
		t.Fatalf("expected pointer escape diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnLocalPointerBindingRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn bad() -> ^const i32 {
	let value: i32 = 1;
	let ptr: ^const i32 = @value;
	return ptr;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrPointerEscape) {
		t.Fatalf("expected pointer escape diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnAddressOfModuleGlobalAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `const global: i32 = 1;

fn get() -> ^const i32 {
	return @global;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReturnModuleGlobalPointerBindingAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `const global: i32 = 1;

fn get() -> ^const i32 {
	let ptr: ^const i32 = @global;
	return ptr;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReturnPointerParamAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `fn identity(ptr: ^i32) -> ^i32 {
	return ptr;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReturnExternPointerAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `#[extern]
fn open_value() -> ^i32;

fn get() -> ^i32 {
	return open_value();
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

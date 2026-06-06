package pipeline

import (
	"strings"
	"testing"

	"compiler/core/diagnostics"
	"compiler/internal/context"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
)

func buildPipelineTest(t *testing.T, preludeSrc, entrySrc string) *diagnostics.DiagnosticBag {
	t.Helper()
	const preludePath = "_builtin_library/global.em"
	const entryPath = "entry.em"

	diag := diagnostics.NewDiagnosticBag(entryPath)
	diag.AddSourceContent(preludePath, preludeSrc)
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := context.New(".", ".em", diag)

	// Register the prelude so the pipeline loader can find it.
	prelude := &context.Module{
		Key:        "core:prelude/global",
		ImportPath: "prelude/global",
		FilePath:   preludePath,
		Origin:     context.ModuleOriginStdlib,
		AST:        parser.ParseModule(preludePath, lexer.Lex(preludePath, preludeSrc, diag), diag),
		Imports:    make(map[string]context.ResolvedImport),
	}
	ctx.AddModule(prelude)

	entry := &context.Module{
		Key:        context.ModuleKeyFor(context.ModuleOriginLocal, entryPath),
		ImportPath: strings.TrimSuffix(entryPath, ".em"),
		FilePath:   entryPath,
		Origin:     context.ModuleOriginLocal,
		AST:        parser.ParseModule(entryPath, lexer.Lex(entryPath, entrySrc, diag), diag),
		Imports:    make(map[string]context.ResolvedImport),
	}

	if err := New(ctx).Run(entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	return diag
}

// TestPipelinePreludeSymbolsVisibleInEntry verifies that prelude-defined symbols
// (write, stdout, etc.) are resolved correctly in user entry modules even when
// the entry module has no explicit import of the prelude.
func TestPipelinePreludeSymbolsVisibleInEntry(t *testing.T) {
	preludeSrc := `let stdin:  i32 = 0;
let stdout: i32 = 1;
let stderr: i32 = 2;

#[extern]
fn write(fd: i32, buf: cstr, n: i32) -> i32;
`
	entrySrc := `fn main() -> i32 {
	let msg: cstr = "Hello from Ember runtime ABI!\n";
	let _ = write(stdout, msg, 30);
	return 0;
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	for _, item := range diag.Diagnostics() {
		if item == nil {
			continue
		}
		if item.Code == diagnostics.ErrUndefinedSymbol && strings.Contains(item.Message, "write") {
			t.Fatalf("unexpected undefined prelude symbol 'write': %s", diag.EmitAllToString())
		}
		if item.Code == diagnostics.ErrUndefinedSymbol && strings.Contains(item.Message, "stdout") {
			t.Fatalf("unexpected undefined prelude symbol 'stdout': %s", diag.EmitAllToString())
		}
	}
}

// TestPipelineAllowsExpressionStatements verifies that call expressions used as
// statements (discarding the return value) do not produce invalid-statement errors.
func TestPipelineAllowsExpressionStatements(t *testing.T) {
	preludeSrc := `let stdout: i32 = 1;

#[extern]
fn write(fd: i32, buf: cstr, n: i32) -> i32;
`
	entrySrc := `fn main() -> i32 {
	let msg: cstr = "Hello from Ember runtime ABI!\n";
	write(stdout, msg, 30);
	return 0;
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	for _, item := range diag.Diagnostics() {
		if item == nil {
			continue
		}
		if item.Code == diagnostics.ErrInvalidStatement && strings.Contains(item.Message, "expression statements") {
			t.Fatalf("unexpected invalid expression statement diagnostic: %s", diag.EmitAllToString())
		}
	}
}

func TestPipelineLowersImplMethodCalls(t *testing.T) {
	preludeSrc := ``
	entrySrc := `impl i32 {
	fn abs(self: Self) -> Self {
		return self;
	}
}

fn main() -> i32 {
	let x: i32 = 1;
	return x.abs();
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersPointerReceiverOnNamedStruct(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct File {}

#[extern]
fn open_file() -> ^File;

impl File {
	fn read(self: ^Self, buf: cstr) -> i32 {
		return 0;
	}
}

fn main() -> i32 {
	let file = open_file();
	return file.read("ok");
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersAutoAddressedPointerReceiverOnValue(t *testing.T) {
	preludeSrc := ``
	entrySrc := `impl i32 {
	fn id(self: ^Self) -> i32 {
		return 7;
	}
}

fn main() -> i32 {
	let mut x: i32 = 1;
	return x.id();
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersPointerFieldAssignment(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Counter {
	value: i32,
}

#[extern]
fn open_counter() -> ^Counter;

impl Counter {
	fn bump(self: ^Self) -> i32 {
		self.value = self.value + 1;
		return self.value;
	}
}

fn main() -> i32 {
	let c = open_counter();
	return c.bump();
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersMutableLocalFieldAssignment(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Counter {
	value: i32,
}

fn main() -> i32 {
	let mut c: Counter = .{ value = 0 };
	c.value = 100;
	return c.value;
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersStructFieldAccess(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Point {
	x: i32,
	y: i32,
}

fn main() -> i32 {
	let p: Point = .{ x = 1, y = 2 };
	return p.x;
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersAnonymousStructLiteralFieldAccess(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn main() -> i32 {
	let p = .{ x = 1, y = 2 };
	return p.x;
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersPointerFieldAccess(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Point {
	x: i32,
	y: i32,
}

#[extern]
fn open_point() -> ^Point;

fn main() -> i32 {
	let p = open_point();
	return p.x;
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersInterfaceDispatchForValueReceiver(t *testing.T) {
	preludeSrc := ``
	entrySrc := `interface Summer {
	sum(Self): i32,
}

struct Point {
	x: i32,
	y: i32,
}

impl Point {
	fn sum(self: Self) -> i32 {
		return self.x + self.y;
	}
}

fn total(v: Summer) -> i32 {
	return v.sum();
}

fn main() -> i32 {
	let p: Point = .{ x = 10, y = 20 };
	return total(p);
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersInterfaceDispatchForPointerReceiver(t *testing.T) {
	preludeSrc := ``
	entrySrc := `interface Reader {
	read(^Self, buf: cstr): i32,
}

struct File {}

#[extern]
fn open_file() -> ^File;

impl File {
	fn read(self: ^Self, buf: cstr) -> i32 {
		return 7;
	}
}

fn use(reader: Reader) -> i32 {
	return reader.read("ok");
}

fn main() -> i32 {
	let file = open_file();
	return use(file);
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineInterfaceDuplicateWrappers(t *testing.T) {
	preludeSrc := ``
	entrySrc := `interface Summer {
	sum(Self): i32,
}

struct Point {
	x: i32,
	y: i32,
}

impl Point {
	fn sum(self: Self) -> i32 {
		return self.x + self.y;
	}
}

fn make_summer_1() -> Summer {
	let p: Point = .{ x = 10, y = 20 };
	return p;
}

fn make_summer_2() -> Summer {
	let p: Point = .{ x = 30, y = 40 };
	return p;
}

fn main() -> i32 {
	return 0;
}`

	const preludePath = "_builtin_library/global.em"
	const entryPath = "entry.em"

	diag := diagnostics.NewDiagnosticBag(entryPath)
	diag.AddSourceContent(preludePath, preludeSrc)
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := context.New(".", ".em", diag)

	entry := &context.Module{
		Key:        context.ModuleKeyFor(context.ModuleOriginLocal, entryPath),
		ImportPath: strings.TrimSuffix(entryPath, ".em"),
		FilePath:   entryPath,
		Origin:     context.ModuleOriginLocal,
		AST:        parser.ParseModule(entryPath, lexer.Lex(entryPath, entrySrc, diag), diag),
		Imports:    make(map[string]context.ResolvedImport),
	}

	if err := New(ctx).Run(entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}

	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}

	// The thunk function for Summer -> Point -> sum should be defined exactly once.
	// We count "define i32 @__ifacethunk__" to verify.
	wrapperDef := "define i32 @__ifacethunk__"
	count := strings.Count(entry.LLVMIR, wrapperDef)
	if count != 1 {
		t.Errorf("expected exactly 1 definition of the interface wrapper function, got %d. LLVM IR:\n%s", count, entry.LLVMIR)
	}
}

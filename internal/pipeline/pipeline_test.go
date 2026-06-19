package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/pkg/peeper"
)

func parseModuleSource(filePath, src string, diag *diagnostics.DiagnosticBag) *project.Module {
	return &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: strings.TrimSuffix(filePath, peeper.SourceExt),
		FilePath:   filePath,
		AST:        parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:    make(map[string]project.ResolvedImport),
	}
}

func buildPipelineTestWithConfig(t *testing.T, cfg project.Config, preludeSrc, entrySrc string) *diagnostics.DiagnosticBag {
	t.Helper()
	const preludePath = "core/global" + peeper.SourceExt
	const entryPath = "entry" + peeper.SourceExt

	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(preludePath, preludeSrc)
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.NewWithConfig(cfg, diag)

	// Register the prelude so the pipeline loader can find it.
	prelude := parseModuleSource(preludePath, preludeSrc, diag)
	prelude.Key = "core:prelude/global"
	prelude.ImportPath = "prelude/global"
	prelude.Namespace = "core"
	prelude.Origin = project.ModuleOriginStdlib
	ctx.AddModule(prelude)

	entry := parseModuleSource(entryPath, entrySrc, diag)
	entry.Origin = project.ModuleOriginLocal

	if err := New(ctx).Run(entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	return diag
}

// TestPipelinePreludeSymbolsVisibleInEntry verifies that prelude-defined symbols
// (write, stdout, etc.) are resolved correctly in user entry modules even when
// the entry module has no explicit import of the prelude.
func TestPipelinePreludeSymbolsVisibleInEntry(t *testing.T) {
	preludeSrc := `const stdin:  i32 = 0;
const stdout: i32 = 1;
const stderr: i32 = 2;

#[extern]
fn write(fd: i32, buf: cstr, n: i32) -> i32;
`
	entrySrc := `fn main() -> i32 {
	let msg: cstr = "Hello from Peeper runtime ABI!\n";
	let _ = write(stdout, msg, 30);
	return 0;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
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

func TestPipelineDebugBuildEmitsLLVMMetadata(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn main() -> i32 {
	return 0;
}`

	cfg := project.Config{
		RootDir:       ".",
		Extension:     peeper.SourceExt,
		TargetOS:      "linux",
		TargetArch:    "amd64",
		TargetBackend: "llvm",
		BuildDebug:    true,
	}
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent("core/global"+peeper.SourceExt, preludeSrc)
	diag.AddSourceContent("entry"+peeper.SourceExt, entrySrc)
	ctx := project.NewWithConfig(cfg, diag)

	prelude := parseModuleSource("core/global"+peeper.SourceExt, preludeSrc, diag)
	prelude.Key = "core:prelude/global"
	prelude.ImportPath = "prelude/global"
	prelude.Namespace = "core"
	prelude.Origin = project.ModuleOriginStdlib
	ctx.AddModule(prelude)

	entry := parseModuleSource("entry"+peeper.SourceExt, entrySrc, diag)
	entry.ImportPath = "entry"
	entry.Origin = project.ModuleOriginLocal

	if err := New(ctx).Run(entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(entry.LLVMIR, "!llvm.dbg.cu") {
		t.Fatalf("expected debug metadata in LLVM IR, got:\n%s", entry.LLVMIR)
	}
}

func TestPipelineAdvanceModulePhaseRunsOnePhaseAtATime(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	const entryPath = "entry" + peeper.SourceExt
	entrySrc := `fn main() -> i32 {
	return 0;
}`
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	entry := parseModuleSource(entryPath, entrySrc, diag)
	entry.Origin = project.ModuleOriginLocal
	entry.Phase = project.PhaseParsed
	ctx.AddModule(entry)

	pipeline := New(ctx)
	want := []project.ModulePhase{
		project.PhaseCollected,
		project.PhaseBound,
		project.PhaseResolved,
		project.PhaseTypechecked,
		project.PhaseUsage,
		project.PhaseHIR,
		project.PhaseMIR,
		project.PhaseBackend,
	}
	for _, phase := range want {
		if !pipeline.advanceModulePhase(entry, diag) {
			t.Fatalf("advanceModulePhase() stopped at %v, want %v", entry.Phase, phase)
		}
		if entry.Phase != phase {
			t.Fatalf("phase = %v, want %v", entry.Phase, phase)
		}
	}
	if pipeline.advanceModulePhase(entry, diag) {
		t.Fatalf("advanceModulePhase() reported progress after backend phase")
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineModuleReadyForNextPhaseFollowsImportContracts(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.NewWithConfig(project.Config{RootDir: "."}, diag)
	pipeline := New(ctx)

	imported := parseModuleSource("util"+peeper.SourceExt, "fn Helper() -> i32 { return 1; }", diag)
	imported.Origin = project.ModuleOriginLocal
	imported.Phase = project.PhaseParsed
	ctx.AddModule(imported)

	entry := parseModuleSource("main"+peeper.SourceExt, "import \"util\";\nfn main() -> i32 { return util::Helper(); }\n", diag)
	entry.Origin = project.ModuleOriginLocal
	entry.Phase = project.PhaseParsed
	entry.Imports = map[string]project.ResolvedImport{
		"util": {
			Key:        imported.Key,
			ImportPath: "util",
			FilePath:   imported.FilePath,
			Origin:     project.ModuleOriginLocal,
		},
	}
	ctx.AddModule(entry)

	if !pipeline.moduleReadyForNextPhase(entry, nil, true) {
		t.Fatalf("parsed importer should be ready for collector when import is parsed")
	}

	entry.Phase = project.PhaseCollected
	if pipeline.moduleReadyForNextPhase(entry, nil, true) {
		t.Fatalf("collected importer should wait for bound import before binder")
	}

	imported.Phase = project.PhaseBound
	if !pipeline.moduleReadyForNextPhase(entry, nil, true) {
		t.Fatalf("collected importer should be ready for binder when import is bound")
	}

	entry.Phase = project.PhaseBound
	imported.Phase = project.PhaseParsed
	if pipeline.moduleReadyForNextPhase(entry, nil, true) {
		t.Fatalf("bound importer should wait for collected import before resolver")
	}

	imported.Phase = project.PhaseCollected
	if !pipeline.moduleReadyForNextPhase(entry, nil, true) {
		t.Fatalf("bound importer should be ready for resolver when import is collected")
	}

	entry.Phase = project.PhaseResolved
	if pipeline.moduleReadyForNextPhase(entry, nil, true) {
		t.Fatalf("resolved importer should wait for bound import before typechecker")
	}

	imported.Phase = project.PhaseBound
	if !pipeline.moduleReadyForNextPhase(entry, nil, true) {
		t.Fatalf("resolved importer should be ready for typechecker when import is bound")
	}
}

func TestPipelineRunResolvesImportedModuleWithScheduler(t *testing.T) {
	root := t.TempDir()
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.NewWithConfig(project.Config{RootDir: root, ProjectName: "app", Extension: peeper.SourceExt}, diag)

	utilPath := filepath.Join(root, "util"+peeper.SourceExt)
	mainPath := filepath.Join(root, "main"+peeper.SourceExt)
	utilSrc := `fn Helper() -> i32 { return 7; }`
	mainSrc := `import "app/util";
fn main() -> i32 {
	return util::Helper();
}`
	diag.AddSourceContent(utilPath, utilSrc)
	diag.AddSourceContent(mainPath, mainSrc)

	entry := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, mainPath),
		ImportPath: "app/main",
		FilePath:   mainPath,
		Origin:     project.ModuleOriginLocal,
	}

	if err := os.WriteFile(utilPath, []byte(utilSrc), 0o644); err != nil {
		t.Fatalf("write util: %v", err)
	}
	if err := os.WriteFile(mainPath, []byte(mainSrc), 0o644); err != nil {
		t.Fatalf("write main: %v", err)
	}

	if err := New(ctx).Run(entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}

	util, ok := ctx.ModuleByFile(utilPath)
	if !ok || util == nil {
		t.Fatalf("expected imported module to be loaded")
	}
	if util.Phase != project.PhaseBackend {
		t.Fatalf("imported module phase = %v, want %v", util.Phase, project.PhaseBackend)
	}
	if entry.Phase != project.PhaseBackend {
		t.Fatalf("entry phase = %v, want %v", entry.Phase, project.PhaseBackend)
	}
}

// TestPipelineAllowsExpressionStatements verifies that call expressions used as
// statements (discarding the return value) do not produce invalid-statement errors.
func TestPipelineAllowsExpressionStatements(t *testing.T) {
	preludeSrc := `const stdout: i32 = 1;

#[extern]
fn write(fd: i32, buf: cstr, n: i32) -> i32;
`
	entrySrc := `fn main() -> i32 {
	let msg: cstr = "Hello from Peeper runtime ABI!\n";
	write(stdout, msg, 30);
	return 0;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	for _, item := range diag.Diagnostics() {
		if item == nil {
			continue
		}
		if item.Code == diagnostics.ErrInvalidStatement && strings.Contains(item.Message, "expression statements") {
			t.Fatalf("unexpected invalid expression statement diagnostic: %s", diag.EmitAllToString())
		}
	}
}

func TestPipelineLowersBareReturnInNoValueFunction(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn log() {
	return;
}

fn main() -> i32 {
	log();
	return 0;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineAllowsForwardFunctionCalls(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn main() -> i32 {
	return later();
}

fn later() -> i32 {
	return 7;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineRejectsTopLevelInitializerUsingLaterBinding(t *testing.T) {
	preludeSrc := ``
	entrySrc := `const first: i32 = second;
const second: i32 = 2;

fn main() -> i32 {
	return second;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if !strings.Contains(diag.EmitAllToString(), diagnostics.ErrUseBeforeDecl) {
		t.Fatalf("expected use-before-declaration diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersUnusedCallBindingAsDiscardedCall(t *testing.T) {
	preludeSrc := `const stdout: i32 = 1;

#[extern]
fn write(fd: i32, buf: cstr, n: i32) -> i32;
`
	entrySrc := `fn work() -> i32 {
	let msg: cstr = "ping\n";
	write(stdout, msg, 5);
	return 7;
}

fn main() -> i32 {
	let unused = work();
	return 0;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
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

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
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

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineAllowsPointerRecursiveStruct(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Node {
	next: ^Node,
}

#[extern]
fn next_node() -> ^Node;

fn main() -> i32 {
	let node: Node = .{ next = next_node() };
	let next: ^Node = node.next;
	return 0;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineRejectsDirectStructCycle(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct A {
	b: B,
}

struct B {
	a: A,
}

fn main() -> i32 {
	return 0;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if !strings.Contains(diag.EmitAllToString(), diagnostics.ErrCircularDependency) {
		t.Fatalf("expected circular dependency diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineRejectsRecursiveTypeAlias(t *testing.T) {
	preludeSrc := ``
	entrySrc := `type Loop = Loop;

fn main() -> i32 {
	return 0;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if !strings.Contains(diag.EmitAllToString(), diagnostics.ErrCircularDependency) {
		t.Fatalf("expected circular dependency diagnostic, got:\n%s", diag.EmitAllToString())
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

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
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

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
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

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
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

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
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

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
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

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersInterfaceDispatchForValueReceiver(t *testing.T) {
	preludeSrc := ``
	entrySrc := `interface Summer {
	sum(Self) -> i32,
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

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersInterfaceDispatchForPointerReceiver(t *testing.T) {
	preludeSrc := ``
	entrySrc := `interface Reader {
	read(^Self, buf: cstr) -> i32,
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

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineInterfaceDuplicateWrappers(t *testing.T) {
	preludeSrc := ``
	entrySrc := `interface Summer {
	sum(Self) -> i32,
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

	const preludePath = "core/global" + peeper.SourceExt
	const entryPath = "entry" + peeper.SourceExt

	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(preludePath, preludeSrc)
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.New(".", peeper.SourceExt, diag)

	entry := parseModuleSource(entryPath, entrySrc, diag)
	entry.Origin = project.ModuleOriginLocal

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

func TestPipelineUsesStackBoxForNonEscapingInterfaceValue(t *testing.T) {
	preludeSrc := ``
	entrySrc := `interface Summer {
	sum(Self) -> i32,
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

fn main() -> i32 {
	let p: Point = .{ x = 10, y = 20 };
	let s: Summer = p;
	return s.sum();
}`

	const preludePath = "core/global" + peeper.SourceExt
	const entryPath = "entry" + peeper.SourceExt

	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(preludePath, preludeSrc)
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.New(".", peeper.SourceExt, diag)

	entry := parseModuleSource(entryPath, entrySrc, diag)
	entry.Origin = project.ModuleOriginLocal

	if err := New(ctx).Run(entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if strings.Contains(entry.LLVMIR, "@malloc") {
		t.Fatalf("expected non-escaping local interface value to avoid malloc, LLVM IR:\n%s", entry.LLVMIR)
	}
}

func TestPipelineLowersNestedFieldAssignment(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Inner {
	value: i32,
}
struct Outer {
	inner: Inner,
}
fn main() -> i32 {
	let mut out: Outer = .{ inner = .{ value = 1 } };
	out.inner.value = 42;
	return out.inner.value;
}`
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersPointerReceiverOnNestedField(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Counter {
	value: i32,
}
impl Counter {
	fn bump(self: ^Self) -> i32 {
		self.value = self.value + 1;
		return self.value;
	}
}
struct Container {
	counter: Counter,
}
fn main() -> i32 {
	let mut c: Container = .{ counter = .{ value = 10 } };
	return c.counter.bump();
}`
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineRejectsNestedFieldAssignmentOnImmutable(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Inner {
	value: i32,
}
struct Outer {
	inner: Inner,
}
fn main() -> i32 {
	let out: Outer = .{ inner = .{ value = 1 } };
	out.inner.value = 42;
	return out.inner.value;
}`
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if !diag.HasErrors() {
		t.Fatalf("expected assignment to immutable binding error, but compiled successfully")
	}
	found := false
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrInvalidAssignment {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected ErrInvalidAssignment error, got:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineInterfaceEscapesViaStoreFieldAndInterfaceCall(t *testing.T) {
	preludeSrc := ``
	entrySrc := `interface Summer {
	sum(Self) -> i32,
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

struct SummerHolder {
	s: Summer,
}

#[extern]
fn consume_holder(h: SummerHolder);

#[extern]
fn consume_summer(s: Summer);

interface SummerConsumer {
	consume(Self, val: Summer) -> i32,
}

#[extern]
fn get_consumer() -> SummerConsumer;

fn test_store_field() -> i32 {
	let p: Point = .{ x = 10, y = 20 };
	let s: Summer = p;
	let mut h: SummerHolder = .{ s = s };
	h.s = s;
	consume_holder(h);
	return 0;
}

fn test_interface_call_arg(c: SummerConsumer) -> i32 {
	let p: Point = .{ x = 10, y = 20 };
	let s: Summer = p;
	let _ = c.consume(s);
	consume_summer(s);
	return 0;
}

fn main() -> i32 {
	test_store_field();
	let c = get_consumer();
	test_interface_call_arg(c);
	return 0;
}`

	const preludePath = "core/global" + peeper.SourceExt
	const entryPath = "entry" + peeper.SourceExt

	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(preludePath, preludeSrc)
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.New(".", peeper.SourceExt, diag)

	entry := parseModuleSource(entryPath, entrySrc, diag)
	entry.Origin = project.ModuleOriginLocal

	if err := New(ctx).Run(entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if strings.Contains(entry.HIR.Text(), "<invalid: unsupported interface method shape>") {
		t.Fatalf("unexpected invalid interface lowering in HIR:\n%s", entry.HIR.Text())
	}
	if !strings.Contains(entry.LLVMIR, "@malloc") {
		t.Fatalf("expected escaping interface values to use malloc, LLVM IR:\n%s", entry.LLVMIR)
	}
	if strings.Contains(entry.LLVMIR, "extractvalue { i8*, i8* } 0") {
		t.Fatalf("unexpected zero interface receiver in LLVM IR:\n%s", entry.LLVMIR)
	}
}

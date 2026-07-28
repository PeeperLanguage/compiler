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

func runImportedRuntimeSymbolPipeline(t *testing.T, entrySrc, runtimeSrc string) *diagnostics.DiagnosticBag {
	t.Helper()
	root := t.TempDir()
	srcDir := filepath.Join(root, peeper.SourceDirName)
	entryPath := filepath.Join(srcDir, peeper.MainFileName)
	runtimePath := filepath.Join(srcDir, "runtime"+peeper.SourceExt)
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(entryPath, []byte(entrySrc), 0o644); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte(runtimeSrc), 0o644); err != nil {
		t.Fatalf("write runtime module: %v", err)
	}

	diag := diagnostics.NewDiagnosticBag()
	ctx := project.NewWithConfig(project.Config{
		RootDir:     root,
		ProjectName: "app",
		Extension:   peeper.SourceExt,
	}, diag)
	entry := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, entryPath),
		ImportPath: "app/main",
		FilePath:   entryPath,
		Origin:     project.ModuleOriginLocal,
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

func TestPipelineImportsCoreAllocatorRawMallocFree(t *testing.T) {
	root := t.TempDir()
	libraryBase := filepath.Join(root, "libs")
	allocatorPath := filepath.Join(libraryBase, "core", peeper.SourceDirName, "allocator"+peeper.SourceExt)
	if err := os.MkdirAll(filepath.Dir(allocatorPath), 0o755); err != nil {
		t.Fatalf("mkdir allocator: %v", err)
	}
	allocatorSrc := `#[extern("malloc")]
fn Malloc(size: usize) -> rawptr;

#[extern("free")]
fn Free(ptr: rawptr);
`
	if err := os.WriteFile(allocatorPath, []byte(allocatorSrc), 0o644); err != nil {
		t.Fatalf("write allocator: %v", err)
	}

	entryPath := filepath.Join(root, "entry"+peeper.SourceExt)
	entrySrc := `import "core:allocator";

fn main() -> i32 {
	let ptr: rawptr = allocator::Malloc(8);
	allocator::Free(ptr);
	return 0;
}`

	diag := diagnostics.NewDiagnosticBag()
	ctx := project.NewWithConfig(project.Config{
		RootDir:        root,
		Extension:      peeper.SourceExt,
		LibraryBaseDir: libraryBase,
		TargetBackend:  "llvm",
	}, diag)
	entry := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, entryPath),
		ImportPath: "entry",
		FilePath:   entryPath,
		Content:    entrySrc,
		Origin:     project.ModuleOriginLocal,
		Imports:    make(map[string]project.ResolvedImport),
	}

	if err := New(ctx).Run(entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(entry.LLVMIR, "declare i8* @malloc(i64)") {
		t.Fatalf("expected malloc declaration, LLVM IR:\n%s", entry.LLVMIR)
	}
	if !strings.Contains(entry.LLVMIR, "declare void @free(i8*)") {
		t.Fatalf("expected free declaration, LLVM IR:\n%s", entry.LLVMIR)
	}
}

func TestPipelineScalarShrinkLocalDropReservesForeignFree(t *testing.T) {
	diag := runImportedRuntimeSymbolPipeline(t, `import "app/runtime";

fn shorten(values: []i32) {
	let shortened = shrink(values, 0);
}`, `type Word = i32;

#[extern("free")]
fn BadFree(value: Word);`)
	if !strings.Contains(diag.EmitAllToString(), "runtime requires fn(rawptr) -> void") {
		t.Fatalf("expected local scalar shrink cleanup to reserve free:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineScalarShrinkReturnDoesNotReserveForeignFree(t *testing.T) {
	diag := runImportedRuntimeSymbolPipeline(t, `import "app/runtime";

fn shorten(values: []i32) -> []i32 {
	return shrink(values, 0);
}`, `type Word = i32;

#[extern("free")]
fn BadFree(value: Word);`)
	if diag.HasErrors() {
		t.Fatalf("scalar shrink pass-through must not reserve free:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineOwnerShrinkReservesForeignFree(t *testing.T) {
	diag := runImportedRuntimeSymbolPipeline(t, `import "app/runtime";

struct Resource { value: *i32 }

fn shorten(values: []Resource) -> []Resource {
	return shrink(values, 0);
}`, `type Word = i32;

#[extern("free")]
fn BadFree(value: Word);`)
	if !strings.Contains(diag.EmitAllToString(), "runtime requires fn(rawptr) -> void") {
		t.Fatalf("expected owner-bearing shrink cleanup to reserve free:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineRuntimeSymbolsReservedAcrossModules(t *testing.T) {
	diag := runImportedRuntimeSymbolPipeline(t, `import "app/runtime";

fn main() {
	print(42);
	let values = []i32{1};
}`, `type Word = i32;

fn printf(value: i32) {}

#[extern("malloc")]
fn BadMalloc(size: Word) -> rawptr;

#[extern("free")]
fn BadFree(value: Word);`)
	out := diag.EmitAllToString()
	for _, symbol := range []string{"printf", "malloc", "free"} {
		message := "runtime symbol `" + symbol + "`"
		if count := strings.Count(out, message); count != 1 {
			t.Fatalf("expected one %s reservation diagnostic, got %d:\n%s", symbol, count, out)
		}
	}
}

func TestPipelineExternWithoutSymbolOverrideUsesDeclaredName(t *testing.T) {
	preludeSrc := ``
	entrySrc := `#[extern]
fn ping() -> i32;

fn main() -> i32 {
	return ping();
}`

	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent("core/global"+peeper.SourceExt, preludeSrc)
	diag.AddSourceContent("entry"+peeper.SourceExt, entrySrc)
	ctx := project.NewWithConfig(project.Config{
		RootDir:       ".",
		Extension:     peeper.SourceExt,
		TargetBackend: "llvm",
	}, diag)

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
	if !strings.Contains(entry.LLVMIR, "declare i32 @ping()") {
		t.Fatalf("expected bare extern declaration, LLVM IR:\n%s", entry.LLVMIR)
	}
	if strings.Contains(entry.LLVMIR, "@ping$") {
		t.Fatalf("bare extern should not be mangled, LLVM IR:\n%s", entry.LLVMIR)
	}
}

func TestPipelineExternDefinitionStaysLocalAfterError(t *testing.T) {
	preludeSrc := ``
	entrySrc := `#[extern("puts")]
fn puts(msg: cstr) -> i32 {
	return 0;
}

fn main() -> i32 {
	return puts("hi");
}`

	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent("core/global"+peeper.SourceExt, preludeSrc)
	diag.AddSourceContent("entry"+peeper.SourceExt, entrySrc)
	ctx := project.NewWithConfig(project.Config{
		RootDir:       ".",
		Extension:     peeper.SourceExt,
		TargetBackend: "llvm",
	}, diag)

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
	if !diag.HasErrors() {
		t.Fatalf("expected extern definition diagnostic")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "attribute `#[extern]` requires a body-less function declaration") {
		t.Fatalf("expected extern definition diagnostic, got:\n%s", out)
	}
	if entry.Phase != project.PhaseHIR {
		t.Fatalf("expected pipeline to continue through HIR and stop before MIR, got phase %v", entry.Phase)
	}
	if entry.HIR == nil {
		t.Fatalf("expected HIR despite extern definition error")
	}
	if len(entry.HIR.Externs) != 0 {
		t.Fatalf("extern definition should not lower as import, got externs %#v", entry.HIR.Externs)
	}
	foundPuts := false
	for _, fn := range entry.HIR.Funcs {
		if fn != nil && fn.Name == "puts" {
			foundPuts = true
			break
		}
	}
	if !foundPuts {
		t.Fatalf("expected local lowered function for puts, got funcs %#v", entry.HIR.Funcs)
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
		project.PhaseConstEval,
		project.PhaseTypechecked,
		project.PhaseOwnership,
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

func TestPipelineAcceptsImplicitMoveSurface(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Buffer {
	ptr: i32,
}

fn get_buffer() -> Buffer {
	return .{ ptr = 0 };
}
fn destroy(_: Buffer) {}

fn main() -> i32 {
	let current: Buffer = get_buffer();
	let next = current;
	destroy(next);
	return 0;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersAddressExprSurface(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn main() -> i32 {
	let mut value: i32 = 1;
	let ptr: rawptr = @value;
	return value;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersAddressOfFieldStorage(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Box {
	value: i32
}

#[extern]
fn use_ptr(ptr: rawptr);

fn main() -> i32 {
	let mut box: Box = .{ value = 1 };
	let ptr: rawptr = @box.value;
	use_ptr(ptr);
	return 0;
}`

	const preludePath = "core/global" + peeper.SourceExt
	const entryPath = "entry" + peeper.SourceExt

	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(preludePath, preludeSrc)
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)

	entry := parseModuleSource(entryPath, entrySrc, diag)
	entry.Origin = project.ModuleOriginLocal

	if err := New(ctx).Run(entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(entry.MIR.Text(), " = addr ") || !strings.Contains(entry.MIR.Text(), ".0") {
		t.Fatalf("expected address-of field to retain direct place, MIR:\n%s", entry.MIR.Text())
	}
	if strings.Contains(entry.MIR.Text(), "projectfield") {
		t.Fatalf("legacy field projection instruction remains, MIR:\n%s", entry.MIR.Text())
	}
	if strings.Contains(entry.LLVMIR, "alloca i32") {
		t.Fatalf("unexpected temp alloca for address-of field, LLVM IR:\n%s", entry.LLVMIR)
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
		t.Fatalf("resolved importer should wait for const-evaluated import before consteval")
	}

	imported.Phase = project.PhaseConstEval
	if !pipeline.moduleReadyForNextPhase(entry, nil, true) {
		t.Fatalf("resolved importer should be ready for consteval when import is const-evaluated")
	}
}

func TestPipelineRunResolvesImportedModuleWithScheduler(t *testing.T) {
	root := t.TempDir()
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.NewWithConfig(project.Config{RootDir: root, ProjectName: "app", Extension: peeper.SourceExt}, diag)

	utilPath := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	utilSrc := `fn Helper() -> i32 { return 7; }`
	mainSrc := `import "app/util";
fn main() -> i32 {
	return util::Helper();
}`
	diag.AddSourceContent(utilPath, utilSrc)
	diag.AddSourceContent(mainPath, mainSrc)

	if err := os.MkdirAll(filepath.Dir(utilPath), 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}

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

func TestPipelineLowersNoneForOptional(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn maybe() -> ?i32 {
	return none;
}

fn main() -> i32 {
	let _: ?i32 = maybe();
	return 0;
}`
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersValueForOptional(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn maybe() -> ?i32 {
	return 7;
}

fn main() -> i32 {
	let _: ?i32 = maybe();
	return 0;
}`
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersBoolLiterals(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn maybe(cond: bool) -> ?i32 {
	if cond {
		return 7;
	}
	return none;
}

fn main() -> i32 {
	let _: ?i32 = maybe(true);
	let _: ?i32 = maybe(false);
	return 0;
}`
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineComparesOptionalWithNone(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn main() -> i32 {
	let x: ?i32 = none;
	if x == none {
		return 0;
	}
	return 1;
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

func TestPipelineLowersReceiverFunctionCalls(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Number { value: i32 }

fn (self: Number) abs() -> Number {
		return self;
}

fn main() -> i32 {
	let x: Number = .{ value = 1 };
	return x.abs().value;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersPointerReceiverOnNamedStruct(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct File {}

fn open_file() -> *File {
	return alloc(.File{});
}

	fn (self: *File) read(buf: cstr) -> i32 {
		return 0;
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
	next: rawptr
}

#[extern]
fn next_node() -> rawptr;

fn main() -> i32 {
	let node: Node = .{ next = next_node() };
	let next: rawptr = node.next;
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

func TestPipelineLowersAutoAddressedMutableReferenceReceiverOnValue(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Number { value: i32 }

fn (self: &mut Number) id() -> i32 {
		return 7;
}

fn main() -> i32 {
	let mut x: Number = .{ value = 1 };
	return x.id();
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersReferenceReceiversOnValue(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Counter {
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
		return self.twice();
	}

fn main() -> i32 {
	let mut c: Counter = .{ value = 6 };
	return c.touch();
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineBorrowsNestedReferenceReceiverFromFieldPlace(t *testing.T) {
	entrySrc := `struct Inner {
	value: i32
}

	fn (self: &mut Inner) bump() {
		self.value = self.value + 1;
	}

struct Outer {
	inner: Inner
}

fn main() -> i32 {
	let mut outer: Outer = .{ inner = .{ value = 1 } };
	outer.inner.bump();
	return outer.inner.value;
}`
	const entryPath = "entry" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	entry := parseModuleSource(entryPath, entrySrc, diag)
	entry.Origin = project.ModuleOriginLocal

	if err := New(ctx).Run(entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if entry.MIR == nil {
		t.Fatalf("expected nested receiver borrow MIR")
	}
	if !strings.Contains(entry.MIR.Text(), " = addr ") || !strings.Contains(entry.MIR.Text(), ".0") {
		t.Fatalf("expected nested receiver borrow to retain original field place, MIR:\n%s", entry.MIR.Text())
	}
	if strings.Contains(entry.MIR.Text(), "projectfield") {
		t.Fatalf("legacy field projection instruction remains, MIR:\n%s", entry.MIR.Text())
	}
	if strings.Contains(entry.LLVMIR, "alloca { i32 }") {
		t.Fatalf("nested receiver borrow must not allocate copied field, LLVM IR:\n%s", entry.LLVMIR)
	}
}

func TestPipelineLowersPointerFieldAssignment(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Counter {
	value: i32
}

fn open_counter() -> *Counter {
	return alloc(.Counter{ value = 0 });
}

	fn (self: *Counter) bump() -> i32 {
		self.value = self.value + 1;
		return self.value;
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

func TestPipelineLowersArrayIndexRead(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn first(xs: [4]i32) -> i32 {
	return xs[0];
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersCallResultArrayIndexRead(t *testing.T) {
	preludeSrc := ``
	entrySrc := `#[extern]
fn make() -> [4]i32;

fn first() -> i32 {
	return make()[0];
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersInferredArrayLiteralIndexRead(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn first() -> i32 {
	let arr = [_]i32{1, 2, 3};
	return arr[0];
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersInferredArrayLiteralIndexAssignment(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn first() -> i32 {
	let mut arr = [_]i32{1, 2, 3};
	arr[0] = 9;
	return arr[0];
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersDynamicArrayLiteral(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn make_values() -> []i32 {
	return []i32{1, 2, 3};
}

fn first() -> i32 {
	let values = make_values();
	return values[0];
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersSliceViewIndexReadAndWrite(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn read_at(xs: &[]i32, index: usize) -> i32 {
	return xs[index];
}

fn write_at(xs: &mut []i32, index: usize, value: i32) {
	xs[index] = value;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersRangeSliceForms(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn ranges(xs: [4]i32) -> i32 {
	let prefix = xs[..2];
	let suffix = xs[1..];
	let middle = xs[1..3];
	let inclusive = xs[1..=2];
	let full = xs[..];
	return prefix[0] + suffix[0] + middle[0] + inclusive[0] + full[0];
}

fn mutate(mut xs: [4]i32) {
	let middle = xs[1..3];
	middle[0] = 9;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected range slicing diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineRejectsConstantArrayIndexOutOfBounds(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn first() -> i32 {
	let arr = [_]i32{1, 2, 3, 4};
	return arr[4];
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if !diag.HasErrors() || !strings.Contains(diag.EmitAllToString(), "array index out of bounds: index 4 for length 4") {
		t.Fatalf("expected out-of-bounds diagnostic, got:\n%s", diag.EmitAllToString())
	}
	items := diag.Diagnostics()
	if len(items) == 0 || len(items[0].Labels) == 0 || items[0].Labels[0].Location == nil {
		t.Fatalf("expected located out-of-bounds diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineRejectsTopLevelConstArrayIndexOutOfBounds(t *testing.T) {
	preludeSrc := ``
	entrySrc := `const I: i32 = 4;

fn first() -> i32 {
	let arr = [_]i32{1, 2, 3, 4};
	return arr[I];
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	out := diag.EmitAllToString()
	if !diag.HasErrors() || !strings.Contains(out, "array index out of bounds: index 4 for length 4") {
		t.Fatalf("expected out-of-bounds diagnostic, got:\n%s", out)
	}
	if strings.Contains(out, "dynamic array index lowering requires bounds policy") {
		t.Fatalf("unexpected backend dynamic-index diagnostic:\n%s", out)
	}
}

func TestPipelineLowersTopLevelConstArrayIndex(t *testing.T) {
	preludeSrc := ``
	entrySrc := `const I: i32 = 1;

fn first() -> i32 {
	let arr = [_]i32{1, 2, 3, 4};
	return arr[I];
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersBasePrefixedArrayLengthsValuesAndIndexes(t *testing.T) {
	entrySrc := `fn main() -> i32 {
	let values = [0x2]i32{0xa, 0x14};
	return values[0x1] - 0x14;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, "", entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersMixedWidthConstArrayIndex(t *testing.T) {
	preludeSrc := ``
	entrySrc := `const A = 1;
const W: i64 = 2;
const B = A + W;

fn first() -> i32 {
	let arr = [_]i32{1, 2, 3, 4};
	return arr[B];
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

func TestPipelineLowersForLoop(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn main() -> i32 {
	for 1 < 2 {
		return 1;
	}
	return 0;
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

fn open_point() -> *Point {
	return alloc(.Point{ x = 0, y = 0 });
}

fn main() -> i32 {
	let p = open_point();
	return p.x;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersBorrowedInterfaceDispatch(t *testing.T) {
	preludeSrc := ``
	entrySrc := `iface Reader {
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

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersOwnedInterfaceDirectCarrier(t *testing.T) {
	preludeSrc := ``
	entrySrc := `iface Reader {
	fn (&Self) read() -> i32
}

struct Counter {
	value: i32
}

	fn (self: &Counter) read() -> i32 {
		return self.value;
	}

fn convert(counter: *Counter) -> *Reader {
	return counter;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected owned-interface conversion diagnostic:\n%s", diag.EmitAllToString())
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
	value: i32
}
	fn (self: &mut Counter) bump() -> i32 {
		self.value = self.value + 1;
		return self.value;
	}
struct Container {
	counter: Counter
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

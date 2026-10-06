package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/graph"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/mir"
	"compiler/internal/ir/thir"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/phase"
	"compiler/internal/prelude"
	"compiler/internal/project"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

func TestInvalidateSemanticDependentsUsesImportClosure(t *testing.T) {
	ctx := project.New(".", peeper.SourceExt, diagnostics.NewDiagnosticBag())
	ctx.Metrics = &project.CompileMetrics{}
	leaf := &module.Module{
		ID:                        moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "leaf"},
		Phase:                     phase.Typechecked,
		SemanticExportFingerprint: "new surface",
	}
	middle := &module.Module{ID: moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "middle"}, Phase: phase.Typechecked}
	root := &module.Module{ID: moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "root"}, Phase: phase.Typechecked}
	unrelated := &module.Module{ID: moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "unrelated"}, Phase: phase.Typechecked}
	for _, module := range []*module.Module{leaf, middle, root, unrelated} {
		ctx.AddModule(module)
	}
	ctx.ImportGraph.AddEdge(graph.NodeID(root.ID.String()), graph.NodeID(middle.ID.String()))
	ctx.ImportGraph.AddEdge(graph.NodeID(middle.ID.String()), graph.NodeID(leaf.ID.String()))
	ctx.SetSemanticExportBaseline(leaf.ID, "old surface")

	invalidateSemanticDependents(ctx, []*module.Module{leaf})

	if leaf.Phase != phase.Typechecked {
		t.Fatalf("changed module phase = %v, want %v", leaf.Phase, phase.Typechecked)
	}
	if middle.Phase != phase.Parsed || root.Phase != phase.Parsed {
		t.Fatalf("dependent phases = (%v, %v), want (%v, %v)", middle.Phase, root.Phase, phase.Parsed, phase.Parsed)
	}
	if unrelated.Phase != phase.Typechecked {
		t.Fatalf("unrelated module phase = %v, want %v", unrelated.Phase, phase.Typechecked)
	}
	if got := ctx.Metrics.Snapshot().ModulesDowngraded; got != 2 {
		t.Fatalf("downgraded modules = %d, want 2", got)
	}
}

func parseModuleSource(filePath, src string, diag *diagnostics.DiagnosticBag) *module.Module {
	return &module.Module{
		ID: moduleid.ID{
			Origin:     string(project.ModuleOriginLocal),
			ImportPath: strings.TrimSuffix(filePath, peeper.SourceExt),
		},
		FilePath: filePath,
		AST:      parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]module.ResolvedImport),
	}
}

func buildPipelineTestWithConfig(t *testing.T, cfg project.Config, preludeSrc, entrySrc string, afterRun ...func(*module.Module)) *diagnostics.DiagnosticBag {
	t.Helper()
	const preludePath = "core/global" + peeper.SourceExt
	const entryPath = "entry" + peeper.SourceExt

	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(preludePath, preludeSrc)
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.NewWithConfig(cfg, diag)

	// Register the prelude so the pipeline loader can find it.
	preludeModule := parseModuleSource(preludePath, preludeSrc, diag)
	preludeModule.ID = prelude.ModuleID(ctx)
	ctx.AddModule(preludeModule)

	entry := parseModuleSource(entryPath, entrySrc, diag)

	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	for _, inspect := range afterRun {
		inspect(entry)
	}
	return diag
}

func assertLLVMCompiles(t *testing.T, compilerTarget target.Info, text string) {
	t.Helper()
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang unavailable for LLVM IR validation")
	}
	cmd := exec.Command(clang, "-target", compilerTarget.LLVMTriple, "-x", "ir", "-c", "-o", filepath.Join(t.TempDir(), "module.o"), "-")
	cmd.Stdin = strings.NewReader(text)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s LLVM is invalid: %v\n%s\n%s", compilerTarget.LLVMTriple, err, output, text)
	}
}

func runImportedRuntimeSymbolPipeline(t *testing.T, entrySrc, runtimeSrc string) *diagnostics.DiagnosticBag {
	t.Helper()
	ctx, entry := importedPipelineContext(t, "", map[string]string{
		peeper.MainFileName: entrySrc, "runtime" + peeper.SourceExt: runtimeSrc,
	})
	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	return ctx.Diagnostics
}

func importedPipelineContext(t *testing.T, arch string, sources map[string]string) (*project.CompilerContext, *module.Module) {
	t.Helper()
	root := t.TempDir()
	srcDir := filepath.Join(root, peeper.SourceDirName)
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	for name, src := range sources {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	diag := diagnostics.NewDiagnosticBag()
	cfg := project.Config{
		RootDir:     root,
		ProjectName: "app",
		Extension:   peeper.SourceExt,
		TargetArch:  arch,
	}
	if arch != "" {
		cfg.TargetOS = "linux"
	}
	ctx := project.NewWithConfig(cfg, diag)
	entry := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "app/main"},
		FilePath: filepath.Join(srcDir, peeper.MainFileName),
	}
	return ctx, entry
}

func assertCheckedConstantIndex(t *testing.T, entry *module.Module, wantValue, wantType string) {
	t.Helper()
	if entry.THIR == nil || entry.MIR == nil || entry.LLVMIR == "" {
		t.Fatal("constant index did not reach THIR, MIR and backend")
	}
	accesses := 0
	for _, stmt := range entry.AST.Stmts {
		ast.Inspect(stmt, func(node ast.Node) bool {
			if index, ok := node.(*ast.IndexExpr); ok {
				accesses++
				checked := entry.THIR.Node(index.ID()).(*thir.Index)
				if checked.Constant == nil || checked.Constant.Text != wantValue ||
					!typeinfo.IsSameType(checked.Constant.Type, checked.Index.ExprType()) || typeinfo.TypeText(typeinfo.Underlying(checked.Constant.Type)) != wantType {
					t.Fatalf("THIR index = %#v, want %s %s", checked.Constant, wantType, wantValue)
				}
			}
			return true
		})
	}
	projections := 0
	for _, function := range entry.MIR.Funcs {
		for _, block := range function.Blocks {
			for _, instruction := range block.Instrs {
				assign, ok := instruction.(*mir.Assign)
				if !ok {
					continue
				}
				load, ok := assign.Value.(*mir.Load)
				if !ok || load.Place == nil {
					continue
				}
				for _, projection := range load.Place.Projections {
					if projection.Kind != mir.PlaceProjectionIndex {
						continue
					}
					projections++
					index, ok := projection.Index.(*mir.RefConst)
					if !ok || index.Value != wantValue || entry.MIR.Types.Text(index.TypeID()) != wantType {
						t.Fatalf("MIR index = %#v, want %s %s", projection.Index, wantType, wantValue)
					}
				}
			}
		}
	}
	if accesses != 1 || projections != 1 {
		t.Fatalf("index counts: AST %d, MIR %d, want one each", accesses, projections)
	}
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

func TestPipelineChecksOwnershipInsideConstantFalseBranch(t *testing.T) {
	entrySrc := `struct Point { value: i32 }

fn consume(_: Point) {}

fn invalid(point: Point) {
	if false {
		let moved = point;
		consume(point);
	}
}`
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, "", entrySrc)
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrUseAfterMove {
			return
		}
	}
	t.Fatalf("expected use-after-move diagnostic from constant false branch, got:\n%s", diag.EmitAllToString())
}

func TestPipelineLowersSequenceIndexesAsUsizeAcrossTargets(t *testing.T) {
	for _, test := range []struct {
		arch     string
		typeText string
		llvmType string
	}{
		{arch: "386", typeText: "u32", llvmType: "i32"},
		{arch: "amd64", typeText: "u64", llvmType: "i64"},
	} {
		t.Run(test.arch, func(t *testing.T) {
			targetInfo, err := target.New("linux", test.arch)
			if err != nil {
				t.Fatal(err)
			}
			diag := buildPipelineTestWithConfig(t, project.Config{
				RootDir:    ".",
				Extension:  peeper.SourceExt,
				TargetOS:   "linux",
				TargetArch: test.arch,
			}, "", `const selected: i32 = 0;
fn main() {
	const saved: i32 = selected;
	const selected: i32 = 1;
	let items = [1]i32{1};
	let chosen = items[saved];
	for index, value in items {}
}`, func(entry *module.Module) {
				if entry.MIR == nil || len(entry.MIR.Funcs) != 1 {
					t.Fatalf("pipeline MIR missing: %#v", entry.MIR)
				}
				foundCompare := false
				foundIndexMove := false
				foundProjection := false
				for _, function := range entry.MIR.Funcs {
					for _, block := range function.Blocks {
						for _, instruction := range block.Instrs {
							assign, ok := instruction.(*mir.Assign)
							if !ok {
								continue
							}
							switch value := assign.Value.(type) {
							case *mir.Binary:
								if value.Op == "<" && entry.MIR.Types.Text(value.Left.TypeID()) == test.typeText && entry.MIR.Types.Text(value.Right.TypeID()) == test.typeText {
									foundCompare = true
								}
							case *mir.Move:
								if entry.MIR.Types.Text(value.TypeID()) == test.typeText && value.TypeID() == value.Src.TypeID() {
									foundIndexMove = true
								}
							case *mir.Load:
								if value.Place != nil && len(value.Place.Projections) == 1 && value.Place.Projections[0].Kind == mir.PlaceProjectionIndex &&
									entry.MIR.Types.Text(value.Place.Projections[0].Index.TypeID()) == test.typeText {
									foundProjection = true
								}
							}
						}
					}
				}
				if !foundCompare || !foundIndexMove || !foundProjection {
					t.Fatalf("MIR target-width evidence missing: compare=%v index=%v projection=%v\n%s", foundCompare, foundIndexMove, foundProjection, entry.MIR.Text())
				}
				if !strings.Contains(entry.LLVMIR, "icmp ult "+test.llvmType) || strings.Contains(entry.LLVMIR, "trunc i64") {
					t.Fatalf("LLVM index width invalid for %s:\n%s", test.arch, entry.LLVMIR)
				}
				assertLLVMCompiles(t, targetInfo, entry.LLVMIR)
			})
			if diag.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
			}
		})
	}
}

func TestPipelineLowersExactLoopExitCleanupToMIR(t *testing.T) {
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, "", `fn main() {
	for i in 0..3 {
		let first = alloc(i);
		if i == 0 { continue; }
		let second = alloc(i);
		if i == 1 { break; }
	}
}`, func(entry *module.Module) {
		fn := entry.AST.Stmts[0].(*ast.FnDecl)
		loop := fn.Body.Stmts[0].(*ast.ForStmt)
		continueStmt := loop.Body.Stmts[1].(*ast.IfStmt).Then.Stmts[0].(*ast.ContinueStmt)
		breakStmt := loop.Body.Stmts[3].(*ast.IfStmt).Then.Stmts[0].(*ast.BreakStmt)
		graph := entry.CFG.FunctionByID(entry.THIR.Function(fn.ID()).Identity)
		if graph == nil || entry.MIR == nil {
			t.Fatalf("pipeline artifacts missing: CFG=%v MIR=%v", graph != nil, entry.MIR != nil)
		}

		var continueExit, breakExit, fallthroughExit cfg.SiteID
		var continueFound, breakFound, fallthroughFound bool
		for _, block := range graph.Blocks {
			if block == nil || !block.IsReachable {
				continue
			}
			var exit *cfg.Site
			hasContinue := false
			hasBreak := false
			for _, site := range block.Sites {
				if site == nil {
					continue
				}
				hasContinue = hasContinue || site.NodeID == continueStmt.ID()
				hasBreak = hasBreak || site.NodeID == breakStmt.ID()
				if site.Kind == cfg.SiteScopeExit && site.NodeID == loop.Body.ID() {
					exit = site
				}
			}
			if exit == nil {
				continue
			}
			switch {
			case hasContinue:
				continueExit, continueFound = exit.ID, true
			case hasBreak:
				breakExit, breakFound = exit.ID, true
			default:
				fallthroughExit, fallthroughFound = exit.ID, true
			}
		}
		if !continueFound || !breakFound || !fallthroughFound {
			t.Fatalf("loop exits missing: continue=%v break=%v fallthrough=%v", continueFound, breakFound, fallthroughFound)
		}

		var mirFn *mir.Function
		for _, function := range entry.MIR.Funcs {
			if function.Name == "main" {
				mirFn = function
				break
			}
		}
		if mirFn == nil {
			t.Fatal("main MIR function missing")
		}
		dropNames := func(blockID int) []string {
			var names []string
			for _, block := range mirFn.Blocks {
				if block.ID != blockID {
					continue
				}
				for _, instruction := range block.Instrs {
					drop, ok := instruction.(*mir.Drop)
					if !ok {
						continue
					}
					name, ok := drop.Value.(*mir.RefName)
					if !ok {
						t.Fatalf("drop value = %#v, want named owner", drop.Value)
					}
					names = append(names, strings.SplitN(name.Name, "$", 2)[0])
				}
			}
			return names
		}
		for _, test := range []struct {
			name string
			site cfg.SiteID
			want []string
		}{
			{name: "continue", site: continueExit, want: []string{"first"}},
			{name: "break", site: breakExit, want: []string{"second", "first"}},
			{name: "fallthrough", site: fallthroughExit, want: []string{"second", "first"}},
		} {
			if got := dropNames(test.site.Block); !slices.Equal(got, test.want) {
				t.Fatalf("%s MIR drops = %v, want %v\n%s", test.name, got, test.want, entry.MIR.Text())
			}
		}
	})
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineRequiresBuildEntrypoint(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{name: "missing", src: `fn helper() {}`},
		{name: "parameter", src: `fn main(value: i32) {}`},
		{name: "wrong return", src: `fn main() -> bool { return true; }`},
		{name: "aliased return", src: `type ExitCode = i32;
fn main() -> ExitCode { return 0; }`},
		{name: "extern", src: `#[extern]
fn main();`},
		{name: "generic", src: `fn main<T>() {}`},
		{name: "method", src: `struct App {}
fn (self: App) main() {}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diag := buildPipelineTestWithConfig(t, project.Config{
				RootDir:           ".",
				Extension:         peeper.SourceExt,
				RequireEntrypoint: true,
			}, "", tt.src)
			for _, item := range diag.Diagnostics() {
				if item != nil && item.Code == diagnostics.ErrInvalidEntrypoint {
					return
				}
			}
			t.Fatalf("expected invalid entrypoint diagnostic, got:\n%s", diag.EmitAllToString())
		})
	}
}

func TestValidateEntrypointWithoutAST(t *testing.T) {
	for _, test := range []struct {
		name, source string
		valid        bool
	}{
		{"ordinary", `fn main() -> i32 { return 0; }`, true},
		{"generic", `fn main<T>() {}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			diag := diagnostics.NewDiagnosticBag()
			entry := parseModuleSource("entry"+peeper.SourceExt, test.source, diag)
			entry.Phase = phase.Parsed
			ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
			ctx.AddModule(entry)
			for entry.Phase < phase.Typechecked {
				if !advanceModulePhase(ctx, entry, diag) {
					t.Fatalf("advanceModulePhase stopped at %v", entry.Phase)
				}
			}
			symbol, found := entry.ModuleScope.LookupLocal("main")
			if !found || symbol == nil {
				t.Fatal("missing main symbol")
			}
			entry.AST = nil
			symbol.ASTNode = nil
			check := diagnostics.NewDiagnosticBag()
			validateProgramEntrypoint(entry, check)
			if valid := !check.HasErrors(); valid != test.valid {
				t.Fatalf("entrypoint validity = %v, want %v: %s", valid, test.valid, check.EmitAllToString())
			}
		})
	}
}

func TestPipelineAcceptsBuildEntrypointReturns(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{name: "void", src: `fn main() {}`},
		{name: "i32", src: `fn main() -> i32 { return 0; }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diag := buildPipelineTestWithConfig(t, project.Config{
				RootDir:           ".",
				Extension:         peeper.SourceExt,
				RequireEntrypoint: true,
			}, "", tt.src)
			if diag.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
			}
		})
	}
}

func TestPipelineCheckAllowsMissingEntrypoint(t *testing.T) {
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, "", `fn helper() {}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineImportsCoreAllocatorRuntimeABI(t *testing.T) {
	root := t.TempDir()
	libraryBase := filepath.Join(root, "libs")
	allocatorPath := filepath.Join(libraryBase, "core", peeper.SourceDirName, "allocator"+peeper.SourceExt)
	if err := os.MkdirAll(filepath.Dir(allocatorPath), 0o755); err != nil {
		t.Fatalf("mkdir allocator: %v", err)
	}
	allocatorSrc := `#[extern("peeper_rt_v1_alloc")]
fn Malloc(size: usize) -> rawptr;

#[extern("peeper_rt_v1_free")]
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
	}, diag)
	entry := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "entry"},
		FilePath: entryPath,
		Content:  entrySrc,
		Imports:  make(map[string]module.ResolvedImport),
	}

	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(entry.LLVMIR, "declare i8* @peeper_rt_v1_alloc(i64)") {
		t.Fatalf("expected malloc declaration, LLVM IR:\n%s", entry.LLVMIR)
	}
	if !strings.Contains(entry.LLVMIR, "declare void @peeper_rt_v1_free(i8*)") {
		t.Fatalf("expected free declaration, LLVM IR:\n%s", entry.LLVMIR)
	}
}

// preludeQualifierPipeline compiles one entry against a real temp library root so
// `core:` imports resolve for real, and returns the resulting diagnostics.
func preludeQualifierPipeline(t *testing.T, libraryFile, librarySrc, entrySrc string) *diagnostics.DiagnosticBag {
	t.Helper()
	root := t.TempDir()
	libraryBase := filepath.Join(root, "libs")
	libraryPath := filepath.Join(libraryBase, "core", peeper.SourceDirName, libraryFile+peeper.SourceExt)
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatalf("mkdir library: %v", err)
	}
	if err := os.WriteFile(libraryPath, []byte(librarySrc), 0o644); err != nil {
		t.Fatalf("write library: %v", err)
	}
	entryPath := filepath.Join(root, "entry"+peeper.SourceExt)
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.NewWithConfig(project.Config{
		RootDir:        root,
		Extension:      peeper.SourceExt,
		LibraryBaseDir: libraryBase,
	}, diag)
	// Production loads the prelude in the driver; do the same so global symbols
	// reach global scope and redundant qualification is observable.
	if err := prelude.Load(ctx); err != nil {
		t.Fatalf("load prelude: %v", err)
	}
	entry := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "entry"},
		FilePath: entryPath,
		Content:  entrySrc,
		Imports:  make(map[string]module.ResolvedImport),
	}
	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	return diag
}

func hasDiagnosticCode(diag *diagnostics.DiagnosticBag, code string) bool {
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}

func TestPipelineNotesRedundantPreludeImportAndQualifier(t *testing.T) {
	diag := preludeQualifierPipeline(t, "global", `fn Ping() -> i32 {
	return 7;
}
`, `import "core:global";

fn main() -> i32 {
	return global::Ping() - 7;
}`)
	if diag.HasErrors() {
		t.Fatalf("redundant prelude use must stay compilable:\n%s", diag.EmitAllToString())
	}
	if !hasDiagnosticCode(diag, diagnostics.InfoRedundantPreludeImport) {
		t.Fatalf("explicit prelude import produced no style note:\n%s", diag.EmitAllToString())
	}
	if !hasDiagnosticCode(diag, diagnostics.InfoRedundantGlobalQualifier) {
		t.Fatalf("global-qualified prelude symbol produced no style note:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineKeepsOrdinaryLibraryQualifierQuiet(t *testing.T) {
	// A non-prelude library is qualified legitimately, so neither style note applies.
	diag := preludeQualifierPipeline(t, "util", `fn Helper() -> i32 {
	return 3;
}
`, `import "core:util";

fn main() -> i32 {
	return util::Helper() - 3;
}`)
	if diag.HasErrors() {
		t.Fatalf("ordinary library import failed:\n%s", diag.EmitAllToString())
	}
	if hasDiagnosticCode(diag, diagnostics.InfoRedundantPreludeImport) {
		t.Fatalf("ordinary import reported as automatic:\n%s", diag.EmitAllToString())
	}
	if hasDiagnosticCode(diag, diagnostics.InfoRedundantGlobalQualifier) {
		t.Fatalf("ordinary qualifier reported as redundant:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineScalarShrinkOwnedParameterReservesForeignFree(t *testing.T) {
	diag := runImportedRuntimeSymbolPipeline(t, `import "app/runtime";

fn shorten(mut values: []i32) {
	values |> shrink(0);
}`, `type Word = i32;

#[extern("peeper_rt_v1_free")]
fn BadFree(value: Word);`)
	if !strings.Contains(diag.EmitAllToString(), "runtime requires fn(rawptr) -> void") {
		t.Fatalf("expected local scalar shrink cleanup to reserve free:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineScalarShrinkBorrowDoesNotReserveForeignFree(t *testing.T) {
	diag := runImportedRuntimeSymbolPipeline(t, `import "app/runtime";

fn shorten(values: &mut []i32) {
	shrink(values, 0);
}`, `type Word = i32;

#[extern("peeper_rt_v1_free")]
fn BadFree(value: Word);`)
	if diag.HasErrors() {
		t.Fatalf("borrowed scalar shrink must not reserve free:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineOwnerShrinkReservesForeignFree(t *testing.T) {
	diag := runImportedRuntimeSymbolPipeline(t, `import "app/runtime";

struct Resource { value: *i32 }

fn shorten(values: &mut []Resource) {
	shrink(values, 0);
}`, `type Word = i32;

#[extern("peeper_rt_v1_free")]
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

fn peeper_rt_v1_printf(value: i32) {}

#[extern("peeper_rt_v1_alloc")]
fn BadMalloc(size: Word) -> rawptr;

#[extern("peeper_rt_v1_free")]
fn BadFree(value: Word);`)
	out := diag.EmitAllToString()
	for _, symbol := range []string{"peeper_rt_v1_alloc", "peeper_rt_v1_free"} {
		message := "runtime symbol `" + symbol + "`"
		if count := strings.Count(out, message); count != 1 {
			t.Fatalf("expected one %s reservation diagnostic, got %d:\n%s", symbol, count, out)
		}
	}
	if strings.Contains(out, "runtime symbol `peeper_rt_v1_printf`") {
		t.Fatalf("module-mangled printf must not conflict with runtime symbol:\n%s", out)
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
		RootDir:   ".",
		Extension: peeper.SourceExt,
	}, diag)

	preludeModule := parseModuleSource("core/global"+peeper.SourceExt, preludeSrc, diag)
	preludeModule.ID = prelude.ModuleID(ctx)
	ctx.AddModule(preludeModule)

	entry := parseModuleSource("entry"+peeper.SourceExt, entrySrc, diag)
	entry.ID.ImportPath = "entry"

	if err := Run(ctx, entry); err != nil {
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

func TestPipelineSemanticErrorStopsBeforeUsageAndMIR(t *testing.T) {
	preludeSrc := ``
	entrySrc := `#[extern("puts")]
fn puts(msg: cstr) -> i32 {
	return 0;
}

fn unused() {}

fn main() -> i32 {
	return puts("hi");
}`

	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent("core/global"+peeper.SourceExt, preludeSrc)
	diag.AddSourceContent("entry"+peeper.SourceExt, entrySrc)
	ctx := project.NewWithConfig(project.Config{
		RootDir:   ".",
		Extension: peeper.SourceExt,
	}, diag)

	preludeModule := parseModuleSource("core/global"+peeper.SourceExt, preludeSrc, diag)
	preludeModule.ID = prelude.ModuleID(ctx)
	ctx.AddModule(preludeModule)

	entry := parseModuleSource("entry"+peeper.SourceExt, entrySrc, diag)
	entry.ID.ImportPath = "entry"

	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if !diag.HasErrors() {
		t.Fatalf("expected extern definition diagnostic")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "attribute `#[extern]` requires a body-less function declaration") {
		t.Fatalf("expected extern definition diagnostic, got:\n%s", out)
	}
	if entry.Phase != phase.Analyzed {
		t.Fatalf("expected pipeline to finish analysis and stop before Usage/MIR, got phase %v", entry.Phase)
	}
	if ctx.CompletedProjectPhase != phase.Analyzed {
		t.Fatalf("completed project phase = %v, want Analyzed", ctx.CompletedProjectPhase)
	}
	if entry.MIR != nil {
		t.Fatalf("semantic error produced MIR: %#v", entry.MIR)
	}
	if entry.CFG == nil || len(entry.CFG.Functions) == 0 {
		t.Fatal("expected canonical CFG despite extern definition error")
	}
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.WarnUnusedPrivateFunction {
			t.Fatalf("Usage ran on project with semantic errors: %#v", item)
		}
	}
}

func TestPipelineSkipsIncompleteAnalysisValidationDuringRecovery(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	const entryPath = "entry" + peeper.SourceExt
	entry := parseModuleSource(entryPath, `fn main() {}`, diag)
	entry.Phase = phase.Parsed
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	ctx.AddModule(entry)

	for entry.Phase < phase.CFG {
		if !advanceModulePhase(ctx, entry, diag) {
			t.Fatalf("advanceModulePhase() stopped at %v", entry.Phase)
		}
	}
	diag.AddError(diagnostics.ErrInvalidAssignment, "source error", nil, "")
	entry.THIR.Functions = nil
	if !advanceModulePhase(ctx, entry, diag) || entry.Phase != phase.Analyzed {
		t.Fatalf("phase = %v, want Analyzed", entry.Phase)
	}
	if entry.Analysis == nil {
		t.Fatal("analysis artifact missing during source-error recovery")
	}
	if hasDiagnosticCode(diag, diagnostics.ErrInvalidEvidence) {
		t.Fatalf("source-error recovery reported invalid evidence:\n%s", diag.EmitAllToString())
	}
}

func TestPipelinePreservesDiagnosticsForIdenticalRedeclarations(t *testing.T) {
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, "", `
fn same() -> i32 {
	let mut value: i32;
	return value;
}
fn same() -> i32 { return 2; }
`)
	out := diag.EmitAllToString()
	for _, code := range []string{diagnostics.ErrRedeclaredSymbol, diagnostics.ErrUninitializedVariable} {
		if !strings.Contains(out, code) {
			t.Fatalf("identical redeclarations lost %s diagnostic:\n%s", code, out)
		}
	}
}

func TestPipelineRejectsUnsupportedComparisonsBeforeMIR(t *testing.T) {
	preludeSrc := ``
	entrySrc := `struct Pair {
	value: i32
}

fn invalid(left: Pair, right: Pair) -> bool {
	return left == right;
}

fn main() -> i32 {
	return 0;
}`

	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent("core/global"+peeper.SourceExt, preludeSrc)
	diag.AddSourceContent("entry"+peeper.SourceExt, entrySrc)
	ctx := project.NewWithConfig(project.Config{
		RootDir:   ".",
		Extension: peeper.SourceExt,
	}, diag)

	preludeModule := parseModuleSource("core/global"+peeper.SourceExt, preludeSrc, diag)
	preludeModule.ID = prelude.ModuleID(ctx)
	ctx.AddModule(preludeModule)

	entry := parseModuleSource("entry"+peeper.SourceExt, entrySrc, diag)
	entry.ID.ImportPath = "entry"

	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if !diag.HasErrors() {
		t.Fatal("expected unsupported struct comparison diagnostic")
	}
	if entry.MIR != nil {
		t.Fatalf("unsupported comparison produced MIR: %#v", entry.MIR)
	}
	if entry.Phase != phase.Analyzed {
		t.Fatalf("expected pipeline to stop before MIR at Analyzed, got phase %v", entry.Phase)
	}
}

func TestPipelineRunDoesNotRepeatUsageWarnings(t *testing.T) {
	const filePath = "repeated_usage" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, `fn unused() {}
fn main() -> i32 { return 0; }`)
	ctx := project.New(".", peeper.SourceExt, diag)
	entry := parseModuleSource(filePath, `fn unused() {}
fn main() -> i32 { return 0; }`, diag)

	if err := Run(ctx, entry); err != nil {
		t.Fatalf("first Pipeline.Run: %v", err)
	}
	first := diag.WarningCount()
	if err := Run(ctx, entry); err != nil {
		t.Fatalf("second Pipeline.Run: %v", err)
	}
	if second := diag.WarningCount(); second != first {
		t.Fatalf("warning count after repeated run = %d, want %d", second, first)
	}
}

func TestPipelineReportsPreludeGlobalCollision(t *testing.T) {
	diag := buildPipelineTestWithConfig(
		t,
		project.Config{RootDir: ".", Extension: peeper.SourceExt},
		`fn len(value: i32) -> i32 { return value; }`,
		`fn main() -> i32 { return 0; }`,
	)
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrRedeclaredSymbol && strings.Contains(item.Message, "len") {
			return
		}
	}
	t.Fatalf("expected prelude/global collision diagnostic, got:\n%s", diag.EmitAllToString())
}

func TestPipelineRunReplacesStaleFinalizeDiagnostics(t *testing.T) {
	const filePath = "stale_finalize" + peeper.SourceExt
	const sourceText = "fn main() -> i32 { return 0; }"
	diag := diagnostics.NewDiagnosticBag()
	diag.BeginPhase(phase.Finalize, "").Add(diagnostics.NewWarning("stale finalize"))
	diag.AddSourceContent(filePath, sourceText)
	ctx := project.New(".", peeper.SourceExt, diag)
	entry := parseModuleSource(filePath, sourceText, diag)

	if err := Run(ctx, entry); err != nil {
		t.Fatalf("Pipeline.Run: %v", err)
	}
	if ctx.CompletedProjectPhase != phase.Finalize {
		t.Fatalf("completed project phase = %v, want Finalize", ctx.CompletedProjectPhase)
	}
	for _, item := range diag.Diagnostics() {
		if item.Message == "stale finalize" {
			t.Fatal("pipeline retained stale finalize diagnostic")
		}
	}
}

func TestPipelineDebugBuildEmitsLLVMMetadata(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn main() -> i32 {
	return 0;
}`

	cfg := project.Config{
		RootDir:      ".",
		Extension:    peeper.SourceExt,
		TargetOS:     "linux",
		TargetArch:   "amd64",
		IsDebugBuild: true,
	}
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent("core/global"+peeper.SourceExt, preludeSrc)
	diag.AddSourceContent("entry"+peeper.SourceExt, entrySrc)
	ctx := project.NewWithConfig(cfg, diag)

	preludeModule := parseModuleSource("core/global"+peeper.SourceExt, preludeSrc, diag)
	preludeModule.ID = prelude.ModuleID(ctx)
	ctx.AddModule(preludeModule)

	entry := parseModuleSource("entry"+peeper.SourceExt, entrySrc, diag)
	entry.ID.ImportPath = "entry"

	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(entry.LLVMIR, "!llvm.dbg.cu") {
		t.Fatalf("expected debug metadata in LLVM IR, got:\n%s", entry.LLVMIR)
	}
}

func TestAdvancePublishesAnalyzedArtifact(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	const entryPath = "entry" + peeper.SourceExt
	entrySrc := `fn main() -> i32 {
	return 0;
}`
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	entry := parseModuleSource(entryPath, entrySrc, diag)
	entry.Phase = phase.Parsed
	ctx.AddModule(entry)

	want := []phase.Phase{
		phase.Collected,
		phase.Bound,
		phase.Resolved,
		phase.Typechecked,
		phase.CFG,
		phase.Analyzed,
	}
	for _, wantPhase := range want {
		if !advanceModulePhase(ctx, entry, diag) {
			t.Fatalf("advanceModulePhase() stopped at %v, want %v", entry.Phase, wantPhase)
		}
		if entry.Phase != wantPhase {
			t.Fatalf("phase = %v, want %v", entry.Phase, wantPhase)
		}
		if wantPhase == phase.CFG && (entry.CFG == nil || len(entry.CFG.Functions) == 0) {
			t.Fatal("CFG phase must retain canonical graph")
		}
		if wantPhase == phase.Analyzed && entry.Analysis == nil {
			t.Fatal("analyzed phase must retain canonical analysis artifact")
		}
		if wantPhase < phase.MIR && entry.MIR != nil {
			t.Fatalf("phase %v produced MIR before mandatory semantics completed", wantPhase)
		}
	}
	if advanceModulePhase(ctx, entry, diag) {
		t.Fatal("per-module scheduler crossed project-wide Usage barrier")
	}
	entry.Phase = phase.Usage
	for _, wantPhase := range []phase.Phase{
		phase.MIR,
		phase.Backend,
	} {
		if !advanceModulePhase(ctx, entry, diag) {
			t.Fatalf("advanceModulePhase() stopped at %v, want %v", entry.Phase, wantPhase)
		}
		if entry.Phase != wantPhase {
			t.Fatalf("phase = %v, want %v", entry.Phase, wantPhase)
		}
	}
	if advanceModulePhase(ctx, entry, diag) {
		t.Fatalf("advanceModulePhase() reported progress after backend phase")
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestTypecheckedPhasePublishesModuleConstants(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	const entryPath = "entry" + peeper.SourceExt
	entry := parseModuleSource(entryPath, `const Value = 1;
fn main() -> i32 { return Value; }
`, diag)
	entry.Phase = phase.Parsed
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	ctx.AddModule(entry)
	for entry.Phase < phase.Resolved {
		if !advanceModulePhase(ctx, entry, diag) {
			t.Fatalf("advanceModulePhase() stopped at %v", entry.Phase)
		}
	}
	sym, found := entry.ModuleScope.LookupLocal("Value")
	if !found || sym == nil {
		t.Fatal("missing const symbol Value")
	}
	stale, ok := constvalue.NewIntText("1", "i64")
	if !ok {
		t.Fatal("failed to construct stale const value")
	}
	entry.SymbolIndex.PublishConstant(sym.ID, stale)
	if !advanceModulePhase(ctx, entry, diag) || entry.Phase != phase.Typechecked {
		t.Fatalf("phase = %v, want typechecked", entry.Phase)
	}
	if got := entry.SymbolIndex.ConstantValue(sym.ID); got == nil || got.TypeText() != "i32" {
		t.Fatalf("final const value = %#v, want i32", got)
	}
}

func TestPipelineMaterializesNumericConstantsAcrossTargets(t *testing.T) {
	for _, arch := range []string{"386", "amd64"} {
		for _, test := range []struct{ name, source, typ, value string }{
			{"mixed widths", "const Value: i64 = 1i32 + 2i64;", "i64", "3"},
			{"narrow initializer", "const Value: i64 = 2i8;", "i64", "2"},
			{"narrow type interned", "const Narrow: i8 = 1i8; const Value: i64 = 2i8;", "i64", "2"},
			{"float narrowing", "const Value: f32 = 1.25f64 as f32;", "f32", "1.25"},
			{"source overflow", "const Value: i64 = 127i8 + 1i8;", "i64", "-128"},
			{"integer float rounding", "const Value: f32 = 9223372586610589697u64 as f32;", "f32", "9.223373e+18"},
			{"integer float infinity", "const Value: f64 = (1u2048 << 1024u2048) as f64;", "f64", "+Inf"},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				source := test.source + "\nfn Read(value: " + test.typ + ") -> " + test.typ + " { return value; }\nfn main() { let value = Read(Value); }"
				diag := buildPipelineTestWithConfig(t, project.Config{
					RootDir: ".", Extension: peeper.SourceExt, TargetOS: "linux", TargetArch: arch,
				}, "", source, func(entry *module.Module) {
					if entry.MIR == nil || entry.LLVMIR == "" {
						t.Fatal("constant did not reach backend")
					}
					symbol, found := entry.ModuleScope.LookupLocal("Value")
					if !found || symbol == nil {
						t.Fatal("missing constant Value")
					}
					name := "@Value$" + symbol.ID.String()
					var static *mir.StaticEntry
					for _, candidate := range entry.MIR.StaticData {
						if candidate.Name == name {
							static = candidate
						}
					}
					if static == nil || entry.MIR.Types.Text(static.Type) != test.typ || static.Constant != entry.SymbolIndex.ConstantValue(symbol.ID) {
						t.Fatalf("static = %#v, want authoritative %s constant", static, test.typ)
					}
					text, ok := static.Constant.(interface{ Text() string })
					if !ok || text.Text() != test.value || static.Constant.TypeText() != test.typ {
						t.Fatalf("static value = %#v, want %s %s", static.Constant, test.value, test.typ)
					}
					if !strings.Contains(entry.LLVMIR, name+" = constant ") {
						t.Fatal("LLVM lacks required constant definition")
					}
					targetInfo, err := target.New("linux", arch)
					if err != nil {
						t.Fatal(err)
					}
					assertLLVMCompiles(t, targetInfo, entry.LLVMIR)
				})
				if diag.HasErrors() {
					t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
				}
			})
		}
	}
}

func TestPipelineOmitsDiscardConstantStaticsAcrossTargets(t *testing.T) {
	for _, arch := range []string{"386", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			diag := buildPipelineTestWithConfig(t, project.Config{
				RootDir: ".", Extension: peeper.SourceExt, TargetOS: "linux", TargetArch: arch,
			}, "", `const _: i32 = 1i32;
const _: f64 = 1.25f32;
fn main() -> i32 { return 0; }`, func(entry *module.Module) {
				if entry.MIR == nil || entry.LLVMIR == "" || len(entry.MIR.StaticData) != 0 {
					t.Fatalf("discard bindings require no static data: MIR=%#v LLVM=%v", entry.MIR, entry.LLVMIR != "")
				}
			})
			if diag.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
			}
		})
	}
}

func TestTypecheckedPhaseFinalizesNamedVariantConstants(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	const entryPath = "entry" + peeper.SourceExt
	entry := parseModuleSource(entryPath, `enum Status {
	Ready: { code: i32, enabled: bool },
	Waiting,
}
const Ready: Status = Status::Ready with .{ code = 7, enabled = true };
const Waiting: Status = Status::Waiting;
const ReadyIsReady: bool = Ready is Status::Ready;
const WaitingIsReady: bool = Waiting is Status::Ready;
`, diag)
	entry.Phase = phase.Parsed
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	ctx.AddModule(entry)
	for entry.Phase < phase.Typechecked {
		if !advanceModulePhase(ctx, entry, diag) {
			t.Fatalf("advanceModulePhase() stopped at %v", entry.Phase)
		}
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	readySymbol, found := entry.ModuleScope.LookupLocal("Ready")
	if !found || readySymbol == nil {
		t.Fatal("missing const symbol Ready")
	}
	readyValue := entry.SymbolIndex.ConstantValue(readySymbol.ID)
	ready, ok := readyValue.(*constvalue.VariantConst)
	if !ok || ready == nil || ready.NominalIdentity() == "" || ready.CaseIndex() != 0 || len(ready.FieldValues()) != 2 {
		t.Fatalf("Ready constant = %#v, want named case 0 with two fields", readyValue)
	}
	code, ok := ready.FieldValues()[0].(*constvalue.IntConst)
	if !ok || code.Text() != "7" {
		t.Fatalf("Ready.code = %#v, want i32 7", ready.FieldValues()[0])
	}
	assertPipelineBoolConst(t, entry, "ReadyIsReady", true)
	assertPipelineBoolConst(t, entry, "WaitingIsReady", false)
}

func TestPipelineEmitsStaticForFunctionUnreferencedVariantConstant(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	const entryPath = "entry" + peeper.SourceExt
	entry := parseModuleSource(entryPath, `enum Status {
	Ready,
	Waiting,
}
const Selected: Status = Status::Ready;

fn main() -> i32 {
	return 0;
}
`, diag)
	ctx := project.NewWithConfig(project.Config{
		RootDir:           ".",
		Extension:         peeper.SourceExt,
		RequireEntrypoint: true,
	}, diag)
	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if entry.MIR == nil || len(entry.MIR.StaticData) != 1 {
		t.Fatalf("static data = %#v, want Selected variant constant", entry.MIR)
	}
	if _, ok := entry.MIR.StaticData[0].Constant.(*constvalue.VariantConst); !ok {
		t.Fatalf("static constant = %#v, want typed variant", entry.MIR.StaticData[0].Constant)
	}
}

func assertPipelineBoolConst(t *testing.T, module *module.Module, name string, want bool) {
	t.Helper()
	sym, found := module.ModuleScope.LookupLocal(name)
	if !found || sym == nil {
		t.Fatalf("missing const symbol %s", name)
	}
	published := module.SymbolIndex.ConstantValue(sym.ID)
	value, ok := published.(*constvalue.BoolConst)
	if !ok || value == nil || value.Bool() != want {
		t.Fatalf("%s = %#v, want bool %t", name, published, want)
	}
}

func TestPipelineFinalizesMissingReturnDiagnosticInCFGPhase(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	const entryPath = "entry" + peeper.SourceExt
	entry := parseModuleSource(entryPath, `fn choose(cond: bool) -> i32 {
	if cond {
		return 7;
	}
}`, diag)
	entry.Phase = phase.Parsed
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	ctx.AddModule(entry)

	for entry.Phase < phase.CFG {
		if !advanceModulePhase(ctx, entry, diag) {
			t.Fatalf("advanceModulePhase stopped at %v", entry.Phase)
		}
	}
	if entry.Phase != phase.CFG {
		t.Fatalf("phase = %v, want CFG", entry.Phase)
	}
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrMissingReturn {
			return
		}
	}
	t.Fatalf("missing-return diagnostic unavailable at CFG phase:\n%s", diag.EmitAllToString())
}

func TestPipelineReportsConstantConditionsInCFGPhase(t *testing.T) {
	for _, test := range []struct {
		name, source, warning string
	}{
		{"literal false", `fn main() -> i32 {
	if false { return 1; }
	return 0;
}`, diagnostics.WarnConstantConditionFalse},
		{"local true", `fn main() { const Flag = true; if Flag { print(1); } }`, diagnostics.WarnConstantConditionTrue},
		{"local false", `fn main() { const Flag = false; if Flag { print(1); } }`, diagnostics.WarnConstantConditionFalse},
		{"const shadow true", `fn main() {
	const value: i32 = 10;
	{
		const saved: i32 = value;
		const value: i32 = 20;
		if saved == 10 { print(1); }
	}
}`, diagnostics.WarnConstantConditionTrue},
		{"const shadow false", `fn main() {
	const value: i32 = 10;
	{
		const saved: i32 = value;
		const value: i32 = 20;
		if saved == 20 { print(1); }
	}
}`, diagnostics.WarnConstantConditionFalse},
		{"let shadow true", `fn main() {
	const value: i32 = 10;
	{
		const saved: i32 = value;
		let value: i32 = 20;
		if saved == 10 { print(1); }
	}
}`, diagnostics.WarnConstantConditionTrue},
		{"let shadow false", `fn main() {
	const value: i32 = 10;
	{
		const saved: i32 = value;
		let value: i32 = 20;
		if saved == 20 { print(1); }
	}
}`, diagnostics.WarnConstantConditionFalse},
	} {
		t.Run(test.name, func(t *testing.T) {
			diag := diagnostics.NewDiagnosticBag()
			entry := parseModuleSource("entry"+peeper.SourceExt, test.source, diag)
			entry.Phase = phase.Parsed
			ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
			ctx.AddModule(entry)
			for entry.Phase < phase.Typechecked {
				if !advanceModulePhase(ctx, entry, diag) {
					t.Fatalf("advanceModulePhase stopped at %v", entry.Phase)
				}
			}
			if diag.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
			}
			for _, item := range diag.Diagnostics() {
				if item != nil && (item.Code == diagnostics.WarnConstantConditionTrue || item.Code == diagnostics.WarnConstantConditionFalse) {
					t.Fatal("constant-condition warning emitted before CFG phase")
				}
			}
			entry.AST = nil
			if !advanceModulePhase(ctx, entry, diag) || entry.Phase != phase.CFG {
				t.Fatal("CFG phase did not consume THIR without AST")
			}
			if diag.HasErrors() {
				t.Fatalf("unexpected CFG diagnostics:\n%s", diag.EmitAllToString())
			}
			if entry.MIR != nil {
				t.Fatalf("CFG phase produced MIR: %#v", entry.MIR)
			}
			warnings := 0
			for _, item := range diag.Diagnostics() {
				if item == nil || (item.Code != diagnostics.WarnConstantConditionTrue && item.Code != diagnostics.WarnConstantConditionFalse) {
					continue
				}
				if item.Code != test.warning {
					t.Fatalf("wrong constant-condition warning %s:\n%s", item.Code, diag.EmitAllToString())
				}
				warnings++
			}
			if warnings != 1 {
				t.Fatalf("got %d warnings %s, want exactly one:\n%s", warnings, test.warning, diag.EmitAllToString())
			}
		})
	}
}

func TestPipelineDefersConstantEvaluationCycleToCFG(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	entry := parseModuleSource("entry"+peeper.SourceExt, `fn main() {
		const First: bool = First;
		if First { print(1); }
	}`, diag)
	entry.Phase = phase.Parsed
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	ctx.AddModule(entry)
	for entry.Phase < phase.Typechecked {
		if !advanceModulePhase(ctx, entry, diag) {
			t.Fatalf("advanceModulePhase stopped at %v", entry.Phase)
		}
	}
	for _, item := range diag.Diagnostics() {
		if item.Code == diagnostics.ErrCircularDependency {
			t.Fatal("constant evaluation cycle emitted before CFG phase")
		}
	}
	entry.AST = nil
	if !advanceModulePhase(ctx, entry, diag) {
		t.Fatal("CFG phase requires AST")
	}
	for _, item := range diag.Diagnostics() {
		if item.Code == diagnostics.ErrCircularDependency {
			return
		}
	}
	t.Fatalf("missing constant evaluation cycle: %s", diag.EmitAllToString())
}

func TestPhaseReadinessRequiresSyntaxThroughTypingAndTHIRAfterward(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	entry := parseModuleSource("entry"+peeper.SourceExt, `fn main() -> i32 { return 0; }`, diag)
	entry.Phase = phase.Parsed
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	ctx.AddModule(entry)

	syntax := entry.AST
	entry.AST = nil
	if IsModuleReadyForNextPhase(ctx, entry, nil, true) || advanceModulePhase(ctx, entry, diag) {
		t.Fatal("collection advanced without syntax")
	}
	entry.AST = syntax
	for entry.Phase < phase.Typechecked {
		if !advanceModulePhase(ctx, entry, diag) {
			t.Fatalf("advanceModulePhase stopped at %v", entry.Phase)
		}
	}
	typed := entry.THIR
	entry.AST = nil
	entry.ResetToPhase(phase.Typechecked)
	if entry.THIR != typed || !IsModuleReadyForNextPhase(ctx, entry, nil, true) {
		t.Fatal("typechecked reset did not preserve THIR readiness without AST")
	}
	for entry.Phase < phase.Analyzed {
		if !advanceModulePhase(ctx, entry, diag) {
			t.Fatalf("post-typing phase stopped without AST at %v", entry.Phase)
		}
	}
	if diag.HasErrors() {
		t.Fatalf("post-typing diagnostics without AST: %s", diag.EmitAllToString())
	}
	entry.ResetToPhase(phase.Resolved)
	if entry.THIR != nil || IsModuleReadyForNextPhase(ctx, entry, nil, true) || advanceModulePhase(ctx, entry, diag) {
		t.Fatal("typechecking advanced without AST after semantic reset")
	}
}

func TestPipelineAnalyzedInitializationIgnoresTerminatingPredecessor(t *testing.T) {
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, "", `fn choose(cond: bool) -> i32 {
	let mut value: i32;
	if cond {
		value = 7;
	} else {
		return 3;
	}
	return value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineLowersCaseRefinedFieldWithConflictingSchemaTypes(t *testing.T) {
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, "", `enum Choice {
	Number: { value: i32 },
	Flag: { value: bool }
}

fn Read(choice: Choice) -> bool {
	if choice is Choice::Flag {
		return choice.value;
	}
	return false;
}

fn main() -> i32 {
	if Read(Choice::Flag with .{ value = true }) {
		return 0;
	}
	return 1;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestRequireScheduledModulesAtLeastReportsStoppedPhase(t *testing.T) {
	tests := []struct {
		name   string
		module *module.Module
		want   string
	}{
		{name: "blocked prerequisite", module: &module.Module{ID: moduleid.ID{ImportPath: "local:main"}, Phase: phase.Resolved}, want: "resolved phase"},
		{name: "missing MIR", module: &module.Module{ID: moduleid.ID{ImportPath: "local:main"}, Phase: phase.Analyzed}, want: "analyzed phase"},
		{name: "missing backend", module: &module.Module{ID: moduleid.ID{ImportPath: "local:main"}, Phase: phase.MIR}, want: "MIR phase"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireScheduledModulesAtLeast([]*module.Module{test.module}, map[moduleid.ID]string{test.module.ID: ""}, phase.Backend)
			if err == nil || !strings.Contains(err.Error(), "local:main") || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("terminal error = %v, want module and %q", err, test.want)
			}
		})
	}
	if err := requireScheduledModulesAtLeast([]*module.Module{{ID: moduleid.ID{ImportPath: "local:main"}, Phase: phase.Backend}}, map[moduleid.ID]string{moduleid.ID{ImportPath: "local:main"}: ""}, phase.Backend); err != nil {
		t.Fatalf("completed module rejected: %v", err)
	}
	if err := requireScheduledModulesAtLeast([]*module.Module{{ID: moduleid.ID{ImportPath: "sourceOverride:stub"}, Phase: phase.None}}, map[moduleid.ID]string{moduleid.ID{ImportPath: "local:main"}: ""}, phase.Backend); err != nil {
		t.Fatalf("unscheduled source override rejected: %v", err)
	}
}

func TestPipelineDiagnosticStopReturnsNormally(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	entry := parseModuleSource("invalid"+peeper.SourceExt, "fn main() -> Missing { return 0; }", diag)
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	if err := Run(ctx, entry); err != nil {
		t.Fatalf("diagnostic-driven stop returned pipeline error: %v", err)
	}
	if !diag.HasErrors() {
		t.Fatal("invalid program produced no diagnostic")
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

	if err := Run(ctx, entry); err != nil {
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

	imported := parseModuleSource("util"+peeper.SourceExt, "fn Helper() -> i32 { return 1; }", diag)
	imported.Phase = phase.Parsed
	ctx.AddModule(imported)

	entry := parseModuleSource("main"+peeper.SourceExt, "import \"util\";\nfn main() -> i32 { return util::Helper(); }\n", diag)
	entry.Phase = phase.Parsed
	entry.Imports = map[string]module.ResolvedImport{
		"util": {
			ID:       imported.ID,
			FilePath: imported.FilePath,
		},
	}
	ctx.AddModule(entry)

	if !IsModuleReadyForNextPhase(ctx, entry, nil, true) {
		t.Fatalf("parsed importer should be ready for collector when import is parsed")
	}

	entry.Phase = phase.Collected
	if IsModuleReadyForNextPhase(ctx, entry, nil, true) {
		t.Fatalf("collected importer should wait for bound import before binder")
	}

	imported.Phase = phase.Bound
	if !IsModuleReadyForNextPhase(ctx, entry, nil, true) {
		t.Fatalf("collected importer should be ready for binder when import is bound")
	}

	entry.Phase = phase.Bound
	imported.Phase = phase.Parsed
	if IsModuleReadyForNextPhase(ctx, entry, nil, true) {
		t.Fatalf("bound importer should wait for collected import before resolver")
	}

	imported.Phase = phase.Collected
	if !IsModuleReadyForNextPhase(ctx, entry, nil, true) {
		t.Fatalf("bound importer should be ready for resolver when import is collected")
	}

	entry.Phase = phase.Resolved
	if IsModuleReadyForNextPhase(ctx, entry, nil, true) {
		t.Fatal("resolved importer should wait for typechecked import before typechecking")
	}

	imported.Phase = phase.Typechecked
	if !IsModuleReadyForNextPhase(ctx, entry, nil, true) {
		t.Fatal("resolved importer should be ready for typechecking when import constants are published")
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

	entry := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "app/main"},
		FilePath: mainPath,
	}

	if err := os.WriteFile(utilPath, []byte(utilSrc), 0o644); err != nil {
		t.Fatalf("write util: %v", err)
	}
	if err := os.WriteFile(mainPath, []byte(mainSrc), 0o644); err != nil {
		t.Fatalf("write main: %v", err)
	}

	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}

	util, ok := ctx.ModuleByFile(utilPath)
	if !ok || util == nil {
		t.Fatalf("expected imported module to be loaded")
	}
	if util.Phase != phase.Backend {
		t.Fatalf("imported module phase = %v, want %v", util.Phase, phase.Backend)
	}
	if entry.Phase != phase.Backend {
		t.Fatalf("entry phase = %v, want %v", entry.Phase, phase.Backend)
	}
}

func TestPipelineParallelModulesShareTypeTable(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, peeper.SourceDirName)
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}

	sources := map[string]string{
		peeper.MainFileName: `import "app/left";
import "app/right";

fn main() -> i32 {
	return left::Value() + right::Value();
}`,
		"left" + peeper.SourceExt: `struct Box { value: i32 }

fn Value() -> i32 {
	let values = []Box{.{value = 19}};
	return values[0].value;
}`,
		"right" + peeper.SourceExt: `struct Box { value: i32 }

fn Value() -> i32 {
	let values = []Box{.{value = 23}};
	return values[0].value;
}`,
	}
	diag := diagnostics.NewDiagnosticBag()
	for name, sourceText := range sources {
		path := filepath.Join(srcDir, name)
		diag.AddSourceContent(path, sourceText)
		if err := os.WriteFile(path, []byte(sourceText), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	mainPath := filepath.Join(srcDir, peeper.MainFileName)
	ctx := project.NewWithConfig(project.Config{RootDir: root, ProjectName: "app", Extension: peeper.SourceExt}, diag)
	entry := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "app/main"},
		FilePath: mainPath,
	}
	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	for _, name := range []string{"left", "right"} {
		module, ok := ctx.ModuleByFile(filepath.Join(srcDir, name+peeper.SourceExt))
		if !ok || module.Phase != phase.Backend {
			t.Fatalf("%s module = %#v, want backend phase", name, module)
		}
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
	let msg: cstr = c"ping\n";
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
	return alloc(File.{});
}

	fn (self: *File) read(buf: cstr) -> i32 {
		return 0;
	}

fn main() -> i32 {
	let file = open_file();
	return file.read(c"ok");
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

func TestPipelineLowersReferenceRecursiveEnumOnSupportedWidths(t *testing.T) {
	const src = `enum Node {
	Next: { next: &Node },
	End
}

fn Read(_: &Node) {}

fn main() -> i32 {
	let end = Node::End;
	let node = Node::Next with .{ next = &end };
	let mut result = 1;
	match node {
		Node::Next with { next = next } => {
			Read(next);
			result = 0;
		}
		Node::End => {}
	}
	return result;
}`

	for _, arch := range []string{"386", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			filePath := "recursive_enum_reference_" + arch + peeper.SourceExt
			diag := diagnostics.NewDiagnosticBag()
			diag.AddSourceContent(filePath, src)
			ctx := project.NewWithConfig(project.Config{
				RootDir: ".", Extension: peeper.SourceExt, TargetOS: "linux", TargetArch: arch,
			}, diag)
			entry := parseModuleSource(filePath, src, diag)
			if err := Run(ctx, entry); err != nil {
				t.Fatalf("pipeline.Run returned error: %v", err)
			}
			if diag.HasErrors() || entry.Phase != phase.Backend || entry.LLVMIR == "" {
				t.Fatalf("reference-recursive enum stopped before backend: phase=%v\n%s", entry.Phase, diag.EmitAllToString())
			}

			assertLLVMCompiles(t, ctx.Target, entry.LLVMIR)
		})
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

	if err := Run(ctx, entry); err != nil {
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
	return alloc(Counter.{ value = 0 });
}

	fn (mut self: *Counter) bump() -> i32 {
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

func TestPipelineLowersConcreteGenericStructInstance(t *testing.T) {
	entrySrc := `struct Box<T> { value: T }

fn Read(box: &Box<i32>) -> i32 { return box.value; }
fn main() -> i32 {
	let box: Box<i32> = .{ value = 42 };
	return Read(&box);
}`
	const entryPath = "entry" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	entry := parseModuleSource(entryPath, entrySrc, diag)

	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected generic pipeline diagnostics:\n%s", diag.EmitAllToString())
	}
	if entry.MIR == nil || entry.LLVMIR == "" {
		t.Fatal("generic named type did not reach MIR and LLVM")
	}
}

func TestPipelineResolvesImportedGenericApplication(t *testing.T) {
	diag := runImportedRuntimeSymbolPipeline(t, `import "app/runtime";

fn Read(box: &runtime::Box<i32>) -> i32 { return box.value; }
fn main() -> i32 {
	let box: runtime::Box<i32> = .{ value = 9 };
	return Read(&box);
}`, `struct Box<T> { value: T }`)
	if diag.HasErrors() {
		t.Fatalf("unexpected imported generic diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineExpandsImportedNamedEnumDefault(t *testing.T) {
	diag := runImportedRuntimeSymbolPipeline(t, `import "app/runtime";

fn main() -> i32 {
	if !(runtime::IsPending()) {
		return 1;
	}
	return runtime::Read();
}`, `enum Status<T> {
	Ready: { value: T },
	Pending,
}

type State<T> = Status<T>;

fn Read(status: State<i32> = State<i32>::Ready with .{ value = 42 }) -> i32 {
	match status {
		State<i32>::Ready with { value = value } => { return value; }
		State<i32>::Pending => { return 0; }
	}
}

fn IsPending(pending: bool = State<i32>::Pending is State<i32>::Pending) -> bool {
	return pending;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected imported enum default diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineRejectsTypeArgumentsOnImportedValuePath(t *testing.T) {
	diag := runImportedRuntimeSymbolPipeline(t, `import "app/runtime";

fn main() -> i32 {
	return runtime::Make<i32>();
}`, `fn Make() -> i32 { return 42; }`)
	out := diag.EmitAllToString()
	if !diag.HasErrors() || !strings.Contains(out, diagnostics.ErrInvalidType) ||
		!strings.Contains(out, "type arguments are not allowed on value paths") {
		t.Fatalf("expected rejected imported value type arguments, got:\n%s", out)
	}
}

func TestPipelineLowersGenericInterfaceInstance(t *testing.T) {
	entrySrc := `iface Reader<T> { fn (&Self) read() -> T }
struct Counter { value: i32 }

fn (self: &Counter) read() -> i32 { return self.value; }
fn Read(reader: &Reader<i32>) -> i32 { return reader.read(); }
fn main() -> i32 {
	let counter: Counter = .{ value = 17 };
	return Read(&counter);
}`
	const entryPath = "entry" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	entry := parseModuleSource(entryPath, entrySrc, diag)

	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected generic interface diagnostics:\n%s", diag.EmitAllToString())
	}
	if entry.MIR == nil || entry.LLVMIR == "" {
		t.Fatal("generic interface instance did not reach MIR and LLVM")
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

func TestPipelineDropsTemporaryDynamicArrayAfterFoldedProjectionLoad(t *testing.T) {
	entrySrc := `fn make_values() -> []i32 {
	return []i32{41};
}

fn main() -> i32 {
	return make_values()[0];
}`
	const entryPath = "entry" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := project.NewWithConfig(project.Config{RootDir: ".", Extension: peeper.SourceExt}, diag)
	entry := parseModuleSource(entryPath, entrySrc, diag)

	if err := Run(ctx, entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}

	found := false
	for _, fn := range entry.MIR.Funcs {
		if fn.Name != "main" {
			continue
		}
		for _, block := range fn.Blocks {
			for index := 0; index+1 < len(block.Instrs); index++ {
				assign, ok := block.Instrs[index].(*mir.Assign)
				if !ok {
					continue
				}
				load, ok := assign.Value.(*mir.Load)
				if !ok {
					continue
				}
				drop, ok := block.Instrs[index+1].(*mir.Drop)
				if ok && drop.Value.Text() == load.Place.Root.Text() {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("temporary dynamic-array projection has no immediate root drop, MIR:\n%s", entry.MIR.Text())
	}
}

func TestPipelineLowersSliceViewIndexReadAndWrite(t *testing.T) {
	preludeSrc := ``
	entrySrc := `fn read_at(xs: &[..]i32, index: usize) -> i32 {
	return xs[index];
}

fn write_at(xs: &mut [..]i32, index: usize, value: i32) {
	xs[index] = value;
}`

	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineRejectsInvalidAllocAritiesWithoutPanic(t *testing.T) {
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
			diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, "", tt.src)
			if out := diag.EmitAllToString(); !strings.Contains(out, tt.want) {
				t.Fatalf("unexpected alloc arity diagnostic:\n%s", out)
			}
		})
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

func TestPipelineRejectsWideConstantIndexesAcrossTargets(t *testing.T) {
	for _, arch := range []string{"386", "amd64"} {
		for _, test := range []struct{ name, declaration, index, value string }{
			{"float cast", "", "4294967296f64 as u64", "4294967296"},
			{"negative float cast", "", "(-4294967296f64) as i64", "-4294967296"},
			{"local constant", "const Offset: i64 = 4294967296i64;", "Offset", "4294967296"},
			{"negative local constant", "const Offset: i64 = -4294967296i64;", "Offset", "-4294967296"},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				var entry *module.Module
				diag := buildPipelineTestWithConfig(t, project.Config{
					RootDir: ".", Extension: peeper.SourceExt, TargetOS: "linux", TargetArch: arch,
				}, "", "fn main() -> i32 { "+test.declaration+" let values = [1]i32{7}; return values["+test.index+"]; }", func(mod *module.Module) {
					entry = mod
				})
				items := diag.Diagnostics()
				if len(items) != 1 || items[0].Code != diagnostics.ErrArrayOutOfBounds ||
					items[0].Message != "array index out of bounds: index "+test.value+" for length 1" ||
					len(items[0].Labels) != 1 || items[0].Labels[0].Location == nil {
					t.Fatalf("expected located bounds diagnostic, got:\n%s", diag.EmitAllToString())
				}
				if entry.MIR != nil || entry.LLVMIR != "" {
					t.Fatal("invalid constant index reached backend")
				}
			})
		}
	}
}

func TestPipelineRejectsHostSizedConstantBounds(t *testing.T) {
	for _, index := range []struct{ source, value string }{
		{"-1i64", "-1"},
		{"9223372036854775808u64", "9223372036854775808"},
		{"18446744073709551616u128", "18446744073709551616"},
	} {
		t.Run(index.source, func(t *testing.T) {
			var entry *module.Module
			diag := buildPipelineTestWithConfig(t, project.Config{
				RootDir: ".", Extension: peeper.SourceExt, TargetOS: "linux", TargetArch: "amd64",
			}, "", "fn read(xs: [9223372036854775808u64]u8) -> u8 { return xs["+index.source+"]; }", func(mod *module.Module) {
				entry = mod
			})
			fn := entry.AST.Stmts[0].(*ast.FnDecl)
			access := fn.Body.Stmts[0].(*ast.ReturnStmt).Value.(*ast.IndexExpr)
			items := diag.Diagnostics()
			if len(items) != 1 || items[0].Code != diagnostics.ErrArrayOutOfBounds ||
				items[0].Message != "array index out of bounds: index "+index.value+" for length 9223372036854775808" ||
				len(items[0].Labels) != 1 || items[0].Labels[0].Location != ast.LocOf(access.Index) {
				t.Fatalf("expected source-located exact bounds diagnostic:\n%s", diag.EmitAllToString())
			}
			if entry.MIR != nil || entry.LLVMIR != "" {
				t.Fatal("invalid large-array index reached backend")
			}
		})
	}
}

func TestPipelineLowersLargeArrayBoundsAcrossTargets(t *testing.T) {
	for _, compilerTarget := range []struct{ arch, length string }{
		{"386", "2147483648"}, {"amd64", "9223372036854775808"}, {"amd64", "9223372036854775809"},
	} {
		for _, index := range []string{"0u64", "index"} {
			t.Run(compilerTarget.arch+"/"+compilerTarget.length+"/"+index, func(t *testing.T) {
				params := "xs: &Large"
				if index == "index" {
					params += ", index: i64"
				}
				src := "struct Large { values: [" + compilerTarget.length + "u64]u8, flag: bool }\n" +
					"fn read(" + params + ") -> u8 { return xs.values[" + index + "]; }"
				var entry *module.Module
				diag := buildPipelineTestWithConfig(t, project.Config{
					RootDir: ".", Extension: peeper.SourceExt, TargetOS: "linux", TargetArch: compilerTarget.arch,
				}, "", src, func(mod *module.Module) { entry = mod })
				if diag.HasErrors() || entry.MIR == nil || entry.LLVMIR == "" {
					t.Fatalf("large array access failed:\n%s", diag.EmitAllToString())
				}
				if index == "index" && (!strings.Contains(entry.LLVMIR, "icmp uge i64") || !strings.Contains(entry.LLVMIR, "icmp slt i64") ||
					!strings.Contains(entry.LLVMIR, compilerTarget.length) || !strings.Contains(entry.LLVMIR, "call void @llvm.trap()")) {
					t.Fatalf("large array runtime guard missing:\n%s", entry.LLVMIR)
				}
				targetInfo, err := target.New("linux", compilerTarget.arch)
				if err != nil {
					t.Fatal(err)
				}
				assertLLVMCompiles(t, targetInfo, entry.LLVMIR)
			})
		}
	}
}

func TestPipelineLowersCheckedConstantIndexesAcrossTargets(t *testing.T) {
	for _, arch := range []string{"386", "amd64"} {
		for _, test := range []struct{ name, declaration, index, typ, value, conversion, length string }{
			{"float cast", "", "1.75f64 as u64", "u64", "1", "", ""},
			{"source width negation", "const Offset: u8 = 255u8;", "-Offset", "u8", "1", "zext i8 1 to i64", ""},
			{"explicit narrowing", "", "257u16 as u8", "u8", "1", "zext i8 1 to i64", ""},
			{"single bit literal", "", "1u1", "u1", "1", "zext i1 1 to i64", ""},
			{"single bit zero", "", "0u1", "u1", "0", "zext i1 0 to i64", ""},
			{"single bit float cast", "", "1f64 as u1", "u1", "1", "zext i1 1 to i64", ""},
			{"single bit binding", "const Offset: u1 = 1u1;", "Offset", "u1", "1", "zext i1 1 to i64", ""},
			{"signed narrow", "", "1i8", "i8", "1", "sext i8 1 to i64", ""},
			{"wide unsigned", "", "1u128", "u128", "1", "trunc i128 1 to i64", ""},
			{"byte", "", "1u8 as byte", "byte", "1", "zext i8 1 to i64", ""},
			{"u8 top bit", "", "128u8", "u8", "128", "zext i8 128 to i64", "129"},
			{"u24 top bit", "", "8388608u24", "u24", "8388608", "zext i24 8388608 to i64", "8388609"},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				src := "fn main() -> i32 { " + test.declaration + " let values = [2]i32{7, 9}; return values[" + test.index + "]; }"
				if test.length != "" {
					// High-bit indexes use borrowed storage; no large object is allocated.
					src = "struct Values { items: [" + test.length + "]i32 }\n" +
						"fn read(values: &Values) -> i32 { " + test.declaration + " return values.items[" + test.index + "]; }"
				}
				diag := buildPipelineTestWithConfig(t, project.Config{
					RootDir: ".", Extension: peeper.SourceExt, TargetOS: "linux", TargetArch: arch,
				}, "", src, func(entry *module.Module) {
					assertCheckedConstantIndex(t, entry, test.value, test.typ)
					if test.conversion != "" && !strings.Contains(entry.LLVMIR, test.conversion) {
						t.Fatalf("checked index value not preserved for GEP; missing %q:\n%s", test.conversion, entry.LLVMIR)
					}
					targetInfo, err := target.New("linux", arch)
					if err != nil {
						t.Fatal(err)
					}
					assertLLVMCompiles(t, targetInfo, entry.LLVMIR)
				})
				if diag.HasErrors() {
					t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
				}
			})
		}
	}
}

func TestPipelineEvaluatesQualifiedConstantIndexesAcrossTargets(t *testing.T) {
	for _, arch := range []string{"386", "amd64"} {
		for _, test := range []struct {
			name, prefix, declaration, index, value, typ string
			rejected                                     bool
		}{
			{"direct wide", "", "", "values::Offset", "18446744073709551616", "", true},
			{"local wide", "", "const Saved: u128 = values::Offset;", "Saved", "18446744073709551616", "", true},
			{"module wide", "const Saved: u128 = values::Offset;", "", "Saved", "18446744073709551616", "", true},
			{"arithmetic wide", "", "", "values::Offset + 0u128", "18446744073709551616", "", true},
			{"cast wide", "", "", "values::Offset as i128", "18446744073709551616", "", true},
			{"transitive wide", "import \"app/bridge\";", "", "bridge::Forward", "18446744073709551616", "", true},
			{"negative", "", "", "values::Negative", "-4294967296", "", true},
			{"equal length", "", "", "values::End", "2", "", true},
			{"zero", "", "", "values::Zero", "0", "u128", false},
			{"unsigned single bit", "", "", "values::Bit", "1", "u1", false},
			{"type alias", "", "", "values::Selected", "1", "u64", false},
			{"source width negation", "", "", "-values::Wrapped", "1", "u8", false},
			{"explicit narrowing", "", "", "values::Wide as u8", "1", "u8", false},
			{"module alias", "const Saved: u128 = values::Zero;", "", "Saved", "0", "u128", false},
			{"transitive valid", "import \"app/bridge\";", "", "bridge::Selected", "1", "u1", false},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				ctx, entry := importedPipelineContext(t, arch, map[string]string{
					peeper.MainFileName: "import \"app/values\";\n" + test.prefix + "\nfn main() -> i32 { " + test.declaration + " let items = [2]i32{7, 9}; return items[" + test.index + "]; }",
					"values" + peeper.SourceExt: `type Index = u64;
const Offset: u128 = 1u128 << 64u128;
const Negative: i64 = -4294967296i64;
const End: u8 = 2u8;
const Zero: u128 = 0u128;
const Bit: u1 = 1u1;
const Selected: Index = 1u64;
const Wrapped: u8 = 255u8;
const Wide: u16 = 257u16;`,
					"bridge" + peeper.SourceExt: `import "app/values";
const Forward: u128 = values::Offset;
const Selected: u1 = values::Bit;`,
				})
				if err := Run(ctx, entry); err != nil {
					t.Fatal(err)
				}
				diag := ctx.Diagnostics
				if test.rejected {
					fn := entry.AST.Stmts[len(entry.AST.Stmts)-1].(*ast.FnDecl)
					access := fn.Body.Stmts[len(fn.Body.Stmts)-1].(*ast.ReturnStmt).Value.(*ast.IndexExpr)
					if diag.ErrorCount() != 1 {
						t.Fatalf("want one source bounds error, got:\n%s", diag.EmitAllToString())
					}
					for _, item := range diag.Diagnostics() {
						if item.Severity != diagnostics.Error {
							continue
						}
						if item.Code != diagnostics.ErrArrayOutOfBounds || item.Message != "array index out of bounds: index "+test.value+" for length 2" ||
							len(item.Labels) != 1 || item.Labels[0].Location != ast.LocOf(access.Index) {
							t.Fatalf("want exact index-located bounds error, got:\n%s", diag.EmitAllToString())
						}
					}
					if entry.MIR != nil || entry.LLVMIR != "" {
						t.Fatal("invalid qualified constant reached backend")
					}
					return
				}
				if diag.HasErrors() {
					t.Fatalf("valid qualified constant failed:\n%s", diag.EmitAllToString())
				}
				assertCheckedConstantIndex(t, entry, test.value, test.typ)
				assertLLVMCompiles(t, ctx.Target, entry.LLVMIR)
			})
		}
	}
}

func TestPipelineChecksQualifiedConstantShiftCounts(t *testing.T) {
	for _, arch := range []string{"386", "amd64"} {
		for _, test := range []struct {
			name, left, count string
			rejected          bool
		}{
			{"out of width", "1u8", "values::Invalid", true},
			{"negative", "1i8", "values::Negative", true},
			{"direct valid", "1u8", "values::Valid", false},
			{"explicit narrowing", "1u8", "values::Wide as u8", false},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				ctx, entry := importedPipelineContext(t, arch, map[string]string{
					peeper.MainFileName: "import \"app/values\"; fn Read() -> i32 { return (" + test.left + " << (" + test.count + ")) as i32; }",
					"values" + peeper.SourceExt: `const Invalid: u8 = 8u8;
const Negative: i8 = -1i8;
const Valid: u8 = 2u8;
const Wide: u16 = 258u16;`,
				})
				if err := Run(ctx, entry); err != nil {
					t.Fatal(err)
				}
				diag := ctx.Diagnostics
				if test.rejected {
					cast := entry.AST.Stmts[0].(*ast.FnDecl).Body.Stmts[0].(*ast.ReturnStmt).Value.(*ast.AsExpr)
					shift := cast.Expr.(*ast.BinaryExpr)
					items := diag.Diagnostics()
					if len(items) != 1 || items[0].Code != diagnostics.ErrInvalidOperation || items[0].Message != "shift count must be between 0 and 7" ||
						len(items[0].Labels) != 1 || items[0].Labels[0].Location != ast.LocOf(shift.Right) || entry.MIR != nil || entry.LLVMIR != "" {
						t.Fatalf("want located constant-shift error before MIR, got:\n%s", diag.EmitAllToString())
					}
					return
				}
				if diag.HasErrors() || entry.MIR == nil || entry.LLVMIR == "" {
					t.Fatalf("valid qualified shift failed:\n%s", diag.EmitAllToString())
				}
				assertLLVMCompiles(t, ctx.Target, entry.LLVMIR)
			})
		}
	}
}

func TestPipelineRecordsQualifiedRangeEntryAcrossTargets(t *testing.T) {
	for _, arch := range []string{"386", "amd64"} {
		for _, test := range []struct {
			name, params, start, end string
			guaranteed               bool
		}{
			{"ascending", "", "values::Zero", "values::One", true},
			{"equal", "", "values::One", "values::One", false},
			{"reversed", "", "values::One", "values::Zero", false},
			{"runtime", "start: i64", "start", "values::One", false},
		} {
			t.Run(arch+"/"+test.name, func(t *testing.T) {
				ctx, entry := importedPipelineContext(t, arch, map[string]string{
					peeper.MainFileName:         "import \"app/values\"; fn Read(" + test.params + ") -> i64 { for value in " + test.start + "..(" + test.end + " as i64) { return value; } }",
					"values" + peeper.SourceExt: `const Zero: i64 = 0i64; const One: i64 = 1i64;`,
				})
				if err := Run(ctx, entry); err != nil {
					t.Fatal(err)
				}
				loop := entry.AST.Stmts[0].(*ast.FnDecl).Body.Stmts[0].(*ast.ForStmt)
				plan, ok := entry.THIR.Node(loop.ID()).(*thir.For).Iteration.(*thir.RangeIteration)
				if !ok {
					t.Fatalf("missing range plan for %T:\n%s", loop.Iterable, ctx.Diagnostics.EmitAllToString())
				}
				if plan.HasGuaranteedEntry != test.guaranteed {
					t.Fatalf("range entry = %v, want %v", plan.HasGuaranteedEntry, test.guaranteed)
				}
				for name, typ := range map[string]typeinfo.Type{"element": plan.ElementType, "cursor": plan.Cursor.Type, "limit": plan.Limit.Type} {
					if typeinfo.TypeText(typ) != "i64" {
						t.Fatalf("%s type = %s, want i64", name, typeinfo.TypeText(typ))
					}
				}
				diag := ctx.Diagnostics
				if !test.guaranteed {
					if diag.ErrorCount() != 1 || !hasDiagnosticCode(diag, diagnostics.ErrMissingReturn) || entry.MIR != nil || entry.LLVMIR != "" {
						t.Fatalf("non-guaranteed range must retain missing-return error:\n%s", diag.EmitAllToString())
					}
					return
				}
				if diag.HasErrors() || entry.MIR == nil || entry.LLVMIR == "" {
					t.Fatalf("guaranteed imported range failed:\n%s", diag.EmitAllToString())
				}
				assertLLVMCompiles(t, ctx.Target, entry.LLVMIR)
			})
		}
	}
}

func TestPipelineReportsQualifiedConstantConditionsInCFGPhase(t *testing.T) {
	for _, test := range []struct {
		condition, warning string
		value              bool
	}{
		{"values::Flag", diagnostics.WarnConstantConditionTrue, true},
		{"values::One == 1i64", diagnostics.WarnConstantConditionTrue, true},
		{"values::One == 0i64", diagnostics.WarnConstantConditionFalse, false},
	} {
		t.Run(test.condition, func(t *testing.T) {
			ctx, entry := importedPipelineContext(t, "amd64", map[string]string{
				peeper.MainFileName:         "import \"app/values\"; fn main() -> i32 { if " + test.condition + " { return 1; } return 0; }",
				"values" + peeper.SourceExt: `const Flag: bool = true; const One: i64 = 1i64;`,
			})
			loader := &moduleLoader{ctx: ctx, scheduled: make(map[moduleid.ID]string)}
			if err := loader.Load(entry); err != nil {
				t.Fatal(err)
			}
			owner, found := ctx.ModuleByID(entry.Imports["values"].ID)
			if !found {
				t.Fatal("qualified owner was not loaded")
			}
			diag := ctx.Diagnostics
			for _, mod := range []*module.Module{owner, entry} {
				for mod.Phase < phase.Typechecked {
					if !advanceModulePhase(ctx, mod, diag) {
						t.Fatalf("typing stopped at %v:\n%s", mod.Phase, diag.EmitAllToString())
					}
				}
			}
			condition := entry.AST.Stmts[0].(*ast.FnDecl).Body.Stmts[0].(*ast.IfStmt)
			checked := entry.THIR.Node(condition.ID()).(*thir.If)
			if checked.ConstantCondition == nil || *checked.ConstantCondition != test.value {
				t.Fatalf("qualified condition = %v, want %v", checked.ConstantCondition, test.value)
			}
			if diag.HasErrors() || hasDiagnosticCode(diag, diagnostics.WarnConstantConditionTrue) || hasDiagnosticCode(diag, diagnostics.WarnConstantConditionFalse) {
				t.Fatalf("qualified warning/error emitted before CFG:\n%s", diag.EmitAllToString())
			}
			entry.AST = nil
			if !advanceModulePhase(ctx, entry, diag) || entry.Phase != phase.CFG || entry.MIR != nil || diag.HasErrors() {
				t.Fatalf("CFG did not consume qualified evidence without AST:\n%s", diag.EmitAllToString())
			}
			warnings := 0
			for _, item := range diag.Diagnostics() {
				if item.Code == diagnostics.WarnConstantConditionTrue || item.Code == diagnostics.WarnConstantConditionFalse {
					if item.Code != test.warning {
						t.Fatalf("wrong qualified warning %s", item.Code)
					}
					warnings++
				}
			}
			if warnings != 1 {
				t.Fatalf("qualified warnings = %d, want one:\n%s", warnings, diag.EmitAllToString())
			}
		})
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
	return alloc(Point.{ x = 0, y = 0 });
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

func TestPipelineRejectsIntrinsicInterfaceConformance(t *testing.T) {
	entrySrc := `iface Lenner {
	fn (&Self) len() -> usize
}

fn main() -> i32 {
	let text: str = "abc";
	let value: &Lenner = &text;
	return value |> len() as i32;
}`
	diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, "", entrySrc)
	if !diag.HasErrors() {
		t.Fatal("expected intrinsic interface conformance rejection")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "missing methods: len") {
		t.Fatalf("expected interface conformance diagnostic, got:\n%s", out)
	}
	for _, unexpected := range []string{"missing interface method implementation", "unsupported llvm type"} {
		if strings.Contains(out, unexpected) {
			t.Fatalf("intrinsic interface conformance reached lowering: %s", out)
		}
	}
}

func TestPipelineValidatesFixedArrayLengthAgainstTargetIndex(t *testing.T) {
	const max32 = `type MaxArray = [4294967295u64]u8;`
	const overflow32 = `type TooLarge = [4294967296u64]u8;`
	config32 := project.Config{RootDir: ".", Extension: peeper.SourceExt, TargetOS: "linux", TargetArch: "386"}
	if diag := buildPipelineTestWithConfig(t, config32, "", max32); diag.HasErrors() {
		t.Fatalf("32-bit maximum array length rejected:\n%s", diag.EmitAllToString())
	}
	for name, src := range map[string]string{
		"implicit target overflow": strings.ReplaceAll(overflow32, "u64", ""),
		"explicit wider literal":   overflow32,
	} {
		t.Run(name, func(t *testing.T) {
			diag := buildPipelineTestWithConfig(t, config32, "", src)
			if !diag.HasErrors() || !strings.Contains(diag.EmitAllToString(), "target usize (u32)") {
				t.Fatalf("expected target-specific array length diagnostic, got:\n%s", diag.EmitAllToString())
			}
		})
	}
	config64 := project.Config{RootDir: ".", Extension: peeper.SourceExt, TargetOS: "linux", TargetArch: "amd64"}
	if diag := buildPipelineTestWithConfig(t, config64, "", overflow32); diag.HasErrors() {
		t.Fatalf("64-bit array length rejected:\n%s", diag.EmitAllToString())
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

func TestPipelineLowersEveryRegisteredIntrinsic(t *testing.T) {
	const src = `fn Identity(value: i32) -> i32 {
	return value;
}

fn WriteAt(values: &mut [..]i32, index: usize, value: i32) {
	values[index] = value;
}

fn KeepRef(_: &mut i32) {
}

fn main() -> i32 {
	let text: str = "abc";
	let bytes = text |> as_bytes();
	let copied = bytes |> from_bytes();
	let chars = text |> as_chars();
	let text_length = text |> len();
	let byte_length = bytes |> len();
	let mut values = []i32{1};
	values |> append(2);
	values |> reserve(8);
	values |> resize(4, 0);
	values |> shrink(2);
	WriteAt(values[..], 0, Identity(9));
	let view = values[0..1];
	let first = view[0];
	let mut scalar = first;
	KeepRef(&mut scalar);
	let owned = alloc(7);
	if first != 9 {
		return 1;
	}
	return text_length as i32 + byte_length as i32 + (copied |> len()) as i32 + (chars |> len()) as i32 + (values |> len()) as i32 + scalar;
}`

	targets := []struct {
		name string
		arch string
	}{
		{name: "32-bit", arch: "386"},
		{name: "64-bit", arch: "amd64"},
	}
	for _, compilerTarget := range targets {
		t.Run(compilerTarget.name, func(t *testing.T) {
			filePath := "intrinsic_completeness_" + compilerTarget.arch + peeper.SourceExt
			diag := diagnostics.NewDiagnosticBag()
			diag.AddSourceContent(filePath, src)
			ctx := project.NewWithConfig(project.Config{
				RootDir:    ".",
				Extension:  peeper.SourceExt,
				TargetOS:   "linux",
				TargetArch: compilerTarget.arch,
			}, diag)
			entry := parseModuleSource(filePath, src, diag)
			if err := Run(ctx, entry); err != nil {
				t.Fatalf("pipeline.Run returned error: %v", err)
			}
			if diag.HasErrors() {
				t.Fatalf("registered intrinsic program failed:\n%s", diag.EmitAllToString())
			}

			observed := make(map[symbols.CompilerOp]struct{})
			for _, stmt := range entry.AST.Stmts {
				ast.Inspect(stmt, func(node ast.Node) bool {
					if node != nil {
						symbol := entry.SymbolIndex.Symbol(node)
						if symbol != nil && symbol.CompilerOp != "" {
							observed[symbol.CompilerOp] = struct{}{}
						}
					}
					return true
				})
			}
			for _, op := range intrinsics.Operations() {
				if _, ok := observed[op]; !ok {
					t.Errorf("registered intrinsic %q lacks successful semantic/MIR/LLVM exercise", op)
				}
				delete(observed, op)
			}
			for op := range observed {
				t.Errorf("lowered intrinsic %q is absent from compiler registry", op)
			}
			if entry.Phase != phase.Backend || entry.MIR == nil || entry.LLVMIR == "" {
				t.Fatalf("intrinsic program stopped before backend: phase=%v MIR=%v LLVM=%v", entry.Phase, entry.MIR != nil, entry.LLVMIR != "")
			}
			mirText := entry.MIR.Text()
			for _, marker := range []string{"call ", "store ", " = addr ", " = load ", " = view ", "cast ", " = alloc ", "drop ", " != ", "ret "} {
				if !strings.Contains(mirText, marker) {
					t.Errorf("representative MIR lacks %q:\n%s", marker, mirText)
				}
			}

			assertLLVMCompiles(t, ctx.Target, entry.LLVMIR)
		})
	}
}

func TestPipelineAcceptsOptionalNarrowingAcrossCFGAndStablePlaces(t *testing.T) {
	tests := []struct {
		name          string
		src           string
		redundantInfo bool
	}{
		{
			name: "polarity and reversed operands",
			src: `fn direct(value: ?i32) -> i32 {
	if value != none {
		return value;
	}
	return 0;
}

fn reverseEq(value: ?i32) -> i32 {
	if none == value {
		return 0;
	}
	return value;
}

fn reverseNe(value: ?i32) -> i32 {
	if none != value {
		return value;
	}
	return 0;
}`,
		},
		{
			name: "terminating guard and inferred payload",
			src: `fn guarded(value: ?i32) -> i32 {
	if value == none {
		return 0;
	}
	let payload = value;
	return payload;
}`,
		},
		{
			name: "field and stable indexes",
			src: `struct Holder {
	field: ?i32,
	items: [2]?i32
}

struct Outer {
	inner: Holder
}

fn fields(outer: Outer, holder: Holder, index: usize) -> i32 {
	if outer.inner.field != none {
		return outer.inner.field;
	}
	if holder.field != none {
		return holder.field;
	}
	if holder.items[0] != none {
		return holder.items[0];
	}
	if holder.items[index] != none {
		return holder.items[index];
	}
	return 0;
}`,
		},
		{
			name:          "redundant optional needs one proof",
			redundantInfo: true,
			src: `fn nested(value: ? ?i32) -> i32 {
	if value != none {
		return value;
	}
	return 0;
}`,
		},
		{
			name:          "redundant optional test in eager boolean",
			redundantInfo: true,
			src: `fn nested(value: ? ?i32, enabled: bool) -> bool {
	return value != none && enabled;
}`,
		},
		{
			name: "nested inferred carrier and shadowed identity",
			src: `struct Holder { field: ?i32, fallback: i32 }

fn inferred(value: ?Holder) -> i32 {
	if value == none {
		return 0;
	}
	let inner = value.field;
	if inner == none {
		return value.fallback;
	}
	return inner;
}

fn shadowed(value: ?i32) -> i32 {
	if value == none {
		return 0;
	}
	{
		let value: ?i32 = none;
		if value != none {
			return value;
		}
	}
	return value;
}`,
		},
		{
			name: "join loop and eager result facts",
			src: `fn joined(value: ?i32, choose: bool) -> i32 {
	if choose {
		if value == none {
			return 0;
		}
	} else {
		if value == none {
			return 0;
		}
	}
	return value;
}

fn looped(value: ?i32) -> i32 {
	for value != none {
		return value;
	}
	return 0;
}

fn eager(value: ?i32) -> i32 {
	if value != none && true {
		return value;
	}
	if value == none || false {
		return 0;
	}
	return value;
}`,
		},
		{
			name: "payload descendant and disjoint mutation",
			src: `struct Payload {
	value: i32
}

struct Holder {
	maybe: ?i32,
	other: i32
}

fn Write(_: &mut i32) {}

fn descendant(mut value: ?Payload) -> i32 {
	if value == none {
		return 0;
	}
	value.value = 7;
	return value.value;
}

fn disjoint(mut holder: Holder) -> i32 {
	if holder.maybe == none {
		return 0;
	}
	holder.other = 1;
	Write(&mut holder.other);
	return holder.maybe;
}`,
		},
		{
			name: "mutable payload borrow preserves carrier presence",
			src: `fn Write(_: &mut i32) {}

fn read(mut value: ?i32) -> i32 {
	if value == none {
		return 0;
	}
	Write(&mut value);
	return value;
}`,
		},
		{
			name: "explicit optional reference preserves carrier",
			src: `fn Hold(_: &?i32) {}

fn valid(value: ?i32) {
	Hold(&value);
}`,
		},
		{
			name: "optional reference destination preserves carrier inside proof",
			src: `fn Hold(_: ?&?i32) {}

fn valid(value: ?i32) {
	if value == none {
		return;
	}
	Hold(&value);
}`,
		},
		{
			name: "mutable reference payload reborrow preserves parameter carrier",
			src: `fn Write(_: &mut i32) {}

fn valid(value: ?&mut i32) {
	if value == none {
		return;
	}
	Write(value);
	Write(value);
}`,
		},
		{
			name: "owned pointer payload mutation preserves carrier",
			src: `struct Holder { value: i32 }
fn Write(_: &mut i32) {}

fn valid(mut holder: ?*Holder) -> i32 {
	if holder == none {
		return 0;
	}
	Write(&mut holder.value);
	return holder.value;
}`,
		},
		{
			name: "proven optional receiver method",
			src: `struct Holder { value: i32 }
fn (self: &Holder) Get() -> i32 { return self.value; }

fn valid(value: ?Holder) -> i32 {
	if value == none {
		return 0;
	}
	return value.Get();
}`,
		},
		{
			name: "proven optional callable with optional result destination",
			src: `fn valid(callable: ?fn() -> i32) -> ?i32 {
	if callable == none {
		return none;
	}
	let result: ?i32 = callable();
	return result;
}`,
		},
		{
			name: "eager call ordering preserves fresh and disjoint proofs",
			src: `struct Holder {
	maybe: ?i32,
	other: i32
}

fn Mutate(_: &mut Holder) -> bool { return true; }

fn Touch(_: &mut i32) -> bool { return true; }

fn fresh(holder: &mut Holder) -> i32 {
	if Mutate(holder) && holder.maybe != none {
		return holder.maybe;
	}
	return 0;
}

fn disjoint(holder: &mut Holder) -> i32 {
	if holder.maybe != none && Touch(&mut holder.other) {
		return holder.maybe;
	}
	return 0;
}`,
		},
		{
			name: "eager later test restores invalidated proof",
			src: `fn Touch(_: &mut ?i32) -> bool { return true; }

fn valid(mut value: ?i32) -> i32 {
	if value != none && Touch(&mut value) && value != none {
		return value;
	}
	return 0;
}`,
		},
		{
			name: "reference alias shares presence proof",
			src: `struct Holder { maybe: ?i32 }

fn valid(holder: &Holder) -> i32 {
	let alias = holder;
	if alias.maybe == none {
		return 0;
	}
	return holder.maybe;
}`,
		},
		{
			name: "owned pointer payload consumption",
			src: `struct Holder { value: i32 }

fn valid(owner: ?*Holder) -> i32 {
	if owner == none {
		return 0;
	}
	let result = owner.value;
	free(owner);
	return result;
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, "", tt.src)
			if diag.HasErrors() {
				t.Fatalf("optional narrowing failed:\n%s", diag.EmitAllToString())
			}
			if got := hasDiagnosticCode(diag, diagnostics.InfoRedundantOptional); got != tt.redundantInfo {
				t.Fatalf("redundant optional info = %v, want %v:\n%s", got, tt.redundantInfo, diag.EmitAllToString())
			}
		})
	}
}

func TestPipelineRejectsInvalidOptionalPayloadAccess(t *testing.T) {
	tests := []struct {
		name string
		code string
		src  string
	}{
		{
			name: "missing proof",
			code: "T0041",
			src:  `fn invalid(value: ?i32) -> i32 { return value; }`,
		},
		{
			name: "method receiver missing proof",
			code: "T0041",
			src: `struct Holder { value: i32 }
fn (self: &Holder) Get() -> i32 { return self.value; }
fn invalid(value: ?Holder) -> i32 { return value.Get(); }`,
		},
		{
			name: "optional equality requires none operand",
			code: "T0004",
			src: `fn invalid(left: ?i32, right: ?i32) -> bool {
	return left == right;
}`,
		},
		{
			name: "join recheck clears stale field payload evidence",
			code: "T0041",
			src: `struct Holder { field: ?i32, other: ?i32 }

fn invalid(value: Holder) -> i32 {
	if value.field != none {
	} else {
		print(0);
		print(1);
	}
	if value.other == none {
		return 0;
	}
	return value.field;
}`,
		},
		{
			name: "computed index",
			code: "T0042",
			src: `fn invalid(values: [2]?i32, index: usize) -> i32 {
	if values[index + 1] != none {
		return values[index + 1];
	}
	return 0;
}`,
		},
		{
			name: "index dependency invalidated",
			code: "T0041",
			src: `fn invalid(values: [2]?i32) -> i32 {
	let mut index: usize = 0;
	if values[index] != none {
		index = 1;
		return values[index];
	}
	return 0;
}`,
		},
		{
			name: "index dependency invalidated through mutable alias",
			code: "T0041",
			src: `fn Change(_: &mut usize) {}

fn invalid(values: [2]?i32, mut index: usize) -> i32 {
	if values[index] == none {
		return 0;
	}
	Change(&mut index);
	return values[index];
}`,
		},
		{
			name: "carrier invalidated through mutable reference alias",
			code: "T0041",
			src: `struct Holder { maybe: ?i32 }

fn invalid(holder: &mut Holder) -> i32 {
	let alias = holder;
	if alias.maybe == none {
		return 0;
	}
	alias.maybe = none;
	return alias.maybe;
}`,
		},
		{
			name: "exact carrier assignment invalidated",
			code: "T0041",
			src: `fn invalid(mut value: ?i32) -> i32 {
	if value == none {
		return 0;
	}
	value = 1;
	return value;
}`,
		},
		{
			name: "ancestor assignment invalidated",
			code: "T0041",
			src: `struct Holder {
	field: ?i32
}

fn invalid(mut holder: Holder) -> i32 {
	if holder.field == none {
		return 0;
	}
	holder = Holder.{field = 1};
	return holder.field;
}`,
		},
		{
			name: "nested ancestor assignment invalidated",
			code: "T0041",
			src: `struct Holder {
	field: ?i32
}

struct Outer {
	inner: Holder
}

fn invalid(mut outer: Outer) -> i32 {
	if outer.inner.field == none {
		return 0;
	}
	outer.inner = Holder.{field = 1};
	return outer.inner.field;
}`,
		},
		{
			name: "mutable reference call invalidated",
			code: "T0041",
			src: `struct Holder {
	field: ?i32
}

fn Write(_: &mut Holder) {}

fn invalid(holder: &mut Holder) -> i32 {
	if holder.field == none {
		return 0;
	}
	Write(holder);
	return holder.field;
}`,
		},
		{
			name: "mutable reference call invalidates later argument",
			code: "T0041",
			src: `struct Holder {
	field: ?i32
}

fn Write(_: &mut Holder) -> i32 { return 0; }

fn Use(_: i32, _: i32) {}

fn invalid(holder: &mut Holder) {
	if holder.field == none {
		return;
	}
	Use(Write(holder), holder.field);
}`,
		},
		{
			name: "known raw pointer call invalidated",
			code: "T0041",
			src: `fn Touch(_: rawptr) {}

fn invalid(mut value: ?i32) -> i32 {
	if value == none {
		return 0;
	}
	Touch(@value);
	return value;
}`,
		},
		{
			name: "unknown raw pointer call invalidated",
			code: "T0041",
			src: `fn Touch(_: rawptr) {}

fn invalid(value: ?i32, pointer: rawptr) -> i32 {
	if value == none {
		return 0;
	}
	Touch(pointer);
	return value;
}`,
		},
		{
			name: "unknown raw pointer branch dominates known origin",
			code: "T0041",
			src: `struct Holder { maybe: ?i32, other: i32 }
fn Touch(_: rawptr) {}

fn invalid(mut holder: Holder, pointer: rawptr, choose: bool) -> i32 {
	let mut target: rawptr = @holder.other;
	if choose {
		target = pointer;
	}
	if holder.maybe == none {
		return 0;
	}
	Touch(target);
	return holder.maybe;
}`,
		},
		{
			name: "optional reference carrier assignment invalidated",
			code: "T0041",
			src: `fn Read(_: &i32) {}

fn invalid(value: i32) {
	let mut maybe: ?&i32 = &value;
	if maybe == none {
		return;
	}
	maybe = none;
	Read(maybe);
}`,
		},
		{
			name: "eager right operand has no proof",
			code: "T0041",
			src: `fn invalid(value: ?i32) -> bool {
	return value != none && value > 0;
}`,
		},
		{
			name: "eager later call invalidates result proof",
			code: "T0041",
			src: `struct Holder {
	maybe: ?i32
}

fn Mutate(_: &mut Holder) -> bool { return true; }

fn invalid(holder: &mut Holder) -> i32 {
	if holder.maybe != none && Mutate(holder) {
		return holder.maybe;
	}
	return 0;
}`,
		},
		{
			name: "unreachable payload use still checked",
			code: "T0041",
			src: `fn invalid(value: ?i32) -> i32 {
	return 0;
	return value;
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diag := buildPipelineTestWithConfig(t, project.Config{RootDir: ".", Extension: peeper.SourceExt}, "", tt.src)
			if !strings.Contains(diag.EmitAllToString(), tt.code) {
				t.Fatalf("expected %s diagnostic, got:\n%s", tt.code, diag.EmitAllToString())
			}
		})
	}
}

func TestModuleLoaderReportsSameIdentityFromDifferentFiles(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first"+peeper.SourceExt)
	secondPath := filepath.Join(root, "second"+peeper.SourceExt)
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(root, peeper.SourceExt, diag)
	loader := &moduleLoader{
		ctx:       ctx,
		scheduled: make(map[moduleid.ID]string),
	}
	id := moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "app/shared"}

	loader.enqueue(&module.Module{ID: id, FilePath: firstPath, Content: "fn main() {}\n", HasProvidedContent: true})
	loader.wg.Wait()
	loader.enqueue(&module.Module{ID: id, FilePath: secondPath, Content: "fn helper() {}\n", HasProvidedContent: true})
	loader.wg.Wait()

	ambiguous := 0
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrAmbiguousImport {
			ambiguous++
		}
	}
	if ambiguous != 1 {
		t.Fatalf("ambiguous-import diagnostics = %d, want 1:\n%s", ambiguous, diag.EmitAllToString())
	}
}

// An identity conflict reached through an import must name the import that
// caused it, so the reader has somewhere to go. The registry detects the
// conflict; the loader owns the source site.
func TestModuleLoaderLabelsImportSiteOnIdentityConflict(t *testing.T) {
	root := t.TempDir()
	srcDir := manifest.SourceDir(root)
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	sharedPath := filepath.Join(srcDir, "shared"+peeper.SourceExt)
	if err := os.WriteFile(sharedPath, []byte("fn Helper() {}\n"), 0o600); err != nil {
		t.Fatalf("write shared module: %v", err)
	}

	diag := diagnostics.NewDiagnosticBag()
	ctx := project.NewWithConfig(project.Config{
		RootDir:     root,
		ProjectName: "app",
		Extension:   peeper.SourceExt,
	}, diag)

	resolved, err := ctx.ResolveImportPath("app/shared")
	if err != nil {
		t.Fatalf("resolve import: %v", err)
	}
	// Claim the identity for a different file so the import below conflicts.
	ctx.AddModule(&module.Module{ID: resolved.ID, FilePath: filepath.Join(srcDir, "other"+peeper.SourceExt)})

	entryPath := filepath.Join(srcDir, "entry"+peeper.SourceExt)
	entrySrc := "import \"app/shared\";\n"
	diag.AddSourceContent(entryPath, entrySrc)
	importer := parseModuleSource(entryPath, entrySrc, diag)
	loader := &moduleLoader{ctx: ctx, scheduled: make(map[moduleid.ID]string)}
	loader.resolveImports(importer, diag)
	loader.wg.Wait()

	for _, item := range diag.Diagnostics() {
		if item == nil || item.Code != diagnostics.ErrAmbiguousImport {
			continue
		}
		if len(item.Labels) == 0 || item.Labels[0].Location == nil {
			t.Fatalf("ambiguous-import diagnostic carries no primary label:\n%s", diag.EmitAllToString())
		}
		if item.FilePath != project.CanonicalPath(entryPath) {
			t.Fatalf("diagnostic file = %q, want %q", item.FilePath, project.CanonicalPath(entryPath))
		}
		return
	}
	t.Fatalf("no ambiguous-import diagnostic:\n%s", diag.EmitAllToString())
}

func TestModuleLoaderSamePathDoubleEnqueueIsQuietDedupe(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "shared"+peeper.SourceExt)
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(root, peeper.SourceExt, diag)
	loader := &moduleLoader{
		ctx:       ctx,
		scheduled: make(map[moduleid.ID]string),
	}
	id := moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "app/shared"}

	loader.enqueue(&module.Module{ID: id, FilePath: filePath, Content: "fn main() {}\n", HasProvidedContent: true})
	loader.wg.Wait()
	loader.enqueue(&module.Module{ID: id, FilePath: filePath, Content: "fn main() {}\n", HasProvidedContent: true})
	loader.wg.Wait()

	if diag.HasErrors() {
		t.Fatalf("same-path dedupe produced diagnostics:\n%s", diag.EmitAllToString())
	}
}

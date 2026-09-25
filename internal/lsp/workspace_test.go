package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/fingerprint"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/module"
	"compiler/internal/phase"
	"compiler/internal/project"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

func TestWorkspaceIndexBuildsIndependentComponents(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, filepath.Join(root, "a"+peeper.SourceExt), "fn main() {}\n")
	writeWorkspaceFile(t, filepath.Join(root, "b"+peeper.SourceExt), "fn main() {}\n")

	index := newWorkspaceIndex(root)
	if _, err := index.rebuild(nil); err != nil {
		t.Fatalf("rebuild workspace index: %v", err)
	}

	if len(index.components) != 2 {
		t.Fatalf("components = %d, want 2", len(index.components))
	}
	for _, component := range index.components {
		if len(component.files) != 1 {
			t.Fatalf("component files = %v, want singleton", component.files)
		}
		if len(component.roots) != 1 || component.roots[0] != component.files[0] {
			t.Fatalf("component roots = %v, want %v", component.roots, component.files)
		}
	}
}

func TestWorkspaceFilesSkipsBuiltinLibraryDirectory(t *testing.T) {
	root := t.TempDir()
	localFile := filepath.Join(root, "main"+peeper.SourceExt)
	builtinFile := filepath.Join(root, "_builtin_library", "core", peeper.SourceDirName, "global"+peeper.SourceExt)
	writeWorkspaceFile(t, localFile, "fn main() {}\n")
	writeWorkspaceFile(t, builtinFile, "const stdout: i32 = 1;\n")

	files, err := workspaceFiles(root, nil)
	if err != nil {
		t.Fatalf("workspaceFiles: %v", err)
	}
	gotLocal := false
	for _, file := range files {
		if file == project.CanonicalPath(builtinFile) {
			t.Fatalf("builtin library file leaked into workspace index: %s", file)
		}
		if file == project.CanonicalPath(localFile) {
			gotLocal = true
		}
	}
	if !gotLocal {
		t.Fatalf("workspace files missing local source %s", localFile)
	}
}

func TestWorkspaceIndexGroupsImportedFiles(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	writeWorkspaceFile(t, filepath.Join(root, peeper.SourceDirName, peeper.MainFileName), "import \"app/lib/util\";\nfn main() {}\n")
	writeWorkspaceFile(t, filepath.Join(root, peeper.SourceDirName, "lib", "util"+peeper.SourceExt), "fn helper() {}\n")
	writeWorkspaceFile(t, filepath.Join(root, peeper.SourceDirName, "other"+peeper.SourceExt), "fn main() {}\n")

	index := newWorkspaceIndex(root)
	if _, err := index.rebuild(nil); err != nil {
		t.Fatalf("rebuild workspace index: %v", err)
	}

	if len(index.components) != 2 {
		t.Fatalf("components = %d, want 2", len(index.components))
	}

	var foundGrouped, foundSingleton bool
	mainFile := project.CanonicalPath(filepath.Join(root, peeper.SourceDirName, peeper.MainFileName))
	utilFile := project.CanonicalPath(filepath.Join(root, peeper.SourceDirName, "lib", "util"+peeper.SourceExt))
	otherFile := project.CanonicalPath(filepath.Join(root, peeper.SourceDirName, "other"+peeper.SourceExt))
	for _, component := range index.components {
		switch len(component.files) {
		case 2:
			foundGrouped = true
			if component.files[0] != mainFile && component.files[1] != mainFile {
				t.Fatalf("grouped component missing main.peep: %v", component.files)
			}
			if component.files[0] != utilFile && component.files[1] != utilFile {
				t.Fatalf("grouped component missing util.peep: %v", component.files)
			}
			if len(component.roots) != 1 || component.roots[0] != mainFile {
				t.Fatalf("grouped roots = %v, want [%s]", component.roots, mainFile)
			}
		case 1:
			if component.files[0] != otherFile {
				t.Fatalf("unexpected singleton component: %v", component.files)
			}
			foundSingleton = true
		}
	}

	if !foundGrouped || !foundSingleton {
		t.Fatalf("foundGrouped=%v foundSingleton=%v", foundGrouped, foundSingleton)
	}
}

func TestServerStateReusesUnchangedWorkspaceComponent(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "a")
	fileA := filepath.Join(root, peeper.SourceDirName, "a", peeper.MainFileName)
	fileAUtil := filepath.Join(root, peeper.SourceDirName, "a", "util"+peeper.SourceExt)
	fileB := filepath.Join(root, peeper.SourceDirName, "b"+peeper.SourceExt)
	writeWorkspaceFile(t, fileA, "import \"a/a/util\";\nfn main() { helper(); }\n")
	writeWorkspaceFile(t, fileAUtil, "fn helper() {}\n")
	writeWorkspaceFile(t, fileB, "fn main() {}\n")

	state := NewServerState()
	state.RootDir = root

	if _, mod := state.recompile(fileA); mod == nil {
		t.Fatalf("initial compile returned nil module")
	}
	if _, mod := state.recompile(fileB); mod == nil {
		t.Fatalf("independent component compile returned nil module")
	}
	before := state.modules[project.CanonicalPath(fileB)]
	if before == nil {
		t.Fatalf("missing cached unrelated module")
	}

	updated := "fn helper() { let x = 1; }\n"
	state.applyDocumentSnapshot(fileAUtil, &updated, nil)
	if _, mod := state.recompile(fileAUtil); mod == nil {
		t.Fatalf("recompile returned nil module")
	}

	after := state.modules[project.CanonicalPath(fileB)]
	if after == nil {
		t.Fatalf("missing cached unrelated module after recompile")
	}
	if before != after {
		t.Fatalf("expected unrelated component module reuse")
	}
}

func TestServerStateDoesNotReuseModulesAcrossCompilerInputs(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	entry := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	writeWorkspaceFile(t, entry, "fn main() {}\n")

	state := NewServerState()
	state.RootDir = root
	previous, mod := state.recompile(entry)
	if previous == nil || mod == nil || previous.Diagnostics.HasErrors() {
		t.Fatal("initial compile failed")
	}
	entryPath := project.CanonicalPath(entry)
	dirty := map[string]struct{}{entryPath: {}}
	for _, test := range []struct {
		name      string
		change    func(*project.Config)
		wantReuse bool
	}{
		{name: "unchanged", wantReuse: true},
		{name: "target", change: func(config *project.Config) {
			if config.TargetArch == "386" {
				config.TargetArch = "amd64"
			} else {
				config.TargetArch = "386"
			}
		}},
		{name: "project name", change: func(config *project.Config) {
			config.ProjectName = "different"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := previous.Config
			if test.change != nil {
				test.change(&config)
			}
			ctx := project.NewWithConfig(config, diagnostics.NewDiagnosticBag())
			ctx.Metrics = &project.CompileMetrics{}
			state.seedReusableModules(ctx, dirty)
			reused, found := ctx.ModuleByID(mod.ID)
			if found != test.wantReuse || found && (reused == mod || reused.AST != mod.AST) || (ctx.Metrics.ModulesReused != 0) != test.wantReuse {
				t.Fatalf("module reuse = %t, shell copied = %t, AST retained = %t, metric = %d; want reuse %t", found, reused != mod, found && reused.AST == mod.AST, ctx.Metrics.ModulesReused, test.wantReuse)
			}
		})
	}

	writeWorkspaceProjectConfig(t, root, "different")
	current, rebuilt := state.recompile(entry)
	if current == nil || rebuilt == nil || current.Diagnostics.HasErrors() {
		t.Fatal("compile after project rename failed")
	}
	if rebuilt == mod || rebuilt.ID == mod.ID || state.LastMetrics.ModulesReused != 0 {
		t.Fatalf("project rename reused old module: old ID %s, new ID %s, reused %d", mod.ID, rebuilt.ID, state.LastMetrics.ModulesReused)
	}
	if state.modules[entryPath] != rebuilt {
		t.Fatal("new compiler generation did not replace cached module")
	}
}

func TestServerStateReindexesReusedGenericDeclarations(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	entry := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	container := filepath.Join(root, peeper.SourceDirName, "container"+peeper.SourceExt)
	const initial = `import "app/container";
fn Take(value: container::Box<i32>) {}
fn main() {}`
	writeWorkspaceFile(t, entry, initial)
	writeWorkspaceFile(t, container, "struct Box<T> { value: T }\n")

	state := NewServerState()
	state.RootDir = root
	ctx, mod := state.recompile(entry)
	if mod == nil || ctx == nil || ctx.Diagnostics.HasErrors() {
		t.Fatalf("initial generic compile failed:\n%s", ctx.Diagnostics.EmitAllToString())
	}

	updated := `import "app/container";
fn Take(value: container::Box<i32>) {}
fn main() { let body_only = 1; }`
	state.applyDocumentSnapshot(entry, &updated, nil)
	ctx, mod = state.recompile(entry)
	if mod == nil || ctx == nil {
		t.Fatal("incremental generic compile returned nil")
	}
	if ctx.Diagnostics.HasErrors() {
		t.Fatalf("incremental generic compile lost declaration registry:\n%s", ctx.Diagnostics.EmitAllToString())
	}
}

func TestServerStateReplaysDiagnosticsForUnchangedModule(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	entry := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	writeWorkspaceFile(t, entry, `#[target_os("linux")]
fn unused() {}
fn main() -> i32 {
	if true { return 0; }
	return 1;
}
`)

	state := NewServerState()
	state.RootDir = root
	first, mod := state.recompile(entry)
	if mod == nil || first == nil || first.Diagnostics == nil {
		t.Fatal("initial compile returned no module diagnostics")
	}
	second, mod := state.recompile(entry)
	if mod == nil || second == nil || second.Diagnostics == nil {
		t.Fatal("reused compile returned no module diagnostics")
	}

	want := map[string]bool{
		diagnostics.WarnIgnoredTargetOS:       false,
		diagnostics.WarnConstantConditionTrue: false,
		diagnostics.WarnUnusedPrivateFunction: false,
	}
	for _, item := range second.Diagnostics.Diagnostics() {
		if _, tracked := want[item.Code]; tracked {
			want[item.Code] = true
		}
	}
	for code, found := range want {
		if !found {
			t.Errorf("reused diagnostics missing %s", code)
		}
	}
}

func TestServerStateReplaysErrorsBeforeLowering(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	entry := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	writeWorkspaceFile(t, entry, "fn main() -> i32 { return missing; }\n")

	state := NewServerState()
	state.RootDir = root
	first, mod := state.recompile(entry)
	if mod == nil || first == nil || !first.Diagnostics.HasErrors() {
		t.Fatal("initial compile did not report semantic error")
	}
	second, mod := state.recompile(entry)
	if mod == nil || second == nil || !second.Diagnostics.HasErrors() {
		t.Fatal("reused compile lost semantic error")
	}
	if mod.MIR != nil || mod.LLVMIR != "" {
		t.Fatal("reused erroneous module continued into lowering")
	}
}

func TestServerStateDoesNotReplayUsageDiagnosticsBeforeBarrier(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	entry := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	util := filepath.Join(root, peeper.SourceDirName, "lib", "util"+peeper.SourceExt)
	goodSource := "import \"app/lib/util\";\nfn main() -> i32 { return util::Value(); }\n"
	badSource := "import \"app/lib/util\";\nfn main() -> i32 { return missing; }\n"
	writeWorkspaceFile(t, entry, goodSource)
	writeWorkspaceFile(t, util, "fn Value() -> i32 { return 1; }\nfn unused() {}\n")
	hasCode := func(ctx *project.CompilerContext, code string) bool {
		for _, item := range ctx.Diagnostics.Diagnostics() {
			if item.Code == code {
				return true
			}
		}
		return false
	}

	state := NewServerState()
	state.RootDir = root
	first, mod := state.recompile(entry)
	if mod == nil || first == nil || !hasCode(first, diagnostics.WarnUnusedPrivateFunction) {
		t.Fatal("initial compile missing usage warning")
	}

	state.applyDocumentSnapshot(entry, &badSource, nil)
	incremental, mod := state.recompile(entry)
	if mod == nil || incremental == nil || !incremental.Diagnostics.HasErrors() {
		t.Fatal("incremental compile missing semantic error")
	}
	if hasCode(incremental, diagnostics.WarnUnusedPrivateFunction) {
		t.Fatal("incremental compile replayed usage warning before project Usage barrier")
	}

	freshState := NewServerState()
	freshState.RootDir = root
	freshState.applyDocumentSnapshot(entry, &badSource, nil)
	fresh, freshMod := freshState.recompile(entry)
	if freshMod == nil || fresh == nil || !fresh.Diagnostics.HasErrors() {
		t.Fatal("fresh compile missing semantic error")
	}
	if hasCode(fresh, diagnostics.WarnUnusedPrivateFunction) {
		t.Fatal("fresh compile unexpectedly produced usage warning")
	}

	state.applyDocumentSnapshot(entry, &goodSource, nil)
	repaired, mod := state.recompile(entry)
	if mod == nil || repaired == nil || repaired.Diagnostics.HasErrors() {
		t.Fatal("repaired incremental compile failed")
	}
	if !hasCode(repaired, diagnostics.WarnUnusedPrivateFunction) {
		t.Fatal("repaired incremental compile lost reusable usage warning")
	}
}

func TestWorkspaceSyntheticEntryUsesRequestedComponentRoots(t *testing.T) {
	root := t.TempDir()
	fileA := filepath.Join(root, "a"+peeper.SourceExt)
	fileB := filepath.Join(root, "b"+peeper.SourceExt)
	writeWorkspaceFile(t, fileA, "fn main() {}\n")
	writeWorkspaceFile(t, fileB, "fn main() {}\n")

	index := newWorkspaceIndex(root)
	if _, err := index.rebuild(nil); err != nil {
		t.Fatalf("rebuild workspace index: %v", err)
	}

	_, content, ok := index.syntheticEntry(fileA)
	if !ok {
		t.Fatalf("expected synthetic entry")
	}
	if got, want := strings.Count(content, "import "), 1; got != want {
		t.Fatalf("synthetic import count = %d, want %d\ncontent:\n%s", got, want, content)
	}
	if !strings.Contains(content, "\"a\"") {
		t.Fatalf("synthetic entry missing requested component root import: %s", content)
	}
	if strings.Contains(content, "\"b\"") {
		t.Fatalf("synthetic entry leaked unrelated root import: %s", content)
	}
}

func TestServerStateRecompileSkipsUnrelatedIndependentRoot(t *testing.T) {
	root := t.TempDir()
	fileA := filepath.Join(root, "a"+peeper.SourceExt)
	fileB := filepath.Join(root, "b"+peeper.SourceExt)
	writeWorkspaceFile(t, fileA, "fn main() {}\n")
	writeWorkspaceFile(t, fileB, "fn main() {}\n")

	state := NewServerState()
	state.RootDir = root
	if _, mod := state.recompile(fileA); mod == nil {
		t.Fatalf("initial compile returned nil module")
	}

	aPath := project.CanonicalPath(fileA)
	bPath := project.CanonicalPath(fileB)
	if state.modules[aPath] == nil {
		t.Fatalf("missing requested module")
	}
	if state.modules[bPath] != nil {
		t.Fatalf("unrelated singleton root should not be compiled when requesting %s", aPath)
	}
}

func TestServerStateReusesDependentWhenExportShapeUnchanged(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() { helper(); }\n")
	writeWorkspaceFile(t, fileUtil, "fn helper() {}\n")

	state := NewServerState()
	state.RootDir = root
	if _, mod := state.recompile(fileMain); mod == nil {
		t.Fatalf("initial compile returned nil module")
	}

	before := state.modules[project.CanonicalPath(fileMain)]
	if before == nil {
		t.Fatalf("missing cached dependent module")
	}

	updated := "fn helper() { let x = 1; }\n"
	state.applyDocumentSnapshot(fileUtil, &updated, nil)
	if _, mod := state.recompile(fileUtil); mod == nil {
		t.Fatalf("recompile returned nil module")
	}

	after := state.modules[project.CanonicalPath(fileMain)]
	if after == nil || before == after || before.AST != after.AST || before.THIR != after.THIR {
		t.Fatalf("expected dependent artifacts in a new module shell when export shape is unchanged")
	}
}

func TestWorkspaceReuseReadsPublishedASTSurface(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() { helper(); }\n")
	writeWorkspaceFile(t, fileUtil, "fn helper() {}\n")

	state := NewServerState()
	state.RootDir = root
	if _, mod := state.recompile(fileMain); mod == nil {
		t.Fatal("initial compile returned nil module")
	}
	mainPath := project.CanonicalPath(fileMain)
	utilPath := project.CanonicalPath(fileUtil)
	cached := make(map[string]*module.Module)
	for _, path := range []string{mainPath, utilPath} {
		compiled := state.modules[path]
		if compiled == nil || compiled.AST == nil {
			t.Fatalf("missing parsed module %s", path)
		}
		cached[path] = &module.Module{
			FilePath:    compiled.FilePath,
			ContentHash: compiled.ContentHash,
			Phase:       compiled.Phase,
			AST:         compiled.AST,
			Imports:     compiled.Imports,
		}
	}

	updated := "fn helper() { let x = 1; }\n"
	state.applyDocumentSnapshot(fileUtil, &updated, nil)
	index := newWorkspaceIndex(root)
	if _, err := index.rebuild(state.Cache); err != nil {
		t.Fatalf("rebuild workspace index: %v", err)
	}
	if dirty := index.dirtyFiles(fileUtil, cached); len(dirty) != 1 {
		t.Fatalf("dirty files = %v, want only changed function module", dirty)
	}
	if got := index.reusePhases(fileUtil, cached)[mainPath]; got != cached[mainPath].Phase {
		t.Fatalf("dependent reuse phase = %v, want %v", got, cached[mainPath].Phase)
	}
	cached[utilPath].AST = nil
	if dirty := index.dirtyFiles(fileUtil, cached); len(dirty) != 2 {
		t.Fatalf("dirty files with missing syntax = %v, want changed module and importer", dirty)
	}
	if got := index.reusePhases(fileUtil, cached)[mainPath]; got != phase.Parsed {
		t.Fatalf("dependent reuse with missing syntax = %v, want %v", got, phase.Parsed)
	}
}

func TestServerStateRebuildsLaterFunctionAfterEarlierBodyEdit(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	entry := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	writeWorkspaceFile(t, entry, "fn Prep() {}\nfn main() -> i32 { return 7; }\n")

	state := NewServerState()
	state.RootDir = root
	ctx, before := state.recompile(entry)
	if before == nil || ctx == nil || ctx.Diagnostics.HasErrors() || before.THIR == nil || len(before.THIR.Functions) != 2 {
		t.Fatalf("initial compile failed: %v", ctx)
	}
	previousFunction := before.THIR.Functions[1]
	previousID := previousFunction.Source.NodeID
	previousSurface := before.AST.ExportFingerprint

	updated := "fn Prep() { let x = 1; let y = 2; }\nfn main() -> i32 { return 7; }\n"
	state.applyDocumentSnapshot(entry, &updated, nil)
	ctx, after := state.recompile(entry)
	if after == nil || ctx == nil || ctx.Diagnostics.HasErrors() || after.THIR == nil || len(after.THIR.Functions) != 2 {
		t.Fatalf("incremental compile failed: %v", ctx)
	}
	if after.AST.ExportFingerprint != previousSurface {
		t.Fatal("body-only edit changed declaration surface")
	}
	if after == before || after.THIR.Functions[1] == previousFunction {
		t.Fatal("changed module reused prior-generation function")
	}
	currentID := after.THIR.Functions[1].Source.NodeID
	if currentID == previousID {
		t.Fatalf("later function ID stayed %d despite earlier body adding nodes", currentID)
	}
	if after.THIR.Function(currentID) != after.THIR.Functions[1] {
		t.Fatal("rebuilt function is not indexed under current-generation ID")
	}
}

func TestWorkspaceParseFeedsChangedModuleCompilation(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	entry := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	writeWorkspaceFile(t, entry, "fn main() -> i32 { return 1; }\n")

	state := NewServerState()
	state.RootDir = root
	if ctx, mod := state.recompile(entry); ctx == nil || mod == nil || ctx.Diagnostics.HasErrors() {
		t.Fatal("initial compile failed")
	}
	updated := "fn main() -> i32 { return 2; }\n"
	state.applyDocumentSnapshot(entry, &updated, nil)
	ctx, mod := state.recompile(entry)
	if ctx == nil || mod == nil || ctx.Diagnostics.HasErrors() {
		t.Fatal("updated compile failed")
	}
	if state.workspace.parsedFiles != 1 || ctx.Metrics.ModulesParsed != 1 {
		t.Fatalf("workspace parses = %d, compiler parses = %d; want one workspace parse and only synthetic entry parse", state.workspace.parsedFiles, ctx.Metrics.ModulesParsed)
	}
	if mod.THIR == nil || mod.CFG == nil || mod.THIR.Validate() != nil || mod.CFG.Validate() != nil {
		t.Fatal("changed module lost typed or control-flow artifacts")
	}
}

func TestCaptureModulesKeepsCompiledSourceIdentity(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	entry := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	oldSource := "fn main() -> i32 { return 1; }\n"
	newSource := "fn main() -> i32 { return 2; }\n"
	writeWorkspaceFile(t, entry, oldSource)

	state := NewServerState()
	state.RootDir = root
	previousCtx, previous := state.recompile(entry)
	if previousCtx == nil || previous == nil || previous.AST == nil || previousCtx.Diagnostics.HasErrors() {
		t.Fatal("initial compile failed")
	}
	writeWorkspaceFile(t, entry, newSource)
	if _, err := state.workspace.rebuild(nil); err != nil {
		t.Fatalf("refresh workspace index: %v", err)
	}
	state.captureModules(previousCtx)
	if previous.ContentHash != fingerprint.Text(oldSource) {
		t.Fatal("capture relabeled old AST with new source hash")
	}

	updatedCtx, updated := state.recompile(entry)
	if updatedCtx == nil || updated == nil || updatedCtx.Diagnostics.HasErrors() || updated.AST == previous.AST {
		t.Fatal("changed source reused old AST")
	}
	clean := NewServerState()
	clean.RootDir = root
	cleanCtx, cleanModule := clean.recompile(entry)
	if cleanCtx == nil || cleanModule == nil || cleanCtx.Diagnostics.HasErrors() {
		t.Fatal("clean compile failed")
	}
	if updated.ContentHash != cleanModule.ContentHash || updated.LLVMIR != cleanModule.LLVMIR ||
		updatedCtx.Diagnostics.EmitAllToString() != cleanCtx.Diagnostics.EmitAllToString() {
		t.Fatal("incremental compile differs from clean compile")
	}
}

func TestWorkspaceParseRetainsDiagnosticsDuringWorkspaceRefresh(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	entry := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	content := "fn main() { let x = xs[; }\n"
	writeWorkspaceFile(t, entry, content)

	parseDiagnostics := diagnostics.NewDiagnosticBag()
	parser.New(entry, lexer.New(entry, content, parseDiagnostics).Tokenize(), parseDiagnostics).ParseModule()
	want := parseDiagnostics.Diagnostics()
	if len(want) == 0 {
		t.Fatal("invalid source produced no parser diagnostics")
	}

	state := NewServerState()
	state.RootDir = root
	snapshots := state.workspaceDiagnosticSnapshots()
	if len(snapshots) != 1 || snapshots[0].ctx == nil {
		t.Fatalf("workspace diagnostic snapshots = %d, want one compiled snapshot", len(snapshots))
	}
	ctx := snapshots[0].ctx
	if ctx.Metrics.ModulesParsed != 1 {
		t.Fatalf("compiler parses = %d, want synthetic entry only", ctx.Metrics.ModulesParsed)
	}
	got := ctx.Diagnostics.Diagnostics()
	for _, expected := range want {
		if !slices.ContainsFunc(got, func(actual *diagnostics.Diagnostic) bool {
			return reflect.DeepEqual(actual, expected)
		}) {
			t.Fatalf("missing parser diagnostic %#v in %#v", expected, got)
		}
	}
}

func TestWorkspaceDiagnosticSnapshotsReuseIndexAcrossComponents(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainFile := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	otherFile := filepath.Join(root, peeper.SourceDirName, "other"+peeper.SourceExt)
	writeWorkspaceFile(t, mainFile, "fn main() -> i32 { return 1; }\n")
	writeWorkspaceFile(t, otherFile, "fn main() -> i32 { return 3; }\n")

	state := NewServerState()
	state.RootDir = root
	if snapshots := state.workspaceDiagnosticSnapshots(); len(snapshots) != 2 {
		t.Fatalf("initial snapshots = %d, want 2", len(snapshots))
	}
	updated := "fn main() -> i32 { return 2; }\n"
	state.applyDocumentSnapshot(mainFile, &updated, nil)
	snapshots := state.workspaceDiagnosticSnapshots()
	if len(snapshots) != 2 || state.workspace.parsedFiles != 1 {
		t.Fatalf("snapshots = %d, workspace parses = %d; want 2 snapshots and 1 parse", len(snapshots), state.workspace.parsedFiles)
	}

	clean := NewServerState()
	clean.RootDir = root
	clean.applyDocumentSnapshot(mainFile, &updated, nil)
	want := clean.workspaceDiagnosticSnapshots()
	if len(want) != len(snapshots) {
		t.Fatalf("clean snapshots = %d, want %d", len(want), len(snapshots))
	}
	for i, snapshot := range snapshots {
		if !slices.Equal(snapshot.files, want[i].files) ||
			snapshot.ctx.Diagnostics.EmitAllToString() != want[i].ctx.Diagnostics.EmitAllToString() {
			t.Fatalf("snapshot %d files or diagnostics differ from clean build: files %v vs %v, diagnostics %q vs %q", i, snapshot.files, want[i].files, snapshot.ctx.Diagnostics.EmitAllToString(), want[i].ctx.Diagnostics.EmitAllToString())
		}
		filePath := snapshot.files[0]
		mod, ok := snapshot.ctx.ModuleByFile(filePath)
		cleanMod, cleanOK := want[i].ctx.ModuleByFile(filePath)
		if !ok || !cleanOK || mod.ContentHash != cleanMod.ContentHash || mod.LLVMIR != cleanMod.LLVMIR {
			t.Fatalf("snapshot %d module differs from clean build", i)
		}
	}
}

func TestServerStateInvalidatesDependentWhenExportShapeChanges(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() { helper(); }\n")
	writeWorkspaceFile(t, fileUtil, "fn helper() {}\n")

	state := NewServerState()
	state.RootDir = root
	if _, mod := state.recompile(fileMain); mod == nil {
		t.Fatalf("initial compile returned nil module")
	}

	before := state.modules[project.CanonicalPath(fileMain)]
	if before == nil {
		t.Fatalf("missing cached dependent module")
	}

	updated := "fn helper(v: i32) {}\n"
	state.applyDocumentSnapshot(fileUtil, &updated, nil)
	if _, mod := state.recompile(fileUtil); mod == nil {
		t.Fatalf("recompile returned nil module")
	}

	after := state.modules[project.CanonicalPath(fileMain)]
	if before == after {
		t.Fatalf("expected dependent invalidation when export shape changes")
	}
}

func TestServerStateInvalidatesDependentWhenInferredExportTypeChanges(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() {}\n")
	writeWorkspaceFile(t, fileUtil, "const Value = 1i32;\n")

	state := NewServerState()
	state.RootDir = root
	if _, mod := state.recompile(fileMain); mod == nil {
		t.Fatalf("initial compile returned nil module")
	}

	before := state.modules[project.CanonicalPath(fileMain)]
	if before == nil {
		t.Fatal("missing cached dependent module")
	}
	beforeUtil := state.modules[project.CanonicalPath(fileUtil)]
	if beforeUtil == nil || beforeUtil.ModuleScope == nil {
		t.Fatal("missing cached export module")
	}
	beforeSyntaxFingerprint := beforeUtil.AST.ExportFingerprint
	beforeFingerprint := beforeUtil.SemanticExportFingerprint
	beforeType, found := beforeUtil.ModuleScope.Lookup("Value")
	if !found || beforeType == nil || beforeType.Type == nil {
		t.Fatal("missing cached exported const type")
	}
	updated := "const Value = 1i64;\n"
	state.applyDocumentSnapshot(fileUtil, &updated, nil)
	if _, mod := state.recompile(fileUtil); mod == nil {
		t.Fatalf("recompile returned nil module")
	}

	afterUtil := state.modules[project.CanonicalPath(fileUtil)]
	if afterUtil == nil || afterUtil.ModuleScope == nil {
		t.Fatal("missing recompiled export module")
	}
	if afterUtil.AST.ExportFingerprint != beforeSyntaxFingerprint {
		t.Fatalf("syntax fingerprint changed across inferred type edit: %q -> %q",
			beforeSyntaxFingerprint, afterUtil.AST.ExportFingerprint)
	}
	afterType, found := afterUtil.ModuleScope.Lookup("Value")
	if !found || afterType == nil || afterType.Type == nil {
		t.Fatal("missing recompiled exported const type")
	}
	if state.LastMetrics.ModulesDowngraded == 0 {
		t.Fatalf("dependent retained compiled phases after inferred export type changed: semantic %q -> %q, type %s -> %s",
			beforeFingerprint, afterUtil.SemanticExportFingerprint, beforeType.Type.Text(), afterType.Type.Text())
	}
	after := state.modules[project.CanonicalPath(fileMain)]
	if after == nil {
		t.Fatal("dependent missing after semantic invalidation")
	}
	if after.Phase != phase.Backend {
		t.Fatalf("dependent phase = %v, want completed backend after invalidation", after.Phase)
	}
}

func TestServerStateRecompileReturnsRequestedWorkspaceModule(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() { helper(); }\n")
	writeWorkspaceFile(t, fileUtil, "fn helper() {}\n")

	state := NewServerState()
	state.RootDir = root

	_, mod := state.recompile(fileUtil)
	if mod == nil {
		t.Fatalf("expected compiled module")
	}
	if want := project.CanonicalPath(fileUtil); mod.FilePath != want {
		t.Fatalf("module path = %s, want %s", mod.FilePath, want)
	}
	if len(mod.AST.Stmts) != 1 {
		t.Fatalf("util module stmts = %d, want 1", len(mod.AST.Stmts))
	}
	if fn, ok := mod.AST.Stmts[0].(*ast.FnDecl); !ok || fn.Name == nil || fn.Name.Name != "helper" {
		t.Fatalf("expected helper function module, got %T", mod.AST.Stmts[0])
	}
}

func TestServerStateInvalidatesTransitiveDependentsWhenExportChanges(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileMid := filepath.Join(root, peeper.SourceDirName, "mid"+peeper.SourceExt)
	fileLeaf := filepath.Join(root, peeper.SourceDirName, "leaf"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/mid\";\nfn main() { mid(); }\n")
	writeWorkspaceFile(t, fileMid, "import \"app/leaf\";\nfn mid() { leaf(); }\n")
	writeWorkspaceFile(t, fileLeaf, "fn leaf() {}\n")

	state := NewServerState()
	state.RootDir = root
	if _, mod := state.recompile(fileMain); mod == nil {
		t.Fatalf("initial compile returned nil module")
	}

	beforeMain := state.modules[project.CanonicalPath(fileMain)]
	beforeMid := state.modules[project.CanonicalPath(fileMid)]
	if beforeMain == nil || beforeMid == nil {
		t.Fatalf("missing cached dependents")
	}

	updated := "fn leaf(v: i32) {}\n"
	state.applyDocumentSnapshot(fileLeaf, &updated, nil)
	if _, mod := state.recompile(fileLeaf); mod == nil {
		t.Fatalf("recompile returned nil module")
	}

	afterMain := state.modules[project.CanonicalPath(fileMain)]
	afterMid := state.modules[project.CanonicalPath(fileMid)]
	if beforeMid == afterMid {
		t.Fatalf("expected direct dependent invalidation")
	}
	if beforeMain == afterMain {
		t.Fatalf("expected transitive dependent invalidation")
	}
}

func TestWorkspaceReusePhasesDowngradesDependentToParsed(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() { helper(); }\n")
	writeWorkspaceFile(t, fileUtil, "fn helper() {}\n")

	state := NewServerState()
	state.RootDir = root
	if _, mod := state.recompile(fileMain); mod == nil {
		t.Fatalf("initial compile returned nil module")
	}

	updated := "fn helper(v: i32) {}\n"
	state.applyDocumentSnapshot(fileUtil, &updated, nil)
	index := newWorkspaceIndex(root)
	if _, err := index.rebuild(state.Cache); err != nil {
		t.Fatalf("rebuild workspace index: %v", err)
	}

	phases := index.reusePhases(fileUtil, state.modules)
	mainPath := project.CanonicalPath(fileMain)
	utilPath := project.CanonicalPath(fileUtil)
	if _, ok := phases[utilPath]; ok {
		t.Fatalf("changed source module should not be reused")
	}
	if got := phases[mainPath]; got != phase.Parsed {
		t.Fatalf("dependent reuse phase = %v, want %v", got, phase.Parsed)
	}
}

func TestServerStateParsedResetRebuildsImportedDefaultProvenance(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileExternal := filepath.Join(root, peeper.SourceDirName, "external"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, `import "app/external";

fn main() -> i32 {
	return external::Read();
}
`)
	const external = `const value: i32 = 7;

fn Read(input: i32 = value) -> i32 {
	return input;
}
`
	writeWorkspaceFile(t, fileExternal, external)

	state := NewServerState()
	state.RootDir = root
	ctx, mod := state.recompile(fileMain)
	if ctx == nil || mod == nil {
		t.Fatal("initial compile returned nil context or module")
	}
	if ctx.Diagnostics.HasErrors() {
		t.Fatalf("initial compile diagnostics:\n%s", ctx.Diagnostics.EmitAllToString())
	}

	updated := external + "\nfn Added() {}\n"
	state.applyDocumentSnapshot(fileExternal, &updated, nil)
	ctx, mod = state.recompile(fileExternal)
	if ctx == nil || mod == nil {
		t.Fatal("incremental compile returned nil context or module")
	}
	if got := state.LastMetrics.ModulesDowngraded; got != 1 {
		t.Fatalf("modules downgraded = %d, want 1 dependent reset to Parsed", got)
	}
	if ctx.Diagnostics.HasErrors() {
		t.Fatalf("unexpected diagnostics after dependent Parsed reset:\n%s", ctx.Diagnostics.EmitAllToString())
	}

	mainModule := state.modules[project.CanonicalPath(fileMain)]
	if mainModule == nil {
		t.Fatal("missing recompiled dependent module")
	}
	if mainModule.Phase != phase.Backend {
		t.Fatalf("dependent phase = %v, want %v", mainModule.Phase, phase.Backend)
	}
	var call *ast.CallExpr
	for _, stmt := range mainModule.AST.Stmts {
		ast.Inspect(stmt, func(node ast.Node) bool {
			candidate, ok := node.(*ast.CallExpr)
			if ok && ast.ExprText(candidate.Callee) == "external::Read" {
				call = candidate
				return false
			}
			return call == nil
		})
		if call != nil {
			break
		}
	}
	if call == nil || len(call.Args) != 0 {
		t.Fatalf("source call after reset = %#v, want zero source arguments", call)
	}
	effectiveArgs := mainModule.Typechecking.CallArgumentsOrSource(call)
	if len(effectiveArgs) != 1 {
		t.Fatalf("effective arguments after reset = %#v, want one rebuilt default", effectiveArgs)
	}
	ident, ok := effectiveArgs[0].(*ast.Ident)
	if !ok || mainModule.Bindings == nil || mainModule.Bindings.Symbol(ident) == nil {
		t.Fatalf("rebuilt default = %#v, want resolved imported identifier", effectiveArgs[0])
	}
	if !mainModule.Typechecking.ExpandedDefaultBinding(ident.ID()) {
		t.Fatalf("rebuilt default identifier %d missing declaration-binding provenance", ident.ID())
	}
}

func TestWorkspaceIndexRebuildParsesOnlyChangedFiles(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() { helper(); }\n")
	writeWorkspaceFile(t, fileUtil, "fn helper() {}\n")

	index := newWorkspaceIndex(root)
	if _, err := index.rebuild(nil); err != nil {
		t.Fatalf("initial rebuild: %v", err)
	}
	if got := index.parsedFiles; got != 2 {
		t.Fatalf("initial parsed files = %d, want 2", got)
	}

	mainPath := project.CanonicalPath(fileMain)
	utilPath := project.CanonicalPath(fileUtil)
	beforeMain := index.modules[mainPath]
	beforeUtilTargets := append([]string(nil), index.modules[utilPath].resolvedLocalImportFiles...)

	state := NewServerState()
	updated := "fn helper() { let body_only = 1; }\n"
	state.applyDocumentSnapshot(fileUtil, &updated, nil)
	if _, err := index.rebuild(state.Cache); err != nil {
		t.Fatalf("incremental rebuild: %v", err)
	}
	if got := index.parsedFiles; got != 1 {
		t.Fatalf("incremental parsed files = %d, want 1", got)
	}
	if afterMain := index.modules[mainPath]; afterMain != beforeMain {
		t.Fatalf("unchanged importer should reuse cached workspace surface")
	}
	if got := index.modules[utilPath].resolvedLocalImportFiles; !slices.Equal(got, beforeUtilTargets) {
		t.Fatalf("body-only edit changed import targets: got %v want %v", got, beforeUtilTargets)
	}
}

func TestWorkspaceIndexKeepsProjectImportContextsSeparate(t *testing.T) {
	root := t.TempDir()
	for _, projectName := range []string{"first", "second"} {
		projectRoot := filepath.Join(root, projectName)
		writeWorkspaceProjectConfig(t, projectRoot, projectName)
		writeWorkspaceFile(t, filepath.Join(projectRoot, peeper.SourceDirName, peeper.MainFileName),
			"import \""+projectName+"/util\";\nfn main() {}\n")
		writeWorkspaceFile(t, filepath.Join(projectRoot, peeper.SourceDirName, "util"+peeper.SourceExt),
			"fn helper() {}\n")
	}

	index := newWorkspaceIndex(root)
	if _, err := index.rebuild(nil); err != nil {
		t.Fatal(err)
	}
	for _, projectName := range []string{"first", "second"} {
		projectRoot := filepath.Join(root, projectName)
		mainPath := project.CanonicalPath(filepath.Join(projectRoot, peeper.SourceDirName, peeper.MainFileName))
		utilPath := project.CanonicalPath(filepath.Join(projectRoot, peeper.SourceDirName, "util"+peeper.SourceExt))
		main := index.modules[mainPath]
		if main == nil || main.importPath != projectName+"/main" ||
			!slices.Equal(main.resolvedLocalImportFiles, []string{utilPath}) {
			t.Fatalf("%s project imports = %#v, want %s/util -> %s", projectName, main, projectName, utilPath)
		}
	}
}

func BenchmarkWorkspaceIndexRebuildUnchanged(b *testing.B) {
	root := b.TempDir()
	sourceDir := filepath.Join(root, peeper.SourceDirName)
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte("name = \"app\"\nbuild = \"program\"\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	for i := range 64 {
		path := filepath.Join(sourceDir, fmt.Sprintf("module%d%s", i, peeper.SourceExt))
		if err := os.WriteFile(path, []byte("fn helper() {}\n"), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	index := newWorkspaceIndex(root)
	if _, err := index.rebuild(nil); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := index.rebuild(nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWorkspaceDiagnosticSnapshotsUnchanged(b *testing.B) {
	root := b.TempDir()
	sourceDir := filepath.Join(root, peeper.SourceDirName)
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte("name = \"app\"\nbuild = \"program\"\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	for i := range 8 {
		path := filepath.Join(sourceDir, fmt.Sprintf("module%d%s", i, peeper.SourceExt))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("fn helper%d() {}\n", i)), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	state := NewServerState()
	state.RootDir = root
	if snapshots := state.workspaceDiagnosticSnapshots(); len(snapshots) != 8 {
		b.Fatalf("workspace snapshots = %d, want 8", len(snapshots))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if snapshots := state.workspaceDiagnosticSnapshots(); len(snapshots) != 8 {
			b.Fatalf("workspace snapshots = %d, want 8", len(snapshots))
		}
	}
}

func TestWorkspaceIndexRebuildRefreshesImportTargetsWhenNewFileAppears(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() { helper(); }\n")

	index := newWorkspaceIndex(root)
	if _, err := index.rebuild(nil); err != nil {
		t.Fatalf("initial rebuild: %v", err)
	}
	if got := index.parsedFiles; got != 1 {
		t.Fatalf("initial parsed files = %d, want 1", got)
	}

	mainPath := project.CanonicalPath(fileMain)
	component, ok := index.componentForFile(mainPath)
	if !ok || len(component.files) != 1 {
		t.Fatalf("expected unresolved importer to start as singleton, got %#v", component)
	}

	writeWorkspaceFile(t, fileUtil, "fn helper() {}\n")
	if _, err := index.rebuild(nil); err != nil {
		t.Fatalf("rebuild after adding util: %v", err)
	}
	if got := index.parsedFiles; got != 1 {
		t.Fatalf("parsed files after adding util = %d, want 1", got)
	}

	utilPath := project.CanonicalPath(fileUtil)
	if got := index.modules[mainPath].resolvedLocalImportFiles; !slices.Equal(got, []string{utilPath}) {
		t.Fatalf("main import targets = %v, want [%s]", got, utilPath)
	}
	component, ok = index.componentForFile(mainPath)
	if !ok || len(component.files) != 2 {
		t.Fatalf("expected importer and new target in same component, got %#v", component)
	}
}

func TestWorkspaceIndexRebuildRefreshesImportTargetsWhenFileLeavesSourceDir(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	outsideUtil := filepath.Join(root, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() { helper(); }\n")
	writeWorkspaceFile(t, fileUtil, "fn helper() {}\n")

	index := newWorkspaceIndex(root)
	if _, err := index.rebuild(nil); err != nil {
		t.Fatalf("initial rebuild: %v", err)
	}

	mainPath := project.CanonicalPath(fileMain)
	utilPath := project.CanonicalPath(fileUtil)
	component, ok := index.componentForFile(mainPath)
	if !ok || len(component.files) != 2 {
		t.Fatalf("expected importer and util in same component, got %#v", component)
	}
	if got := index.modules[mainPath].resolvedLocalImportFiles; !slices.Equal(got, []string{utilPath}) {
		t.Fatalf("initial import targets = %v, want [%s]", got, utilPath)
	}

	if err := os.Rename(fileUtil, outsideUtil); err != nil {
		t.Fatalf("util outside src: %v", err)
	}
	if _, err := index.rebuild(nil); err != nil {
		t.Fatalf("rebuild after moving util: %v", err)
	}
	if got := index.parsedFiles; got != 0 {
		t.Fatalf("parsed files after util leaves src = %d, want 0", got)
	}
	if got := index.modules[mainPath].resolvedLocalImportFiles; len(got) != 0 {
		t.Fatalf("main import targets after util leaves src = %v, want empty", got)
	}
	component, ok = index.componentForFile(mainPath)
	if !ok || len(component.files) != 1 {
		t.Fatalf("expected importer to become singleton after util leaves src, got %#v", component)
	}
	if _, ok := index.modules[utilPath]; ok {
		t.Fatalf("util should be removed from workspace modules after leaving src")
	}

	writeWorkspaceFile(t, fileUtil, "fn helper() {}\n")
	if _, err := index.rebuild(nil); err != nil {
		t.Fatalf("rebuild after restoring util: %v", err)
	}
	if got := index.parsedFiles; got != 1 {
		t.Fatalf("parsed files after restoring util = %d, want 1", got)
	}
	if got := index.modules[mainPath].resolvedLocalImportFiles; !slices.Equal(got, []string{utilPath}) {
		t.Fatalf("restored import targets = %v, want [%s]", got, utilPath)
	}
}

func TestServerStateRechecksImporterWhenTargetMembershipChanges(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	mainFile := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	utilFile := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, mainFile, "import \"app/util\";\nfn main() {}\n")

	state := NewServerState()
	state.RootDir = root
	missing, importer := state.recompile(mainFile)
	if missing == nil || importer == nil || !missing.Diagnostics.HasErrors() {
		t.Fatal("missing target should produce import diagnostics")
	}

	writeWorkspaceFile(t, utilFile, "fn helper() {}\n")
	added, rebuilt := state.recompile(mainFile)
	if added == nil || rebuilt == nil || added.Diagnostics.HasErrors() {
		t.Fatalf("added target should resolve import, diagnostics: %v", added.Diagnostics.Diagnostics())
	}
	if rebuilt == importer || rebuilt.AST != importer.AST || state.workspace.parsedFiles != 1 {
		t.Fatalf("importer after addition: same module %t, reused syntax %t, workspace parses %d; want rebuilt semantics with retained syntax", rebuilt == importer, rebuilt.AST == importer.AST, state.workspace.parsedFiles)
	}
	if len(rebuilt.Imports) != 1 {
		t.Fatalf("resolved imports after addition = %d, want 1", len(rebuilt.Imports))
	}

	if err := os.Remove(utilFile); err != nil {
		t.Fatalf("remove target: %v", err)
	}
	removed, rebuiltAgain := state.recompile(mainFile)
	if removed == nil || rebuiltAgain == nil || !removed.Diagnostics.HasErrors() {
		t.Fatal("removed target should restore import diagnostics")
	}
	if rebuiltAgain == rebuilt || len(rebuiltAgain.Imports) != 0 {
		t.Fatalf("importer after removal: same module %t, resolved imports %d; want rebuilt unresolved importer", rebuiltAgain == rebuilt, len(rebuiltAgain.Imports))
	}
}

func TestServerStateKeepsWorkspaceIndexAcrossRecompile(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() { helper(); }\n")
	writeWorkspaceFile(t, fileUtil, "fn helper() {}\n")

	state := NewServerState()
	state.RootDir = root
	if _, mod := state.recompile(fileMain); mod == nil {
		t.Fatalf("initial compile returned nil module")
	}

	workspace := state.workspace
	if workspace == nil {
		t.Fatalf("expected workspace index")
	}

	updated := "fn helper() { let body_only = 1; }\n"
	state.applyDocumentSnapshot(fileUtil, &updated, nil)
	if _, mod := state.recompile(fileUtil); mod == nil {
		t.Fatalf("incremental compile returned nil module")
	}
	if state.workspace != workspace {
		t.Fatalf("expected long-lived workspace index reuse")
	}
}

func TestRecompileUsesEmptyDocumentOverlay(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	entry := filepath.Join(root, peeper.SourceDirName, "main"+peeper.SourceExt)
	disk := "fn DiskOnly() -> i32 { return 7; }\n"
	writeWorkspaceFile(t, entry, disk)

	state := NewServerState()
	state.RootDir = root
	empty := ""
	state.applyDocumentSnapshot(entry, &empty, nil)
	_, mod := state.recompile(entry)
	if mod == nil {
		t.Fatalf("empty overlay compile returned nil module")
	}
	if mod.ContentHash != fingerprint.Text("") {
		t.Fatalf("empty overlay hash = %q, want empty source hash", mod.ContentHash)
	}

	state.applyDocumentSnapshot(entry, nil, nil)
	_, mod = state.recompile(entry)
	if mod == nil {
		t.Fatalf("disk compile returned nil module")
	}
	if mod.ContentHash != fingerprint.Text(disk) {
		t.Fatalf("closed overlay hash = %q, want disk source hash", mod.ContentHash)
	}
}

func TestNavigationRangesUseUTF16AfterNonBMPText(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	entry := filepath.Join(root, peeper.SourceDirName, "main"+peeper.SourceExt)
	marked := "fn main() -> i32 { let text: cstr = \"🙂\"; let x = 1; return " + hoverMarker + "x; }\n"
	content, position := markerPosition(t, marked)
	writeWorkspaceFile(t, entry, content)

	state := NewServerState()
	state.RootDir = root
	state.applyDocumentSnapshot(entry, &content, nil)
	if _, mod := state.recompile(entry); mod == nil {
		t.Fatalf("expected compiled module")
	}

	definition, err := state.HandleDefinition(DefinitionParams{TextDocumentPositionParams: TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(entry))},
		Position:     position,
	}})
	if err != nil || len(definition) != 1 {
		t.Fatalf("definition = %#v, err = %v", definition, err)
	}
	start, startOK := offsetAtPosition(content, definition[0].Range.Start)
	end, endOK := offsetAtPosition(content, definition[0].Range.End)
	if !startOK || !endOK || content[start:end] != "x" {
		t.Fatalf("definition range maps to %q, want x", content[start:end])
	}

	edit, err := state.HandleRename(RenameParams{
		TextDocument: TextDocumentIdentifier{URI: DocumentURI(pathToURI(entry))},
		Position:     position,
		NewName:      "renamed",
	})
	if err != nil || edit == nil {
		t.Fatalf("rename = %#v, err = %v", edit, err)
	}
	for _, textEdit := range edit.Changes[DocumentURI(pathToURI(entry))] {
		start, startOK := offsetAtPosition(content, textEdit.Range.Start)
		end, endOK := offsetAtPosition(content, textEdit.Range.End)
		if !startOK || !endOK || content[start:end] != "x" {
			t.Fatalf("rename range maps to %q, want x", content[start:end])
		}
	}
}

func writeWorkspaceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeWorkspaceProjectConfig(t *testing.T, root string, name string) {
	t.Helper()
	writeWorkspaceFile(t, filepath.Join(root, manifest.FileName), "name = \""+name+"\"\nbuild = \"program\"\n")
}

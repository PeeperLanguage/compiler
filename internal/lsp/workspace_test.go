package lsp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
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
	if err := index.rebuild(nil); err != nil {
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
	if err := index.rebuild(nil); err != nil {
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

	state.Cache[fileAUtil] = "fn helper() { let x = 1; }\n"
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

	state.Cache[entry] = `import "app/container";
fn Take(value: container::Box<i32>) {}
fn main() { let body_only = 1; }`
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
	if mod.HIR != nil || mod.MIR != nil || mod.LLVMIR != "" {
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

	state.Cache[project.CanonicalPath(entry)] = badSource
	incremental, mod := state.recompile(entry)
	if mod == nil || incremental == nil || !incremental.Diagnostics.HasErrors() {
		t.Fatal("incremental compile missing semantic error")
	}
	if hasCode(incremental, diagnostics.WarnUnusedPrivateFunction) {
		t.Fatal("incremental compile replayed usage warning before project Usage barrier")
	}

	freshState := NewServerState()
	freshState.RootDir = root
	freshState.Cache[project.CanonicalPath(entry)] = badSource
	fresh, freshMod := freshState.recompile(entry)
	if freshMod == nil || fresh == nil || !fresh.Diagnostics.HasErrors() {
		t.Fatal("fresh compile missing semantic error")
	}
	if hasCode(fresh, diagnostics.WarnUnusedPrivateFunction) {
		t.Fatal("fresh compile unexpectedly produced usage warning")
	}

	state.Cache[project.CanonicalPath(entry)] = goodSource
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
	if err := index.rebuild(nil); err != nil {
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

	state.Cache[fileUtil] = "fn helper() { let x = 1; }\n"
	if _, mod := state.recompile(fileUtil); mod == nil {
		t.Fatalf("recompile returned nil module")
	}

	after := state.modules[project.CanonicalPath(fileMain)]
	if before != after {
		t.Fatalf("expected dependent module reuse when export shape unchanged")
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

	state.Cache[fileUtil] = "fn helper(v: i32) {}\n"
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
	beforeSyntaxFingerprint := beforeUtil.ExportFingerprint
	beforeFingerprint := beforeUtil.SemanticExportFingerprint
	beforeType, found := beforeUtil.ModuleScope.Lookup("Value")
	if !found || beforeType == nil || beforeType.Type == nil {
		t.Fatal("missing cached exported const type")
	}
	state.Cache[fileUtil] = "const Value = 1i64;\n"
	if _, mod := state.recompile(fileUtil); mod == nil {
		t.Fatalf("recompile returned nil module")
	}

	afterUtil := state.modules[project.CanonicalPath(fileUtil)]
	if afterUtil == nil || afterUtil.ModuleScope == nil {
		t.Fatal("missing recompiled export module")
	}
	if afterUtil.ExportFingerprint != beforeSyntaxFingerprint {
		t.Fatalf("syntax fingerprint changed across inferred type edit: %q -> %q",
			beforeSyntaxFingerprint, afterUtil.ExportFingerprint)
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
	if got := project.CanonicalPath(mod.FilePath); got != project.CanonicalPath(fileUtil) {
		t.Fatalf("module path = %s, want %s", got, project.CanonicalPath(fileUtil))
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

	state.Cache[fileLeaf] = "fn leaf(v: i32) {}\n"
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

	state.Cache[fileUtil] = "fn helper(v: i32) {}\n"
	index := newWorkspaceIndex(root)
	if err := index.rebuild(state.Cache); err != nil {
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

func TestWorkspaceIndexRebuildParsesOnlyChangedFiles(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() { helper(); }\n")
	writeWorkspaceFile(t, fileUtil, "fn helper() {}\n")

	index := newWorkspaceIndex(root)
	if err := index.rebuild(nil); err != nil {
		t.Fatalf("initial rebuild: %v", err)
	}
	if got := index.parsedFiles; got != 2 {
		t.Fatalf("initial parsed files = %d, want 2", got)
	}

	mainPath := project.CanonicalPath(fileMain)
	utilPath := project.CanonicalPath(fileUtil)
	beforeMain := index.modules[mainPath]
	beforeUtilTargets := append([]string(nil), index.modules[utilPath].importTargets...)

	cache := map[string]string{
		fileUtil: "fn helper() { let body_only = 1; }\n",
	}
	if err := index.rebuild(cache); err != nil {
		t.Fatalf("incremental rebuild: %v", err)
	}
	if got := index.parsedFiles; got != 1 {
		t.Fatalf("incremental parsed files = %d, want 1", got)
	}
	if afterMain := index.modules[mainPath]; afterMain != beforeMain {
		t.Fatalf("unchanged importer should reuse cached workspace surface")
	}
	if got := index.modules[utilPath].importTargets; !slices.Equal(got, beforeUtilTargets) {
		t.Fatalf("body-only edit changed import targets: got %v want %v", got, beforeUtilTargets)
	}
}

func TestWorkspaceIndexRebuildRefreshesImportTargetsWhenNewFileAppears(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceProjectConfig(t, root, "app")
	fileMain := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
	fileUtil := filepath.Join(root, peeper.SourceDirName, "util"+peeper.SourceExt)
	writeWorkspaceFile(t, fileMain, "import \"app/util\";\nfn main() { helper(); }\n")

	index := newWorkspaceIndex(root)
	if err := index.rebuild(nil); err != nil {
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
	if err := index.rebuild(nil); err != nil {
		t.Fatalf("rebuild after adding util: %v", err)
	}
	if got := index.parsedFiles; got != 2 {
		t.Fatalf("parsed files after file membership change = %d, want 2", got)
	}

	utilPath := project.CanonicalPath(fileUtil)
	if got := index.modules[mainPath].importTargets; !slices.Equal(got, []string{utilPath}) {
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
	if err := index.rebuild(nil); err != nil {
		t.Fatalf("initial rebuild: %v", err)
	}

	mainPath := project.CanonicalPath(fileMain)
	utilPath := project.CanonicalPath(fileUtil)
	component, ok := index.componentForFile(mainPath)
	if !ok || len(component.files) != 2 {
		t.Fatalf("expected importer and util in same component, got %#v", component)
	}
	if got := index.modules[mainPath].importTargets; !slices.Equal(got, []string{utilPath}) {
		t.Fatalf("initial import targets = %v, want [%s]", got, utilPath)
	}

	if err := os.Rename(fileUtil, outsideUtil); err != nil {
		t.Fatalf("util outside src: %v", err)
	}
	if err := index.rebuild(nil); err != nil {
		t.Fatalf("rebuild after moving util: %v", err)
	}
	if got := index.parsedFiles; got != 1 {
		t.Fatalf("parsed files after util leaves src = %d, want 1", got)
	}
	if got := index.modules[mainPath].importTargets; len(got) != 0 {
		t.Fatalf("main import targets after util leaves src = %v, want empty", got)
	}
	component, ok = index.componentForFile(mainPath)
	if !ok || len(component.files) != 1 {
		t.Fatalf("expected importer to become singleton after util leaves src, got %#v", component)
	}
	if _, ok := index.modules[utilPath]; ok {
		t.Fatalf("util should be removed from workspace modules after leaving src")
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

	state.Cache[fileUtil] = "fn helper() { let body_only = 1; }\n"
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
	if mod.ContentHash != ast.HashText("") {
		t.Fatalf("empty overlay hash = %q, want empty source hash", mod.ContentHash)
	}

	state.applyDocumentSnapshot(entry, nil, nil)
	_, mod = state.recompile(entry)
	if mod == nil {
		t.Fatalf("disk compile returned nil module")
	}
	if mod.ContentHash != ast.HashText(disk) {
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
	state.Cache[entry] = content
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

package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"compiler/internal/frontend/ast"
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
	if got := phases[mainPath]; got != project.PhaseParsed {
		t.Fatalf("dependent reuse phase = %v, want %v", got, project.PhaseParsed)
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

package lsp

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/project"
)

func TestWorkspaceIndexBuildsIndependentComponents(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, filepath.Join(root, "a.peep"), "fn main() {}\n")
	writeWorkspaceFile(t, filepath.Join(root, "b.peep"), "fn main() {}\n")

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

func TestWorkspaceIndexGroupsImportedFiles(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, filepath.Join(root, "main.peep"), "import \"lib/util\";\nfn main() {}\n")
	writeWorkspaceFile(t, filepath.Join(root, "lib", "util.peep"), "fn helper() {}\n")
	writeWorkspaceFile(t, filepath.Join(root, "other.peep"), "fn main() {}\n")

	index := newWorkspaceIndex(root)
	if err := index.rebuild(nil); err != nil {
		t.Fatalf("rebuild workspace index: %v", err)
	}

	if len(index.components) != 2 {
		t.Fatalf("components = %d, want 2", len(index.components))
	}

	var foundGrouped, foundSingleton bool
	mainFile := project.CanonicalPath(filepath.Join(root, "main.peep"))
	utilFile := project.CanonicalPath(filepath.Join(root, "lib", "util.peep"))
	otherFile := project.CanonicalPath(filepath.Join(root, "other.peep"))
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
	fileA := filepath.Join(root, "a", "main.peep")
	fileAUtil := filepath.Join(root, "a", "util.peep")
	fileB := filepath.Join(root, "b.peep")
	writeWorkspaceFile(t, fileA, "import \"a/util\";\nfn main() { helper(); }\n")
	writeWorkspaceFile(t, fileAUtil, "fn helper() {}\n")
	writeWorkspaceFile(t, fileB, "fn main() {}\n")

	state := NewServerState()
	state.RootDir = root

	if _, mod := state.recompile(fileA); mod == nil {
		t.Fatalf("initial compile returned nil module")
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

func TestServerStateReusesDependentWhenExportShapeUnchanged(t *testing.T) {
	root := t.TempDir()
	fileMain := filepath.Join(root, "main.peep")
	fileUtil := filepath.Join(root, "util.peep")
	writeWorkspaceFile(t, fileMain, "import \"util\";\nfn main() { helper(); }\n")
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
	fileMain := filepath.Join(root, "main.peep")
	fileUtil := filepath.Join(root, "util.peep")
	writeWorkspaceFile(t, fileMain, "import \"util\";\nfn main() { helper(); }\n")
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
	fileMain := filepath.Join(root, "main.peep")
	fileUtil := filepath.Join(root, "util.peep")
	writeWorkspaceFile(t, fileMain, "import \"util\";\nfn main() { helper(); }\n")
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

func writeWorkspaceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

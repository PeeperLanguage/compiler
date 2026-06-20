package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"compiler/pkg/peeper"
)

func TestLoadSupportsDependencyTableSyntax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	src := `
name = "app"
version = "0.1.0"
build = "lib"

[dependencies]
json = { type = "remote", repo = "github.com/acme/json", version = "v1.2.3" }
ui = { path = "./deps/ui" }
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	file, err := Load(path)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if file.Dependencies["json"].Type != DependencyRemote || file.Dependencies["json"].Version != "v1.2.3" {
		t.Fatalf("unexpected remote dependency: %#v", file.Dependencies["json"])
	}
	if file.Dependencies["ui"].Type != DependencyNeighbor {
		t.Fatalf("unexpected neighbor dependency: %#v", file.Dependencies["ui"])
	}
}

func TestLoadSupportsConstraintSyntaxForRemoteDependency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	src := `
name = "app"
build = "lib"

[dependencies]
json = "github.com/acme/json@^0.2.0"
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	file, err := Load(path)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	dep, ok := file.Dependencies["json"]
	if !ok {
		t.Fatal("expected dependency json")
	}
	if dep.Path != "github.com/acme/json" || dep.Version != "^0.2.0" {
		t.Fatalf("unexpected dependency: %#v", dep)
	}
}

func TestLoadRejectsReservedDependencyAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	src := `
name = "app"
build = "lib"

[dependencies]
core = "./deps/core"
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("expected reserved alias error")
	}
}

func TestLoadParsesBuildType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	src := `
name = "app"
build = "program"
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	file, err := Load(path)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if file.Package.Build != BuildProgram {
		t.Fatalf("expected build to be parsed, got %q", file.Package.Build)
	}
}

func TestSaveWritesBuildType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	file := &File{
		Package: PackageInfo{
			Name:  "app",
			Build: BuildProgram,
		},
		Dependencies: map[string]Dependency{},
	}

	if err := Save(path, file); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if loaded.Package.Build != BuildProgram {
		t.Fatalf("expected build to round-trip, got %q", loaded.Package.Build)
	}
}

func TestSaveWritesExplicitLatestRemoteDependency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	file := &File{
		Package: PackageInfo{Name: "app", Build: BuildLib},
		Dependencies: map[string]Dependency{
			"json": {Type: DependencyRemote, Path: "github.com/acme/json", Version: "latest"},
		},
	}

	if err := Save(path, file); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got == "" || !strings.Contains(got, `json = "github.com/acme/json@latest"`) {
		t.Fatalf("expected explicit @latest dependency, got:\n%s", got)
	}
}

func TestLoadProjectAcceptsSourceFilePath(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", "util"+peeper.SourceExt)
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("name = \"app\"\nbuild = \"program\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("fn Helper() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	project, err := LoadProject(src)
	if err != nil {
		t.Fatalf("load project from file path: %v", err)
	}
	if project.RootDir != root {
		t.Fatalf("project root = %q, want %q", project.RootDir, root)
	}
}

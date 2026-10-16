package project

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"compiler/internal/diag"
)

// write writes files (paths relative to a temporary folder) and returns that
// folder and its packages: "myapp" in src, "core" in libs/core/src.
func write(t *testing.T, files map[string]string) (string, Packages) {
	t.Helper()
	root := t.TempDir()
	for name, text := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, Packages{"myapp": filepath.Join(root, "src"), "core": filepath.Join(root, "libs", "core", "src")}
}

// messages returns "module: CODE message" for every diagnostic, sorted.
func messages(prog *Program) []string {
	var out []string
	add := func(where string, ds []diag.Diagnostic) {
		for _, d := range ds {
			out = append(out, where+": "+d.Code.ID+" "+d.Message)
		}
	}
	add("program", prog.Diagnostics)
	for path, m := range prog.Modules {
		add(path, m.Diagnostics)
	}
	slices.Sort(out)
	return out
}

func imports(m *Module) []string {
	var out []string
	for _, imp := range m.Imports {
		out = append(out, imp.Alias+"="+imp.Path)
	}
	return out
}

func TestLoadNestedImports(t *testing.T) {
	root, packages := write(t, map[string]string{
		"src/main.peep":             "import \"myapp:a\";\nimport \"myapp:b/c\" as c2;\nfn main() {}",
		"src/a.peep":                "import \"myapp:b/c\";\nfn A() {}",
		"src/b/c.peep":              "fn C() {}",
		"libs/core/src/global.peep": "fn exit(code: i32) {}",
	})
	prog := Load(packages, filepath.Join(root, "src", "main.peep"))
	if got := messages(prog); len(got) != 0 {
		t.Fatalf("unexpected diagnostics: %q", got)
	}
	if got := strings.Join(prog.Order, " "); got != "core:global myapp:b/c myapp:a myapp:main" {
		t.Fatalf("order = %s", got)
	}
	if got := imports(prog.Modules["myapp:main"]); !slices.Equal(got, []string{"a=myapp:a", "c2=myapp:b/c", "=core:global"}) {
		t.Fatalf("main imports = %q", got)
	}
	if got := imports(prog.Modules["core:global"]); len(got) != 0 {
		t.Fatalf("the prelude imports itself: %q", got)
	}
}

func TestLoadImportErrors(t *testing.T) {
	root, packages := write(t, map[string]string{
		"src/main.peep": strings.Join([]string{
			`import "myapp:missing";`,
			`import "myapp:./x";`,
			`import "myapp:../x";`,
			`import "myapp:";`,
			`import "other:x";`,
			`import "x/y";`,
			`import "myapp:x.txt";`,
			`import "myapp:a";`,
			`import "myapp:c" as a;`,
			`import "myapp:a.peep";`, // the same module as myapp:a under the same name: kept once
			`import "myapp:b";`,
		}, "\n"),
		"src/a.peep": "",
		"src/b.peep": "fn f( {", // a syntax error belongs to b's own diagnostics
		"src/c.peep": "",
	})
	prog := Load(packages, filepath.Join(root, "src", "main.peep"))
	want := []string{
		"myapp:main: M0001 module not found: " + filepath.Join(root, "src", "missing.peep"),
		"myapp:main: M0003 import path needs a package, such as `myapp:utils/file`",
		"myapp:main: M0003 import path parts cannot be empty, `.` or `..`",
		"myapp:main: M0003 import path parts cannot be empty, `.` or `..`",
		"myapp:main: M0003 import path parts cannot be empty, `.` or `..`",
		`myapp:main: M0003 invalid import extension ".txt"`,
		"myapp:main: M0003 unknown package `other`",
		"myapp:main: M0005 import alias already in use",
	}
	var got []string
	for _, line := range messages(prog) {
		if strings.HasPrefix(line, "myapp:main: ") {
			got = append(got, line)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("main's diagnostics:\n%s", strings.Join(got, "\n"))
	}
	if b := prog.Modules["myapp:b"]; b == nil || len(b.Diagnostics) == 0 || b.Diagnostics[0].Code != diag.ExpectedToken {
		t.Error("b's syntax error is not in b's diagnostics")
	}
}

func TestLoadReportsImportsAtTheirLine(t *testing.T) {
	root, packages := write(t, map[string]string{"src/main.peep": "fn main() {}\nimport \"myapp:missing\";\n"})
	prog := Load(packages, filepath.Join(root, "src", "main.peep"))
	m := prog.Modules["myapp:main"]
	if len(m.Diagnostics) != 1 {
		t.Fatalf("diagnostics: %q", messages(prog))
	}
	label := m.Diagnostics[0].Labels[0]
	if line, column := m.Source.Position(label.Span.Start); line != 2 || column != 1 {
		t.Fatalf("label at %d:%d, want 2:1", line, column)
	}
}

func TestLoadCycle(t *testing.T) {
	root, packages := write(t, map[string]string{
		"src/main.peep": `import "myapp:a";`,
		"src/a.peep":    `import "myapp:b";`,
		"src/b.peep":    `import "myapp:a";`,
	})
	prog := Load(packages, filepath.Join(root, "src", "main.peep"))
	got := messages(prog)
	if !slices.Equal(got, []string{"program: M0002 cyclic import detected: myapp:a -> myapp:b -> myapp:a"}) {
		t.Fatalf("diagnostics: %q", got)
	}
	if label := prog.Diagnostics[0].Labels[0]; label.File != prog.Modules["myapp:b"].Source {
		t.Fatalf("cycle label is not on b's import")
	}
}

func TestLoadRedundantPreludeImport(t *testing.T) {
	root, packages := write(t, map[string]string{
		"src/main.peep":             `import "core:global";`,
		"libs/core/src/global.peep": "",
	})
	prog := Load(packages, filepath.Join(root, "src", "main.peep"))
	if got := messages(prog); !slices.Equal(got, []string{"myapp:main: S0004 `core:global` is imported automatically"}) {
		t.Fatalf("diagnostics: %q", got)
	}
	if got := imports(prog.Modules["myapp:main"]); !slices.Equal(got, []string{"global=core:global"}) {
		t.Fatalf("imports = %q (the implicit import must not repeat an explicit one)", got)
	}
}

func TestLoadSingleFile(t *testing.T) {
	root, packages := write(t, map[string]string{
		"tool.peep":                 "import \"myapp:a\";\nfn main() {}",
		"libs/core/src/global.peep": "",
	})
	prog := Load(packages, filepath.Join(root, "tool.peep"))
	if got := messages(prog); !slices.Equal(got, []string{"tool: M0003 a single file cannot import; check its project folder instead"}) {
		t.Fatalf("diagnostics: %q", got)
	}
	if got := imports(prog.Modules["tool"]); !slices.Equal(got, []string{"=core:global"}) {
		t.Fatalf("imports = %q (the prelude still comes in)", got)
	}
}

func TestLoadUnreadableEntry(t *testing.T) {
	root, packages := write(t, nil)
	prog := Load(packages, filepath.Join(root, "src", "main.peep"))
	got := messages(prog)
	if len(got) != 1 || !strings.HasPrefix(got[0], "myapp:main: M0001 read module: ") {
		t.Fatalf("diagnostics: %q", got)
	}
}

// TestLoadManyModules loads a wide, deep project. Run with -race; the order
// must be the same every time.
func TestLoadManyModules(t *testing.T) {
	files := map[string]string{"libs/core/src/global.peep": ""}
	var main strings.Builder
	for i := range 200 {
		fmt.Fprintf(&main, "import \"myapp:m%d\";\n", i)
		var m strings.Builder
		for j := i + 1; j < min(i+4, 200); j++ {
			fmt.Fprintf(&m, "import \"myapp:m%d\";\n", j)
		}
		m.WriteString("fn f() {}\n")
		files[fmt.Sprintf("src/m%d.peep", i)] = m.String()
	}
	files["src/main.peep"] = main.String()
	root, packages := write(t, files)
	entry := filepath.Join(root, "src", "main.peep")
	first := Load(packages, entry)
	if got := messages(first); len(got) != 0 || len(first.Modules) != 202 {
		t.Fatalf("%d modules, diagnostics: %q", len(first.Modules), got)
	}
	position := map[string]int{}
	for i, path := range first.Order {
		position[path] = i
	}
	for path, m := range first.Modules {
		for _, imp := range m.Imports {
			if position[imp.Path] >= position[path] {
				t.Fatalf("%s comes before its import %s", path, imp.Path)
			}
		}
	}
	for range 5 {
		if again := Load(packages, entry); !slices.Equal(again.Order, first.Order) {
			t.Fatal("order differs between runs")
		}
	}
}

func TestPathOf(t *testing.T) {
	root, packages := write(t, nil)
	if path, inPackage := packages.pathOf(filepath.Join(root, "src", "deep", "x.peep")); path != "myapp:deep/x" || !inPackage {
		t.Fatalf("pathOf in a package = %s, %v", path, inPackage)
	}
	if path, inPackage := packages.pathOf(filepath.Join(root, "tool.peep")); path != "tool" || inPackage {
		t.Fatalf("pathOf outside every package = %s, %v", path, inPackage)
	}
}

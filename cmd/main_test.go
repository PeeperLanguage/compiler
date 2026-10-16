package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckProject(t *testing.T) {
	root := t.TempDir()
	libs := filepath.Join(root, "libs")
	write(t, root, map[string]string{
		"app/peeper.toml":           `name = "myapp"`,
		"app/src/main.peep":         "import \"myapp:util\";\nimport \"myapp:gone\";\nfn main() {}",
		"app/src/util.peep":         "fn Util() {}",
		"libs/core/src/global.peep": "",
	})
	var out strings.Builder
	if code := check(filepath.Join(root, "app"), libs, &out, false); code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	want := "[M0001]: module not found: " + filepath.Join(root, "app", "src", "gone.peep") + "\n" +
		"  --> " + filepath.Join(root, "app", "src", "main.peep") + ":2:1\n"
	if !strings.HasPrefix(out.String(), want) || !strings.HasSuffix(out.String(), "Compilation failed with 1 error(s)\n") {
		t.Fatalf("output:\n%s", out.String())
	}

	write(t, root, map[string]string{"app/src/main.peep": "import \"myapp:util\";\nfn main() {}"})
	out.Reset()
	if code := check(filepath.Join(root, "app"), libs, &out, false); code != 0 || out.Len() != 0 {
		t.Fatalf("exit code %d, output:\n%s", code, out.String())
	}
}

func TestCheckSingleFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"tool.peep": "fn main() {}"}) // no peeper.toml needed
	var out strings.Builder
	if code := check(filepath.Join(root, "tool.peep"), "", &out, false); code != 0 || out.Len() != 0 {
		t.Fatalf("exit code %d, output:\n%s", code, out.String())
	}
}

func TestCheckFolderErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"core/peeper.toml":     `name = "core"`,
		"libs/core/src/x.peep": "",
	})
	for _, test := range []struct{ folder, want string }{
		{t.TempDir(), "no peeper.toml in this folder"},
		{filepath.Join(root, "core"), "project name `core` is taken by a bundled library"},
	} {
		var out strings.Builder
		if code := check(test.folder, filepath.Join(root, "libs"), &out, false); code != 1 || !strings.Contains(out.String(), test.want) {
			t.Errorf("%s: exit code %d, output: %s", test.folder, code, out.String())
		}
	}
}

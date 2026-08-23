package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

func buildTestCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "peeper")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	return binary
}

func TestInitCommandCreatesRunnableNormalizedProject(t *testing.T) {
	binary := buildTestCLI(t)

	t.Run("arity failure creates nothing", func(t *testing.T) {
		root := t.TempDir()
		command := exec.Command(binary, "init", "one", "two")
		command.Dir = root
		if output, err := command.CombinedOutput(); err == nil {
			t.Fatalf("init with two names succeeded:\n%s", output)
		}
		for _, path := range []string{manifest.FileName, peeper.SourceDirName} {
			if _, err := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(err) {
				t.Fatalf("invalid arity created %s: %v", path, err)
			}
		}
	})

	t.Run("normalized project runs", func(t *testing.T) {
		root := t.TempDir()
		command := exec.Command(binary, "init", "hello-peeper")
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("init failed: %v\n%s", err, output)
		}

		file, err := manifest.Load(filepath.Join(root, manifest.FileName))
		if err != nil {
			t.Fatalf("load generated manifest: %v", err)
		}
		if file.Package.Name != "hello_peeper" {
			t.Fatalf("generated package name = %q, want hello_peeper", file.Package.Name)
		}
		manifestContent, err := os.ReadFile(filepath.Join(root, manifest.FileName))
		if err != nil {
			t.Fatalf("read generated manifest: %v", err)
		}
		if !strings.Contains(string(manifestContent), "[dependencies]") {
			t.Fatalf("generated manifest missing dependencies section:\n%s", manifestContent)
		}
		mainPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)
		starter, err := os.ReadFile(mainPath)
		if err != nil {
			t.Fatalf("read generated starter: %v", err)
		}
		if !strings.Contains(string(starter), `println("Hello from Peeper!");`) {
			t.Fatalf("generated starter missing semicolon:\n%s", starter)
		}

		command = exec.Command(binary, "run")
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("run generated project: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "Hello from Peeper!") {
			t.Fatalf("generated project output:\n%s", output)
		}
	})
}

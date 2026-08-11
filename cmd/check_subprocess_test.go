package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"compiler/pkg/peeper"
)

func TestCheckCommandSupportsRecursiveAndMultipleTargetsWithFailureStatus(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "peeper")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	validDir := filepath.Join(root, "valid")
	invalidOneDir := filepath.Join(root, "invalid-one")
	invalidTwoDir := filepath.Join(root, "invalid-two")
	for _, dir := range []string{filepath.Join(validDir, "nested"), invalidOneDir, invalidTwoDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, source string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(validDir, "valid"+peeper.SourceExt), "fn Valid() {}\n")
	write(filepath.Join(validDir, "nested", "more"+peeper.SourceExt), "fn More() {}\n")
	invalidOne := filepath.Join(invalidOneDir, "first"+peeper.SourceExt)
	invalidTwo := filepath.Join(invalidTwoDir, "second"+peeper.SourceExt)
	write(invalidOne, "fn First() { missing; }\n")
	write(invalidTwo, "fn Second() -> i32 { return true; }\n")

	if output, err := exec.Command(binary, "check", validDir).CombinedOutput(); err != nil {
		t.Fatalf("recursive valid check failed: %v\n%s", err, output)
	}
	output, err := exec.Command(binary, "check", validDir, invalidOneDir, invalidTwo).CombinedOutput()
	if err == nil {
		t.Fatalf("mixed check succeeded:\n%s", output)
	}
	text := string(output)
	if !strings.Contains(text, filepath.Base(invalidOne)) || !strings.Contains(text, filepath.Base(invalidTwo)) {
		t.Fatalf("mixed check did not report every failed group:\n%s", text)
	}
}

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

func TestParseCommandArgsRunDebug(t *testing.T) {
	opts, err := parseCommandArgs("run", []string{"--debug", "demo" + peeper.SourceExt}, true)
	if err != nil {
		t.Fatalf("parse command args: %v", err)
	}
	if !opts.debugBuild {
		t.Fatal("expected debug build flag")
	}
	if len(opts.positional) != 1 || opts.positional[0] != "demo"+peeper.SourceExt {
		t.Fatalf("positional = %#v", opts.positional)
	}
}

func TestRunCommandReturnsProgramStatusAfterCleanup(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "exit"+peeper.SourceExt)
	if err := os.WriteFile(sourcePath, []byte("fn main() -> i32 { return 10; }\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	err := runCommand([]string{sourcePath})
	var status programExitStatus
	if !errors.As(err, &status) || status != 10 {
		t.Fatalf("runCommand error = %v, want program status 10", err)
	}
	entries, readErr := os.ReadDir(tempDir)
	if readErr != nil {
		t.Fatalf("read temp directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("runCommand leaked temporary files: %v", entries)
	}
}

func TestParseCommandArgsRejectsConflictingM32TargetArch(t *testing.T) {
	_, err := parseCommandArgs("build", []string{"--m32", "--target-arch", "amd64"}, false)
	if err == nil {
		t.Fatal("expected -m32 and amd64 conflict")
	}
}

func TestResolveBuildTargetUsesManifestEntryAndPackageName(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, manifest.FileName)
	entryPath := filepath.Join(root, peeper.SourceDirName, peeper.MainFileName)

	if err := os.MkdirAll(filepath.Dir(entryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := `name = "sample_app"
build = "program"
`
	if err := os.WriteFile(manifestPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	resolvedPath, info, err := resolveBuildTarget("build", root, "linux")
	if err != nil {
		t.Fatalf("resolve build target: %v", err)
	}
	if resolvedPath != entryPath {
		t.Fatalf("resolved path = %q, want %q", resolvedPath, entryPath)
	}
	if !info.SelectedByDiscovery {
		t.Fatal("expected manifest-based discovery")
	}
	if info.DefaultOutputPath != "sample_app" {
		t.Fatalf("default output = %q, want sample_app", info.DefaultOutputPath)
	}
}

func TestResolveBuildTargetUsesFileStemWithoutManifest(t *testing.T) {
	root := t.TempDir()
	entryPath := filepath.Join(root, "demo"+peeper.SourceExt)
	if err := os.WriteFile(entryPath, []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolvedPath, info, err := resolveBuildTarget("build", entryPath, "linux")
	if err != nil {
		t.Fatalf("resolve build target: %v", err)
	}
	if resolvedPath != entryPath {
		t.Fatalf("resolved path = %q, want %q", resolvedPath, entryPath)
	}
	if info.SelectedByDiscovery {
		t.Fatal("did not expect manifest-based discovery")
	}
	if info.DefaultOutputPath != "demo" {
		t.Fatalf("default output = %q, want demo", info.DefaultOutputPath)
	}
}

func TestResolveBuildTargetRejectsConfiguredFileOutsideSrc(t *testing.T) {
	root := t.TempDir()
	entryPath := filepath.Join(root, "demo"+peeper.SourceExt)
	if err := os.WriteFile(entryPath, []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := `name = "sample_app"
build = "program"
`
	if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := resolveBuildTarget("build", entryPath, "linux"); err == nil {
		t.Fatal("expected source-root error")
	}
}

func TestResolveBuildTargetAppendsWindowsSuffix(t *testing.T) {
	root := t.TempDir()
	entryPath := filepath.Join(root, "demo"+peeper.SourceExt)
	if err := os.WriteFile(entryPath, []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, info, err := resolveBuildTarget("build", entryPath, "windows")
	if err != nil {
		t.Fatalf("resolve build target: %v", err)
	}
	if info.DefaultOutputPath != "demo.exe" {
		t.Fatalf("default output = %q, want demo.exe", info.DefaultOutputPath)
	}
}

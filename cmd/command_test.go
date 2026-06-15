package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCommandArgsRunDebug(t *testing.T) {
	opts, err := parseCommandArgs("run", []string{"--debug", "demo.peep"}, true)
	if err != nil {
		t.Fatalf("parse command args: %v", err)
	}
	if !opts.debugBuild {
		t.Fatal("expected debug build flag")
	}
	if len(opts.positional) != 1 || opts.positional[0] != "demo.peep" {
		t.Fatalf("positional = %#v", opts.positional)
	}
}

func TestResolveBuildTargetUsesManifestEntryAndPackageName(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "peeper")
	entryPath := filepath.Join(root, "src", "main.peep")

	if err := os.MkdirAll(filepath.Dir(entryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := `[package]
name = "sample_app"
entry = "src/main"
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
	entryPath := filepath.Join(root, "demo.peep")
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

func TestResolveBuildTargetAppendsWindowsSuffix(t *testing.T) {
	root := t.TempDir()
	entryPath := filepath.Join(root, "demo.peep")
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

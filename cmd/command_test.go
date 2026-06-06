package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBuildTargetUsesManifestEntryAndPackageName(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "ember")
	entryPath := filepath.Join(root, "src", "main.em")

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

	resolvedPath, info, err := resolveBuildTarget("build", root)
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
	entryPath := filepath.Join(root, "demo.em")
	if err := os.WriteFile(entryPath, []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolvedPath, info, err := resolveBuildTarget("build", entryPath)
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

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateNativeLinkTarget(t *testing.T) {
	if err := validateNativeLinkTarget(runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("host target rejected: %v", err)
	}

	targetOS := "linux"
	if runtime.GOOS == targetOS {
		targetOS = "windows"
	}
	if err := validateNativeLinkTarget(targetOS, runtime.GOARCH); err == nil {
		t.Fatalf("non-host target %s/%s accepted", targetOS, runtime.GOARCH)
	}
}

func TestReplacePathReplacesExistingFile(t *testing.T) {
	root := t.TempDir()
	staged := filepath.Join(root, "staged")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replacePath(staged, target); err != nil {
		t.Fatalf("replacePath() error = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("replaced file = %q", data)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged path still exists: %v", err)
	}
}

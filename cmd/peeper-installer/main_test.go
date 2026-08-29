package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultInstallRootIsUserScoped(t *testing.T) {
	root, err := defaultInstallRoot()
	if err != nil {
		t.Fatalf("defaultInstallRoot() error = %v", err)
	}
	if !filepath.IsAbs(root) || filepath.Dir(root) == root {
		t.Fatalf("defaultInstallRoot() = %q", root)
	}
	if runtime.GOOS != "windows" && filepath.Base(root) != ".peeper" {
		t.Fatalf("defaultInstallRoot() = %q", root)
	}
}

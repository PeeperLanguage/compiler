package main

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/internal/moduleid"
	"compiler/internal/project"
)

func TestSaveIRsKeepsSameBasenameModulesDistinctAndReplacesOldTree(t *testing.T) {
	ctx := project.NewWithConfig(project.Config{RootDir: t.TempDir()}, nil)
	ctx.AddModule(&project.Module{ID: moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "app/one/common"}, FilePath: "/one/common.peep", LLVMIR: "one"})
	ctx.AddModule(&project.Module{ID: moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "app/two/common"}, FilePath: "/two/common.peep", LLVMIR: "two"})
	target := filepath.Join(t.TempDir(), "_gen")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "stale.ll"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := saveIRs(ctx, target); err != nil {
		t.Fatalf("saveIRs: %v", err)
	}
	for _, artifact := range []string{
		filepath.Join("local", "app", "one", "common.ll"),
		filepath.Join("local", "app", "two", "common.ll"),
	} {
		if _, err := os.Stat(filepath.Join(target, artifact)); err != nil {
			t.Fatalf("missing distinct artifact %s: %v", artifact, err)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "stale.ll")); !os.IsNotExist(err) {
		t.Fatalf("stale artifact survived replacement: %v", err)
	}
}

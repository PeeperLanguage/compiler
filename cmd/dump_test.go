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
		filepath.Join("local", "_", "_", "app", "one", "common.ll"),
		filepath.Join("local", "_", "_", "app", "two", "common.ll"),
	} {
		if _, err := os.Stat(filepath.Join(target, artifact)); err != nil {
			t.Fatalf("missing distinct artifact %s: %v", artifact, err)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "stale.ll")); !os.IsNotExist(err) {
		t.Fatalf("stale artifact survived replacement: %v", err)
	}
}

func TestSaveIRsSeparatesIdentitiesDifferingOnlyByNamespace(t *testing.T) {
	// Namespace and dependency are canonical identity components. Artifacts that
	// ignored them collided, and the surviving file depended on map order.
	ctx := project.NewWithConfig(project.Config{RootDir: t.TempDir()}, nil)
	ctx.AddModule(&project.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginStdlib), Namespace: "core", ImportPath: "json"},
		FilePath: "/core/json.peep", LLVMIR: "core",
	})
	ctx.AddModule(&project.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginStdlib), Namespace: "vendor", ImportPath: "json"},
		FilePath: "/vendor/json.peep", LLVMIR: "vendor",
	})
	target := filepath.Join(t.TempDir(), "_gen")
	if err := saveIRs(ctx, target); err != nil {
		t.Fatalf("saveIRs: %v", err)
	}
	for namespace, want := range map[string]string{"core": "core", "vendor": "vendor"} {
		path := filepath.Join(target, string(project.ModuleOriginStdlib), namespace, "_", "json.ll")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing artifact for namespace %s: %v", namespace, err)
		}
		if string(content) != want {
			t.Fatalf("artifact %s = %q, want %q", path, content, want)
		}
	}
}

func TestSaveIRsSeparatesIdentitiesDifferingOnlyByDependency(t *testing.T) {
	ctx := project.NewWithConfig(project.Config{RootDir: t.TempDir()}, nil)
	ctx.AddModule(&project.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginDependency), Namespace: "vendor", Dependency: "left", ImportPath: "util"},
		FilePath: "/left/util.peep", LLVMIR: "left",
	})
	ctx.AddModule(&project.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginDependency), Namespace: "vendor", Dependency: "right", ImportPath: "util"},
		FilePath: "/right/util.peep", LLVMIR: "right",
	})
	target := filepath.Join(t.TempDir(), "_gen")
	if err := saveIRs(ctx, target); err != nil {
		t.Fatalf("saveIRs: %v", err)
	}
	for _, dependency := range []string{"left", "right"} {
		path := filepath.Join(target, string(project.ModuleOriginDependency), "vendor", dependency, "util.ll")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing artifact for dependency %s: %v", dependency, err)
		}
		if string(content) != dependency {
			t.Fatalf("artifact %s = %q, want %q", path, content, dependency)
		}
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestModuleArtifactBaseEncodesIdentityComponentsInjectively(t *testing.T) {
	stage := t.TempDir()
	cases := []struct {
		name   string
		id     moduleid.ID
		want   string
		within bool
	}{
		{
			name: "empty and underscore namespace are distinct",
			id:   moduleid.ID{Origin: string(project.ModuleOriginLocal), Namespace: "_", ImportPath: "app"},
			want: filepath.Join(stage, "local", "%5F", "_", "app"),
		},
		{
			name:   "dotdot dependency is confined",
			id:     moduleid.ID{Origin: string(project.ModuleOriginLocal), Dependency: "..", ImportPath: "app"},
			want:   filepath.Join(stage, "local", "_", "%2E.", "app"),
			within: true,
		},
		{
			name:   "dotdot origin is confined",
			id:     moduleid.ID{Origin: "..", ImportPath: "app"},
			want:   filepath.Join(stage, "%2E.", "_", "_", "app"),
			within: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := moduleArtifactBase(stage, &project.Module{ID: tc.id})
			if err != nil {
				t.Fatalf("moduleArtifactBase: %v", err)
			}
			if got != tc.want {
				t.Fatalf("moduleArtifactBase = %q, want %q", got, tc.want)
			}
			if tc.within {
				rel, err := filepath.Rel(stage, got)
				if err != nil || strings.HasPrefix(rel, "..") {
					t.Fatalf("artifact %q escapes stage %q", got, stage)
				}
			}
		})
	}

	empty, err := moduleArtifactBase(stage, &project.Module{ID: moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "app"}})
	if err != nil {
		t.Fatalf("moduleArtifactBase: %v", err)
	}
	underscore, err := moduleArtifactBase(stage, &project.Module{ID: moduleid.ID{Origin: string(project.ModuleOriginLocal), Namespace: "_", ImportPath: "app"}})
	if err != nil {
		t.Fatalf("moduleArtifactBase: %v", err)
	}
	if empty == underscore {
		t.Fatalf("empty and underscore namespaces share artifact path %q", empty)
	}
	dotdot, err := moduleArtifactBase(stage, &project.Module{ID: moduleid.ID{Origin: string(project.ModuleOriginLocal), Dependency: "..", ImportPath: "app"}})
	if err != nil {
		t.Fatalf("moduleArtifactBase: %v", err)
	}
	if dotdot == empty {
		t.Fatalf("dotdot and empty dependencies share artifact path %q", dotdot)
	}

	colon, err := moduleArtifactBase(stage, &project.Module{ID: moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "a:b"}})
	if err != nil {
		t.Fatalf("moduleArtifactBase: %v", err)
	}
	slash, err := moduleArtifactBase(stage, &project.Module{ID: moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "a/b"}})
	if err != nil {
		t.Fatalf("moduleArtifactBase: %v", err)
	}
	if colon == slash {
		t.Fatalf("import paths a:b and a/b share artifact path %q", colon)
	}
}

func TestSaveIRsWritesEncodedIdentityArtifacts(t *testing.T) {
	ctx := project.NewWithConfig(project.Config{RootDir: t.TempDir()}, nil)
	ctx.AddModule(&project.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), Namespace: "_", ImportPath: "a:b"},
		FilePath: "/colon.peep", LLVMIR: "colon",
	})
	ctx.AddModule(&project.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "a/b"},
		FilePath: "/slash.peep", LLVMIR: "slash",
	})
	target := filepath.Join(t.TempDir(), "_gen")
	if err := saveIRs(ctx, target); err != nil {
		t.Fatalf("saveIRs: %v", err)
	}
	for _, artifact := range []string{
		filepath.Join("local", "%5F", "_", "a%3Ab.ll"),
		filepath.Join("local", "_", "_", "a", "b.ll"),
	} {
		if _, err := os.Stat(filepath.Join(target, artifact)); err != nil {
			t.Fatalf("missing encoded artifact %s: %v", artifact, err)
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

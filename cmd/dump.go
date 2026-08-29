package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"compiler/internal/project"
)

// Write -keep-gen artifacts for each module.
func saveIRs(ctx *project.CompilerContext, dir string) error {
	if ctx == nil {
		return fmt.Errorf("missing compiler context")
	}
	target, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve artifact directory: %w", err)
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(target)+"-stage-")
	if err != nil {
		return fmt.Errorf("create artifact staging tree: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	for _, module := range ctx.Modules() {
		if module == nil {
			continue
		}
		base, err := moduleArtifactBase(stage, module)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
			return err
		}
		hirText := ""
		if module.HIR != nil {
			hirText = module.HIR.Text()
		}
		if err := os.WriteFile(base+".hir", []byte(hirText), 0o644); err != nil {
			return err
		}
		mirText := ""
		if module.MIR != nil {
			mirText = module.MIR.Text()
		}
		if err := os.WriteFile(base+".mir", []byte(mirText), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(base+".ll", []byte(module.LLVMIR), 0o644); err != nil {
			return err
		}
	}
	return replacePath(stage, target)
}

func moduleArtifactBase(stage string, module *project.Module) (string, error) {
	origin := string(module.Origin)
	if origin == "" {
		origin = string(project.ModuleOriginLocal)
	}
	identity := strings.TrimSpace(module.ImportPath)
	if identity == "" {
		return "", fmt.Errorf("module %q has no import identity", module.FilePath)
	}
	identity = filepath.Clean(filepath.FromSlash(strings.ReplaceAll(identity, ":", "/")))
	if identity == "." || filepath.IsAbs(identity) || identity == ".." || strings.HasPrefix(identity, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid module import identity %q", module.ImportPath)
	}
	return filepath.Join(stage, origin, identity), nil
}

func replacePath(stage, target string) error {
	backup := ""
	if _, err := os.Stat(target); err == nil {
		reserved, err := os.MkdirTemp(filepath.Dir(target), "."+filepath.Base(target)+"-backup-")
		if err != nil {
			return fmt.Errorf("reserve destination backup: %w", err)
		}
		if err := os.Remove(reserved); err != nil {
			return fmt.Errorf("prepare destination backup: %w", err)
		}
		if err := os.Rename(target, reserved); err != nil {
			return fmt.Errorf("backup destination: %w", err)
		}
		backup = reserved
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect destination: %w", err)
	}
	if err := os.Rename(stage, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return fmt.Errorf("publish staged path: %w", err)
	}
	if backup != "" {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove replaced destination: %w", err)
		}
	}
	return nil
}

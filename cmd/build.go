package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"compiler/pkg/manifest"
	"compiler/pkg/diagnostics"
	"compiler/internal/backend"
	"compiler/internal/context"
	compiler "compiler/internal/driver"
)

// Compile one entry file with a fresh compiler context.
func compileEntry(path, backendName string, debugBuild bool) (*context.CompilerContext, *context.Module) {
	cfg := buildCompilerConfig(path, backendName, debugBuild)
	ctx := compiler.NewContext(cfg, diagnostics.NewDiagnosticBag(path))
	entry := compiler.ParseFile(ctx, path)
	return ctx, entry
}

// Convert CLI inputs to compiler config.
func buildCompilerConfig(path, backendName string, debugBuild bool) context.Config {
	rootDir := path
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		rootDir = filepath.Dir(path)
	}
	if manifestPath, err := manifest.Find(rootDir); err == nil {
		rootDir = filepath.Dir(manifestPath)
	}
	return context.Config{
		RootDir:       rootDir,
		Extension:     compiler.SOURCE_EXT,
		TargetBackend: backendName,
		BuildDebug:    debugBuild,
	}
}

// Build final output after successful compilation.
func buildExecutable(ctx *context.CompilerContext, entry *context.Module, outputPath string, target backend.BACKEND_TYPE) error {
	if ctx != nil && ctx.Diagnostics != nil && ctx.Diagnostics.HasErrors() {
		return fmt.Errorf("cannot build with existing diagnostics errors")
	}
	if entry == nil {
		return fmt.Errorf("no entry module produced")
	}
	if target != backend.LLVM {
		return fmt.Errorf("unsupported backend: %s", target)
	}

	modules := ctx.Modules()
	if len(modules) == 0 {
		return fmt.Errorf("no modules compiled")
	}

	llDir, err := os.MkdirTemp("", "ember-ll-")
	if err != nil {
		return fmt.Errorf("create llvm temp dir: %w", err)
	}
	defer os.RemoveAll(llDir)

	llPaths := make([]string, 0, len(modules))
	for i, module := range modules {
		if module == nil {
			continue
		}
		ir := strings.TrimSpace(module.LLVMIR)
		if ir == "" {
			return fmt.Errorf("empty LLVM IR for module %s", module.ImportPath)
		}
		llPath := filepath.Join(llDir, fmt.Sprintf("mod_%d.ll", i))
		if err := os.WriteFile(llPath, []byte(ir), 0o644); err != nil {
			return fmt.Errorf("write llvm ir: %w", err)
		}
		llPaths = append(llPaths, llPath)
	}
	if len(llPaths) == 0 {
		return fmt.Errorf("no LLVM IR emitted")
	}

	clangPath, err := exec.LookPath("clang")
	if err != nil {
		return fmt.Errorf("clang not found in PATH; install LLVM clang to build LLVM IR")
	}
	args := make([]string, 0, len(llPaths)*3+2)
	for _, llPath := range llPaths {
		args = append(args, "-x", "ir", llPath)
	}
	args = append(args, "-o", outputPath)
	cmd := exec.Command(clangPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clang link failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

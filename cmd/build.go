package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/driver"
	"compiler/internal/phase"
	"compiler/internal/project"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

// Compile one entry file with a fresh compiler project.
func compileEntry(path string, debugBuild bool, targetOS, targetArch string) (compilerContext *project.CompilerContext, program *project.Module) {
	sourceProject, err := manifest.ResolveSourceFileProject(path)
	rootDir := sourceProject.RootDir
	projectName := sourceProject.ProjectName
	cfg := project.Config{
		RootDir:     rootDir,
		ProjectName: projectName,
		Extension:   peeper.SourceExt,
		TargetOS:    targetOS,
		TargetArch:  targetArch,
		BuildDebug:  debugBuild,
	}
	compilerContext = compiler.NewCompilerContext(cfg, diagnostics.NewDiagnosticBag())
	if err != nil {
		compilerContext.Diagnostics.BeginPhase(phase.Load, "").Add(diagnostics.NewError(
			err.Error(),
		))
		return compilerContext, nil
	}
	program = compiler.CompileFile(compilerContext, path, nil)
	return compilerContext, program
}

// Build final output after successful compilation.
func buildExecutable(ctx *project.CompilerContext, entry *project.Module, outputPath string) error {
	if ctx != nil && ctx.Diagnostics != nil && ctx.Diagnostics.HasErrors() {
		return fmt.Errorf("cannot build with existing diagnostics errors")
	}
	if entry == nil {
		return fmt.Errorf("no entry module produced")
	}
	modules := ctx.Modules()
	if len(modules) == 0 {
		return fmt.Errorf("no modules compiled")
	}

	llDir, err := os.MkdirTemp("", "peeper-ll-")
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
	if !ctx.Target.Valid() {
		return fmt.Errorf("compiler target is unavailable")
	}

	clangPath, err := exec.LookPath("clang")
	if err != nil {
		return fmt.Errorf("clang not found in PATH; install LLVM clang to build LLVM IR")
	}
	args := clangArgsForBuild(ctx.Config, ctx.Target.LLVMTriple, llPaths, outputPath)
	cmd := exec.Command(clangPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clang link failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func clangArgsForBuild(cfg project.Config, targetTriple string, llPaths []string, outputPath string) []string {
	args := make([]string, 0, len(llPaths)*3+6)
	args = append(args, "-target", targetTriple)
	if cfg.BuildDebug {
		args = append(args, "-O0")
		if cfg.TargetOS == "windows" {
			args = append(args, "-gcodeview")
		} else {
			args = append(args, "-g")
		}
	}
	for _, llPath := range llPaths {
		args = append(args, "-x", "ir", llPath)
	}
	args = append(args, "-o", outputPath)
	return args
}

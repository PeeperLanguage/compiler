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
	"compiler/internal/toolchain"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

// Compile one entry file with a fresh compiler project.
func compileEntry(path string, debugBuild bool, targetOS, targetArch string) (compilerContext *project.CompilerContext, program *project.Module) {
	sourceProject, err := manifest.ResolveSourceFileProject(path)
	rootDir := sourceProject.RootDir
	projectName := sourceProject.ProjectName
	cfg := project.Config{
		RootDir:           rootDir,
		ProjectName:       projectName,
		Extension:         peeper.SourceExt,
		TargetOS:          targetOS,
		TargetArch:        targetArch,
		BuildDebug:        debugBuild,
		RequireEntrypoint: true,
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
	if !ctx.Target.Valid() {
		return fmt.Errorf("compiler target is unavailable")
	}

	artifactDir, err := os.MkdirTemp("", "peeper-link-")
	if err != nil {
		return fmt.Errorf("create link artifact directory: %w", err)
	}
	defer os.RemoveAll(artifactDir)

	executablePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve compiler executable: %w", err)
	}
	profile, err := toolchain.Resolve(executablePath, ctx.Target)
	if err != nil {
		return err
	}
	if !profile.Managed {
		fmt.Fprintf(os.Stderr, "warning: no managed Peeper toolchain profile; using %s from PATH\n", profile.ClangPath)
	}

	objectPaths := make([]string, 0, len(modules))
	for i, module := range modules {
		if module == nil {
			continue
		}
		ir := strings.TrimSpace(module.LLVMIR)
		if ir == "" {
			return fmt.Errorf("empty LLVM IR for module %s", module.ImportPath)
		}
		llPath := filepath.Join(artifactDir, fmt.Sprintf("mod_%d.ll", i))
		if err := os.WriteFile(llPath, []byte(ir), 0o644); err != nil {
			return fmt.Errorf("write llvm ir: %w", err)
		}
		objectPath := filepath.Join(artifactDir, fmt.Sprintf("mod_%d.o", i))
		if err := runCompilerTool(profile.ClangPath, profile.ObjectArgs(llPath, objectPath, ctx.Config.BuildDebug), "compile LLVM module "+module.ImportPath); err != nil {
			return err
		}
		objectPaths = append(objectPaths, objectPath)
	}
	if len(objectPaths) == 0 {
		return fmt.Errorf("no LLVM IR emitted")
	}
	responsePath := filepath.Join(artifactDir, "objects.rsp")
	if err := profile.WriteResponseFile(responsePath, objectPaths); err != nil {
		return err
	}
	outputDir := filepath.Dir(outputPath)
	extension := filepath.Ext(outputPath)
	base := strings.TrimSuffix(filepath.Base(outputPath), extension)
	stagedFile, err := os.CreateTemp(outputDir, "."+base+"-link-*"+extension)
	if err != nil {
		return fmt.Errorf("stage executable output: %w", err)
	}
	stagedPath := stagedFile.Name()
	if err := stagedFile.Close(); err != nil {
		return fmt.Errorf("close staged executable: %w", err)
	}
	if err := os.Remove(stagedPath); err != nil {
		return fmt.Errorf("prepare staged executable: %w", err)
	}
	defer os.Remove(stagedPath)
	if err := runCompilerTool(profile.LinkerPath, profile.LinkArgs(responsePath, stagedPath), "link executable"); err != nil {
		return err
	}
	return replacePath(stagedPath, outputPath)
}

func runCompilerTool(path string, args []string, action string) error {
	cmd := exec.Command(path, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s with %s: %w\n%s", action, filepath.Base(path), err, strings.TrimSpace(string(output)))
	}
	return nil
}

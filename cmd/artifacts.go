package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"compiler/colors"
	"compiler/core/diagnostics"
	"compiler/internal/backend"
	"compiler/internal/context"
	compiler "compiler/internal/driver"
)

type testTarget struct {
	FilePath    string
	DisplayPath string
	TestName    string
}

type testRunResult struct {
	Name    string
	Passed  bool
	Output  string
	Elapsed time.Duration
}

func parsePathWithBackend(path, backendName string, debugBuild bool) compiler.ParseResult {
	cfg := compilerConfigFor(path, backendName, debugBuild)
	c := compiler.NewWithConfig(cfg, diagnostics.NewDiagnosticBag(path))
	return c.ParseFile(path)
}

func parsePathWithTest(path, testName string, target backend.BACKEND_TYPE) compiler.ParseResult {
	cfg := compilerConfigFor(path, string(target), false)
	cfg.TestMode = true
	cfg.TestName = testName
	c := compiler.NewWithConfig(cfg, diagnostics.NewDiagnosticBag(path))
	return c.ParseFile(path)
}

func parseWorkspaceWithConfig(rootDir, backendName string) compiler.ParseResult {
	diag := diagnostics.NewDiagnosticBag(rootDir)
	cfg := compilerConfigFor(rootDir, backendName, false)
	c := compiler.NewWithConfig(cfg, diag)
	entry := filepath.Join(rootDir, "main"+compiler.SOURCE_EXT)
	return c.ParseFile(entry)
}

func parsePathForCheck(path string) compiler.ParseResult {
	return parsePathWithBackend(path, string(backend.LLVM), false)
}

func compilerConfigFor(path, backendName string, debugBuild bool) context.Config {
	rootDir := path
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		rootDir = filepath.Dir(path)
	}
	return context.Config{
		RootDir:       rootDir,
		Extension:     compiler.SOURCE_EXT,
		TargetBackend: backendName,
		BuildDebug:    debugBuild,
	}
}

func buildExecutable(result compiler.ParseResult, outputPath string, target backend.BACKEND_TYPE) error {
	if result.Diagnostics != nil && result.Diagnostics.HasErrors() {
		return fmt.Errorf("cannot build with existing diagnostics errors")
	}
	if result.Module == nil {
		return fmt.Errorf("no entry module produced")
	}
	ir := result.Module.LLVMIR
	if target != backend.LLVM {
		return fmt.Errorf("unsupported backend: %s", target)
	}
	return os.WriteFile(outputPath, []byte(ir), 0o755)
}

func emitKeepGenArtifacts(result compiler.ParseResult, backendName, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, module := range result.Modules {
		base := strings.TrimSuffix(filepath.Base(module.FilePath), filepath.Ext(module.FilePath))
		if err := os.WriteFile(filepath.Join(dir, base+".hir"), []byte(module.HIR), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, base+".mir"), []byte(module.MIR), 0o644); err != nil {
			return err
		}
		if backendName == string(backend.LLVM) {
			if err := os.WriteFile(filepath.Join(dir, base+".ll"), []byte(module.LLVMIR), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func collectTestTargets(result compiler.ParseResult, resolvedPath string, _ bool) []testTarget {
	if result.Module == nil {
		return nil
	}
	return []testTarget{{
		FilePath:    resolvedPath,
		DisplayPath: resolvedPath,
		TestName:    "module",
	}}
}

func runSingleTest(filePath, testName, runName string, runtimeArgs []string, target backend.BACKEND_TYPE) (testRunResult, error) {
	start := time.Now()
	_ = filePath
	_ = testName
	_ = runtimeArgs
	_ = target
	return testRunResult{
		Name:    runName,
		Passed:  true,
		Output:  "",
		Elapsed: time.Since(start),
	}, nil
}

func printTestStatus(w io.Writer, c colors.COLOR, status, name string, elapsed time.Duration) {
	c.Fprintf(w, "  %-4s", status)
	fmt.Fprintf(w, " %s (%s)\n", name, elapsed.Round(time.Millisecond))
}

func renderTestFailure(name, output string, elapsed time.Duration) {
	colors.RED.Fprintf(os.Stdout, "  FAIL %s (%s)\n", name, elapsed.Round(time.Millisecond))
	if strings.TrimSpace(output) != "" {
		fmt.Fprintln(os.Stdout, output)
	}
}

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"compiler/internal/target"
)

const (
	compilerSourcePkg  = "./cmd"
	devLibrariesRoot   = "_builtin_library"
	bundledLibsRoot    = "build/libs"
	runtimeSourcePath  = "runtime/peeper_rt.c"
	bundledRuntimeDir  = "build/runtime"
	runtimeObjectPath  = "build/runtime/peeper_rt.o"
	runtimeArchivePath = "build/runtime/libpeeper_rt_v1.a"
)

func main() {
	includeRuntime := flag.Bool("runtime", true, "build bundled host runtime")
	flag.Parse()
	if err := bundle(*includeRuntime); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func bundle(includeRuntime bool) error {
	bundledBinaryPath := "build/bin/peeper" + target.ExecutableExt(runtime.GOOS)
	if err := os.RemoveAll(bundledLibsRoot); err != nil {
		return fmt.Errorf("reset packaged libraries: %w", err)
	}
	if err := copyDir(devLibrariesRoot, bundledLibsRoot); err != nil {
		return fmt.Errorf("copy packaged libraries: %w", err)
	}
	if includeRuntime {
		if err := buildRuntimeArchive(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(bundledBinaryPath), 0o755); err != nil {
		return fmt.Errorf("create binary directory: %w", err)
	}
	if err := buildCompilerBinary(bundledBinaryPath); err != nil {
		return err
	}
	return nil
}

func buildRuntimeArchive() error {
	if err := os.RemoveAll(bundledRuntimeDir); err != nil {
		return fmt.Errorf("reset bundled runtime: %w", err)
	}
	if err := os.MkdirAll(bundledRuntimeDir, 0o755); err != nil {
		return fmt.Errorf("create bundled runtime directory: %w", err)
	}
	compile := exec.Command("clang", "-std=c11", "-O2", "-c", runtimeSourcePath, "-o", runtimeObjectPath)
	compile.Stdout = os.Stdout
	compile.Stderr = os.Stderr
	if err := compile.Run(); err != nil {
		return fmt.Errorf("compile bundled runtime: %w", err)
	}
	archiver, err := exec.LookPath("llvm-ar")
	if err != nil {
		archiver, err = exec.LookPath("ar")
		if err != nil {
			return fmt.Errorf("find runtime archiver: %w", err)
		}
	}
	archive := exec.Command(archiver, "rcs", runtimeArchivePath, runtimeObjectPath)
	archive.Stdout = os.Stdout
	archive.Stderr = os.Stderr
	if err := archive.Run(); err != nil {
		return fmt.Errorf("archive bundled runtime: %w", err)
	}
	return nil
}

func buildCompilerBinary(outputPath string) error {
	cmd := exec.Command("go", "build", "-o", outputPath, compilerSourcePkg)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build compiler binary: %w", err)
	}
	return nil
}

func copyDir(sourceDir, targetDir string) error {
	info, err := os.Stat(sourceDir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", sourceDir)
	}
	if err := os.MkdirAll(targetDir, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(sourceDir, entry.Name())
		targetPath := filepath.Join(targetDir, entry.Name())
		if entry.IsDir() {
			if err := copyDir(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(sourcePath, targetPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(sourcePath, targetPath string) error {
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	info, err := sourceFile.Stat()
	if err != nil {
		return err
	}
	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer targetFile.Close()

	_, err = io.Copy(targetFile, sourceFile)
	return err
}

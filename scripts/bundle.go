package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	compilerSourcePkg = "./cmd"
	bundledBinaryPath = "build/bin/peeper"
	devLibrariesRoot  = "_builtin_library"
	bundledLibsRoot   = "build/libs"
)

func main() {
	if err := bundle(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func bundle() error {
	if err := os.RemoveAll(bundledLibsRoot); err != nil {
		return fmt.Errorf("reset packaged libraries: %w", err)
	}
	if err := copyDir(devLibrariesRoot, bundledLibsRoot); err != nil {
		return fmt.Errorf("copy packaged libraries: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(bundledBinaryPath), 0o755); err != nil {
		return fmt.Errorf("create binary directory: %w", err)
	}
	if err := buildCompilerBinary(); err != nil {
		return err
	}
	return nil
}

func buildCompilerBinary() error {
	cmd := exec.Command("go", "build", "-o", bundledBinaryPath, compilerSourcePkg)
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

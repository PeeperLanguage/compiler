package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

func InitCommand(args []string) (returnErr error) {
	if _, err := os.Lstat(manifest.FileName); err == nil {
		return fmt.Errorf("%s already exists in current directory", manifest.FileName)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", manifest.FileName, err)
	}

	reader := bufio.NewReader(os.Stdin)
	projectName := ""
	if len(args) > 0 {
		projectName = args[0]
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		defaultName := filepath.Base(cwd)
		fmt.Printf("Project name (%s): ", defaultName)
		input, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read project name: %w", err)
		}
		input = strings.TrimSpace(input)
		if input == "" {
			projectName = defaultName
		} else {
			projectName = input
		}
	}

	projectName = strings.Map(func(char rune) rune {
		if char == '-' || unicode.IsSpace(char) {
			return '_'
		}
		return unicode.ToLower(char)
	}, projectName)
	if err := manifest.ValidatePackageName(projectName); err != nil {
		return err
	}

	sourceExists := false
	if info, err := os.Lstat(peeper.SourceDirName); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", peeper.SourceDirName)
		}
		sourceExists = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", peeper.SourceDirName, err)
	}

	mainPath := filepath.Join(peeper.SourceDirName, peeper.MainFileName)
	mainExists := false
	if sourceExists {
		if info, err := os.Lstat(mainPath); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s exists and is not a regular file", mainPath)
			}
			mainExists = true
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect %s: %w", mainPath, err)
		}
	}

	createdPaths := make([]string, 0, 3)
	complete := false
	defer func() {
		if complete {
			return
		}
		errs := []error{returnErr}
		for index := len(createdPaths) - 1; index >= 0; index-- {
			if err := os.Remove(createdPaths[index]); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("remove created %s: %w", createdPaths[index], err))
			}
		}
		returnErr = errors.Join(errs...)
	}()

	if !sourceExists {
		if err := os.Mkdir(peeper.SourceDirName, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", peeper.SourceDirName, err)
		}
		createdPaths = append(createdPaths, peeper.SourceDirName)
	}
	if !mainExists {
		createdPaths = append(createdPaths, mainPath)
		mainContent := `fn main() {
	println("Hello from Peeper!");
}
`
		if err := manifest.WriteFileAtomic(mainPath, []byte(mainContent), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", mainPath, err)
		}
		printSuccess("Created " + mainPath)
	}

	createdPaths = append(createdPaths, manifest.FileName)
	manifestContent := fmt.Sprintf(`name = %q
version = "0.0.1"
compiler = "<=%s"
build = "program"

[dependencies]
`, projectName, peeper.CompilerVersion)
	if err := manifest.WriteFileAtomic(manifest.FileName, []byte(manifestContent), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", manifest.FileName, err)
	}
	complete = true

	printSuccess(fmt.Sprintf("Initialized project: %s", projectName))
	fmt.Println("\nNext steps:")
	fmt.Printf("  1. Edit %s to add dependencies\n", manifest.FileName)
	fmt.Println("  2. Run: peeper get")
	fmt.Println("  3. Run: peeper run")
	return nil
}

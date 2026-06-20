package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"compiler/internal/driver"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

func InitCommand(args []string) error {
	if _, err := os.Stat(manifest.FileName); err == nil {
		return fmt.Errorf("%s already exists in current directory", manifest.FileName)
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
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input == "" {
			projectName = defaultName
		} else {
			projectName = input
		}
	}

	projectName = strings.ToLower(strings.ReplaceAll(projectName, " ", "-"))

	content := fmt.Sprintf(`name = %q
version = "0.0.1"
compiler = "<=%s"
build = "program"

[dependencies]
`, projectName, compiler.COMPILER_VERSION)

	if err := os.WriteFile(manifest.FileName, []byte(content), 0o644); err != nil {
		return err
	}

	if err := os.MkdirAll(peeper.SourceDirName, 0o755); err != nil {
		return err
	}
	mainPath := filepath.Join(peeper.SourceDirName, peeper.MainFileName)
	if _, err := os.Stat(mainPath); os.IsNotExist(err) {
		mainContent := `
fn main() {
	println("Hello from Peeper!")
}
`
		if err := os.WriteFile(mainPath, []byte(mainContent), 0o644); err != nil {
			return err
		}
		printSuccess("Created " + mainPath)
	}

	printSuccess(fmt.Sprintf("Initialized project: %s", projectName))
	fmt.Println("\nNext steps:")
	fmt.Printf("  1. Edit %s to add dependencies\n", manifest.FileName)
	fmt.Println("  2. Run: peeper get")
	fmt.Println("  3. Run: peeper run")
	return nil
}

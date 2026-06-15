package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"compiler/internal/driver"
)

func InitCommand(args []string) error {
	if _, err := os.Stat("peeper"); err == nil {
		return fmt.Errorf("peeper already exists in current directory")
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

	fmt.Print("Description (optional): ")
	description, _ := reader.ReadString('\n')
	description = strings.TrimSpace(description)
	if description == "" {
		description = "A new Peeper project"
	}

	fmt.Print("Author (optional): ")
	author, _ := reader.ReadString('\n')
	author = strings.TrimSpace(author)

	content := fmt.Sprintf(`[package]
name = %q
version = "0.0.1"
description = %q
author = %q
compiler = "<=%s"
	entry = "main.peep"

[dependencies]
`, projectName, description, author, compiler.COMPILER_VERSION)

	if err := os.WriteFile("peeper", []byte(content), 0o644); err != nil {
		return err
	}

	if _, err := os.Stat("main.peep"); os.IsNotExist(err) {
		mainContent := `
fn main() {
	println("Hello from Peeper!")
}
`
		if err := os.WriteFile("main.peep", []byte(mainContent), 0o644); err != nil {
			return err
		}
		printSuccess("Created main.peep")
	}

	printSuccess(fmt.Sprintf("Initialized project: %s", projectName))
	fmt.Println("\nNext steps:")
	fmt.Println("  1. Edit peeper to add dependencies")
	fmt.Println("  2. Run: peeper get")
	fmt.Println("  3. Run: peeper main.peep")
	return nil
}

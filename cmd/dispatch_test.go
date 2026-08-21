package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestExitOnCommandErrorPreservesProgramStatus(t *testing.T) {
	if os.Getenv("PEEPER_TEST_PROGRAM_EXIT") == "1" {
		exitOnCommandError(programExitStatus(10))
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestExitOnCommandErrorPreservesProgramStatus")
	cmd.Env = append(os.Environ(), "PEEPER_TEST_PROGRAM_EXIT=1")
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 10 {
		t.Fatalf("subprocess error = %v, want exit status 10", err)
	}
}

func TestCommandRegistryHasUniqueNamesAndRequiredAliases(t *testing.T) {
	seen := make(map[string]string)
	for _, command := range commandRegistry {
		if command.Name == "" || command.Usage == "" || command.Description == "" || command.Handler == nil {
			t.Fatalf("incomplete command definition: %#v", command)
		}
		for _, name := range append([]string{command.Name}, command.Aliases...) {
			if owner, duplicate := seen[name]; duplicate {
				t.Fatalf("command name %q shared by %q and %q", name, owner, command.Name)
			}
			seen[name] = command.Name
		}
	}
	for _, name := range []string{"lint", "rm", "ls", "clean", "cleanup", "build:llvm", "run:llvm"} {
		if _, ok := lookupCommand(name); !ok {
			t.Fatalf("required command or alias %q missing", name)
		}
	}
}

func TestTopLevelHelpComesFromCommandRegistry(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "help-")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = output
	printTopLevelUsage()
	os.Stderr = previous
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	help := string(content)
	for _, command := range commandRegistry {
		if !strings.Contains(help, command.Usage) || !strings.Contains(help, command.Description) {
			t.Fatalf("help missing registry command %q:\n%s", command.Name, help)
		}
	}
}

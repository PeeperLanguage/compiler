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
		if command.MinArgs < 0 || command.MaxArgs < unboundedArgs || command.MaxArgs != unboundedArgs && command.MaxArgs < command.MinArgs {
			t.Fatalf("invalid command arity: %#v", command)
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

func TestCommandRegistryArityContracts(t *testing.T) {
	tests := []struct {
		name string
		min  int
		max  int
	}{
		{name: "init", min: 0, max: 1},
		{name: "remove", min: 1, max: 1},
		{name: "list", min: 0, max: 0},
		{name: "cleanup", min: 0, max: 0},
		{name: "orphans", min: 0, max: 0},
		{name: "lsp", min: 0, max: 0},
		{name: "build", min: 0, max: unboundedArgs},
		{name: "run", min: 0, max: unboundedArgs},
		{name: "check", min: 0, max: unboundedArgs},
		{name: "get", min: 0, max: unboundedArgs},
		{name: "update", min: 0, max: unboundedArgs},
		{name: "sniff", min: 0, max: unboundedArgs},
	}
	for _, test := range tests {
		command, ok := lookupCommand(test.name)
		if !ok {
			t.Fatalf("command %q missing", test.name)
		}
		if command.MinArgs != test.min || command.MaxArgs != test.max {
			t.Fatalf("%s arity = %d..%d, want %d..%d", test.name, command.MinArgs, command.MaxArgs, test.min, test.max)
		}
	}
}

func TestCommandRunRejectsArityBeforeHandler(t *testing.T) {
	called := false
	command := commandDefinition{
		Name:    "sample",
		Usage:   "sample <value>",
		MinArgs: 1,
		MaxArgs: 1,
		Handler: func([]string) error {
			called = true
			return nil
		},
	}
	for _, args := range [][]string{nil, {"one", "two"}} {
		if err := command.run(args); err == nil {
			t.Fatalf("run(%v) succeeded", args)
		}
		if called {
			t.Fatalf("handler called for invalid args %v", args)
		}
	}
	if err := command.run([]string{"one"}); err != nil {
		t.Fatalf("run valid args: %v", err)
	}
	if !called {
		t.Fatal("handler not called for valid args")
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

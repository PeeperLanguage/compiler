package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"

	"compiler/cmd/cli"
	"compiler/internal/lsp"
	"compiler/pkg/colors"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

const (
	exitCodeOK    = 0
	exitCodeError = 1
	exitCodeUsage = 2
	unboundedArgs = -1
)

type programExitStatus int

func (status programExitStatus) Error() string {
	return fmt.Sprintf("program exited with status %d", status)
}

// exitOnCommandError prints err to stderr in red (unless it is
// errAlreadyReported, which the caller has already reported) and exits.
func exitOnCommandError(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, errAlreadyReported) {
		os.Exit(exitCodeError)
	}
	if status, ok := errors.AsType[programExitStatus](err); ok {
		os.Exit(int(status))
	}
	colors.RED.Fprintln(os.Stderr, err)
	os.Exit(exitCodeError)
}

func parseAndRunCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	command, ok := lookupCommand(args[0])
	if !ok {
		return false
	}
	exitOnCommandError(command.run(args[1:]))
	return true
}

type commandDefinition struct {
	Name        string
	Aliases     []string
	Usage       string
	Description string
	MinArgs     int
	MaxArgs     int
	Handler     func([]string) error
}

func (command commandDefinition) run(args []string) error {
	if len(args) < command.MinArgs || command.MaxArgs != unboundedArgs && len(args) > command.MaxArgs {
		return fmt.Errorf("usage: peeper %s", command.Usage)
	}
	return command.Handler(args)
}

var commandRegistry = []commandDefinition{
	{Name: "build", Aliases: []string{"build:llvm"}, Usage: "build[:llvm] [path]", Description: fmt.Sprintf("build program or use %s/%s from %s", peeper.SourceDirName, peeper.MainFileName, manifest.FileName), MaxArgs: unboundedArgs, Handler: buildCommand},
	{Name: "run", Aliases: []string{"run:llvm"}, Usage: "run[:llvm] [path] [args]", Description: "build and run program", MaxArgs: unboundedArgs, Handler: runCommand},
	{Name: "check", Aliases: []string{"lint"}, Usage: "check|lint [path ...]", Description: fmt.Sprintf("typecheck files or folders recursively (%s only)", peeper.SourceExt), MaxArgs: unboundedArgs, Handler: checkCommand},
	{Name: "init", Usage: "init [name]", Description: fmt.Sprintf("create project with %s", manifest.FileName), MaxArgs: 1, Handler: cli.InitCommand},
	{Name: "get", Usage: "get [pkg ...]", Description: fmt.Sprintf("install dependencies from %s or named packages", manifest.FileName), MaxArgs: unboundedArgs, Handler: cli.GetCommand},
	{Name: "update", Usage: "update [pkg ...]", Description: "update locked dependencies", MaxArgs: unboundedArgs, Handler: cli.UpdateCommand},
	{Name: "sniff", Usage: "sniff [pkg ...]", Description: "preview dependency updates", MaxArgs: unboundedArgs, Handler: cli.SniffCommand},
	{Name: "remove", Aliases: []string{"rm"}, Usage: "remove|rm <alias>", Description: fmt.Sprintf("remove dependency from %s and %s", manifest.FileName, manifest.LockfileName), MinArgs: 1, MaxArgs: 1, Handler: cli.RemoveCommand},
	{Name: "list", Aliases: []string{"ls"}, Usage: "list|ls", Description: "list direct and transitive dependencies", Handler: cli.ListCommand},
	{Name: "cleanup", Aliases: []string{"clean"}, Usage: "cleanup|clean", Description: "remove orphaned cached dependencies", Handler: cli.CleanupCommand},
	{Name: "orphans", Usage: "orphans", Description: "list orphaned cache and lock entries", Handler: cli.OrphansCommand},
	{Name: "lsp", Usage: "lsp", Description: "start language server", Handler: lspCommand},
}

func lookupCommand(name string) (commandDefinition, bool) {
	for _, command := range commandRegistry {
		if name == command.Name {
			return command, true
		}
		if slices.Contains(command.Aliases, name) {
			return command, true
		}
	}
	return commandDefinition{}, false
}

func lspCommand(_ []string) error {
	colors.CYAN.Fprintln(os.Stderr, "starting Peeper LSP server...")
	return lsp.Run(os.Stdin, os.Stdout)
}

func printUsageAndExit(code int) {
	showVersion := defineTopLevelFlags()

	if len(os.Args) > 1 {
		if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(exitCodeError)
		}
	}
	if *showVersion {
		fmt.Printf("v%s\n", peeper.CompilerVersion)
		os.Exit(exitCodeOK)
	}
	printTopLevelUsage()
	os.Exit(code)
}

// defineTopLevelFlags registers the -version/-v and -help/-h flags on the
// global flag set and returns a pointer to the parsed version flag.
// The -help flag is registered only for side effect of being parseable;
// the actual help banner is printed unconditionally in printTopLevelUsage.
func defineTopLevelFlags() *bool {
	flag.CommandLine.SetOutput(os.Stderr)
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	showVersion := flag.Bool("version", false, "show compiler version")
	flag.BoolVar(showVersion, "v", false, "alias for -version")
	flag.Bool("help", false, "show help")
	flag.Bool("h", false, "alias for -help")
	return showVersion
}

// printTopLevelUsage writes the program's usage banner to stderr.
func printTopLevelUsage() {
	colors.BLUE.Fprintln(os.Stderr, "Peeper compiler v"+peeper.CompilerVersion)
	colors.CYAN.Fprintln(os.Stderr, "\nUsage:")
	colors.GREEN.Fprintf(os.Stderr, "  peeper [command] [args]\n")
	colors.CYAN.Fprintln(os.Stderr, "\nCommands:")
	for _, command := range commandRegistry {
		fmt.Fprintf(os.Stderr, "  %-28s %s\n", command.Usage, command.Description)
	}
	colors.CYAN.Fprintln(os.Stderr, "\nExamples:")
	colors.GREEN.Fprintf(os.Stderr, "  peeper build\n")
	colors.GREEN.Fprintf(os.Stderr, "  peeper build %s/%s\n", peeper.SourceDirName, peeper.MainFileName)
	colors.GREEN.Fprintf(os.Stderr, "  peeper build -o app\n")
	colors.GREEN.Fprintf(os.Stderr, "  peeper run main%s arg1 arg2\n", peeper.SourceExt)
}

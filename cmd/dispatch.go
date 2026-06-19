package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"compiler/cmd/cli"
	compiler "compiler/internal/driver"
	"compiler/internal/lsp"
	"compiler/pkg/colors"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

const (
	exitCodeOK    = 0
	exitCodeError = 1
	exitCodeUsage = 2
)

// exitOnCommandError prints err to stderr in red (unless it is
// errAlreadyReported, which the caller has already reported) and exits.
func exitOnCommandError(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, errAlreadyReported) {
		os.Exit(exitCodeError)
	}
	colors.RED.Fprintln(os.Stderr, err)
	os.Exit(exitCodeError)
}

func parseAndRunCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	commandName, commandBackend, err := parseCommandBackend(args[0])
	if err != nil {
		colors.RED.Fprintln(os.Stderr, err)
		os.Exit(exitCodeUsage)
	}

	switch commandName {
	case "build":
		exitOnCommandError(buildCommand(args[1:], commandBackend))
	case "lsp":
		startLSPServer()
	case "run":
		exitOnCommandError(runCommand(args[1:], commandBackend))
	case "check", "lint":
		exitOnCommandError(checkCommand(args[1:]))
	default:
		return runCLISubcommand(commandName, args[1:])
	}
	return true
}

// runCLISubcommand dispatches to a cli.XxxCommand and converts its error into
// a process exit. Returns true when the name matched a known CLI subcommand.
func runCLISubcommand(name string, args []string) bool {
	handler, ok := lookupCLISubcommand(name)
	if !ok {
		return false
	}
	exitOnCommandError(handler(args))
	return true
}

// cliSubcommand maps a CLI subcommand name to its handler function.
type cliSubcommand func([]string) error

// lookupCLISubcommand returns the handler for a known CLI subcommand.
// Add new entries here as new commands are introduced.
func lookupCLISubcommand(name string) (cliSubcommand, bool) {
	switch name {
	case "init":
		return cli.InitCommand, true
	case "get":
		return cli.GetCommand, true
	case "update":
		return cli.UpdateCommand, true
	case "sniff":
		return cli.SniffCommand, true
	case "remove", "rm":
		return cli.RemoveCommand, true
	case "list", "ls":
		return cli.ListCommand, true
	case "clean":
		return cli.CleanupCommand, true
	case "orphans":
		return cli.OrphansCommand, true
	default:
		return nil, false
	}
}

func startLSPServer() {
	colors.CYAN.Fprintln(os.Stderr, "starting Peeper LSP server...")
	if err := lsp.Run(os.Stdin, os.Stdout); err != nil {
		colors.RED.Fprintln(os.Stderr, err)
		os.Exit(exitCodeError)
	}
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
		fmt.Printf("v%s\n", compiler.COMPILER_VERSION)
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
	colors.BLUE.Fprintln(os.Stderr, "Peeper compiler v"+compiler.COMPILER_VERSION)
	colors.CYAN.Fprintln(os.Stderr, "\nUsage:")
	colors.GREEN.Fprintf(os.Stderr, "  peeper [command] [args]\n")
	colors.CYAN.Fprintln(os.Stderr, "\nCommands:")
	fmt.Fprintf(os.Stderr, "  build[:llvm] [path]     build a program or use src/main%s from %s\n", peeper.SourceExt, manifest.FileName)
	fmt.Fprintln(os.Stderr, "  run[:llvm] [path] [args]  build and run a program (default llvm)")
	fmt.Fprintf(os.Stderr, "  check|lint [path]       typecheck file or recursively check folder (%s only)\n", peeper.SourceExt)
	fmt.Fprintf(os.Stderr, "  init [name]             create a new project with %s\n", manifest.FileName)
	fmt.Fprintf(os.Stderr, "  get [pkg ...]           install dependencies from %s or specific packages\n", manifest.FileName)
	fmt.Fprintln(os.Stderr, "  update [pkg ...]        update locked dependencies")
	fmt.Fprintln(os.Stderr, "  sniff [pkg ...]         preview updates that peeper update would apply")
	fmt.Fprintf(os.Stderr, "  remove|rm <alias>       remove dependency alias from %s and %s\n", manifest.FileName, manifest.LockfileName)
	fmt.Fprintln(os.Stderr, "  list|ls                 list direct and transitive dependencies")
	fmt.Fprintln(os.Stderr, "  orphans                 list orphaned cache/lock entries clean will remove")
	fmt.Fprintln(os.Stderr, "  cleanup|clean           remove orphaned cached dependencies")
	fmt.Fprintln(os.Stderr, "  lsp                     start the Peeper language server")
	colors.CYAN.Fprintln(os.Stderr, "\nExamples:")
	colors.GREEN.Fprintf(os.Stderr, "  peeper build\n")
	colors.GREEN.Fprintf(os.Stderr, "  peeper build src/main%s\n", peeper.SourceExt)
	colors.GREEN.Fprintf(os.Stderr, "  peeper build -o app\n")
	colors.GREEN.Fprintf(os.Stderr, "  peeper run main%s arg1 arg2\n", peeper.SourceExt)
}

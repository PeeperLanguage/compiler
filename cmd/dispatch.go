package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"compiler/cmd/cli"
	"compiler/colors"
	compiler "compiler/internal/driver"
	"compiler/internal/lsp"
)

func exitOnCommandError(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, errAlreadyReported) {
		os.Exit(1)
	}
	colors.RED.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func parseAndRunCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}

	command := args[0]
	commandArgs := args[1:]
	commandName, commandBackend, err := parseCommandBackend(command)
	if err != nil {
		colors.RED.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	switch commandName {
	case "build":
		exitOnCommandError(buildCommand(commandArgs, commandBackend))
		return true
	case "lsp":
		colors.CYAN.Fprintln(os.Stderr, "starting Ember LSP server...")
		if err := lsp.Run(os.Stdin, os.Stdout); err != nil {
			colors.RED.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
	case "init":
		exitOnCommandError(cli.InitCommand(commandArgs))
		return true
	case "get":
		exitOnCommandError(cli.GetCommand(commandArgs))
		return true
	case "update":
		exitOnCommandError(cli.UpdateCommand(commandArgs))
		return true
	case "sniff":
		exitOnCommandError(cli.SniffCommand(commandArgs))
		return true
	case "remove", "rm":
		exitOnCommandError(cli.RemoveCommand(commandArgs))
		return true
	case "list", "ls":
		exitOnCommandError(cli.ListCommand(commandArgs))
		return true
	case "clean":
		exitOnCommandError(cli.CleanupCommand(commandArgs))
		return true
	case "orphans":
		exitOnCommandError(cli.OrphansCommand(commandArgs))
		return true
	case "run":
		exitOnCommandError(runCommand(commandArgs, commandBackend))
		return true
	case "check", "lint":
		exitOnCommandError(checkCommand(commandArgs))
		return true
	default:
		return false
	}
}

func printUsageAndExit(code int) {
	flag.CommandLine.SetOutput(os.Stderr)
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	showVersion := flag.Bool("version", false, "show compiler version")
	flag.BoolVar(showVersion, "v", false, "alias for -version")
	showHelp := flag.Bool("help", false, "show help")
	flag.BoolVar(showHelp, "h", false, "alias for -help")

	flag.Usage = func() {
		colors.BLUE.Fprintln(os.Stderr, "Ember compiler v"+compiler.COMPILER_VERSION)
		colors.CYAN.Fprintln(os.Stderr, "\nUsage:")
		colors.GREEN.Fprintf(os.Stderr, "  ember [command] [args]\n")
		colors.CYAN.Fprintln(os.Stderr, "\nCommands:")
		fmt.Fprintln(os.Stderr, "  build[:llvm] [path]     build a program or use package.entry from ember")
		fmt.Fprintln(os.Stderr, "  run[:llvm] [path] [args]  build and run a program (default llvm)")
		fmt.Fprintln(os.Stderr, "  check|lint [path]       typecheck file or recursively check folder (.em only)")
		fmt.Fprintln(os.Stderr, "  init [name]             create a new project with ember")
		fmt.Fprintln(os.Stderr, "  get [pkg ...]           install dependencies from ember or specific packages")
		fmt.Fprintln(os.Stderr, "  update [pkg ...]        update locked dependencies")
		fmt.Fprintln(os.Stderr, "  sniff [pkg ...]         preview updates that ember update would apply")
		fmt.Fprintln(os.Stderr, "  remove|rm <alias>       remove dependency alias from ember and lockfile")
		fmt.Fprintln(os.Stderr, "  list|ls                 list direct and transitive dependencies")
		fmt.Fprintln(os.Stderr, "  orphans                 list orphaned cache/lock entries clean will remove")
		fmt.Fprintln(os.Stderr, "  cleanup|clean           remove orphaned cached dependencies")
		fmt.Fprintln(os.Stderr, "  lsp                     start the Ember language server")
		colors.CYAN.Fprintln(os.Stderr, "\nExamples:")
		colors.GREEN.Fprintf(os.Stderr, "  ember build\n")
		colors.GREEN.Fprintf(os.Stderr, "  ember build src/main.em\n")
		colors.GREEN.Fprintf(os.Stderr, "  ember build -o app\n")
		colors.GREEN.Fprintf(os.Stderr, "  ember run main.em arg1 arg2\n")
	}

	if len(os.Args) > 1 {
		_ = flag.CommandLine.Parse(os.Args[1:])
	}
	if *showVersion {
		fmt.Printf("v%s\n", compiler.COMPILER_VERSION)
		os.Exit(0)
	}
	flag.Usage()
	os.Exit(code)
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"compiler/internal/backend"
	compiler "compiler/internal/driver"
	"compiler/internal/project"
	"compiler/internal/target"
	"compiler/pkg/colors"
	"compiler/pkg/manifest"
)

var errAlreadyReported = errors.New("diagnostics already reported")

// tempRunFilePrefix is the prefix for the temporary executable created by 'ember run'.
const (
	tempRunFilePrefix = "ember-run-"
	genArtifactsDir   = "_gen"
)

// emitAndCheckDiagnostics prints all pending diagnostics and returns errAlreadyReported
// if any errors are present. Shared by build, run, and check commands.
func emitAndCheckDiagnostics(ctx *project.CompilerContext) error {
	if ctx == nil || ctx.Diagnostics == nil {
		return fmt.Errorf("compiler diagnostics unavailable")
	}
	if diags := ctx.Diagnostics.Diagnostics(); len(diags) > 0 {
		ctx.Diagnostics.EmitAll()
	}
	if ctx.Diagnostics.HasErrors() {
		return errAlreadyReported
	}
	return nil
}

// parseBackendType returns backend.LLVM when target is empty.
// build and run default to LLVM when no explicit backend is specified.
func parseBackendType(t backend.BACKEND_TYPE) backend.BACKEND_TYPE {
	if t == "" {
		return backend.LLVM
	}
	return t
}

type commandCommonFlags struct {
	logFormat *string
	m32       *bool
}

func addCommandCommonFlags(fs *flag.FlagSet) commandCommonFlags {
	return commandCommonFlags{
		logFormat: fs.String("logformat", string(colors.LogFormatANSI), "log output format (ansi|normal|html)"),
		m32:       fs.Bool("m32", false, "target 32-bit ABI"),
	}
}

func applyCommandCommonFlags(flags commandCommonFlags) error {
	if err := colors.SetLogFormatString(*flags.logFormat); err != nil {
		return err
	}
	if *flags.m32 {
		return target.SetSizeBits(target.Bits32)
	}
	return target.SetSizeBits(0)
}

func parseCommandArgs(name string, args []string) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	common := addCommandCommonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if err := applyCommandCommonFlags(common); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

func parseCommandBackend(command string) (string, backend.BACKEND_TYPE, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", "", fmt.Errorf("empty command")
	}
	base, suffix, hasSuffix := strings.Cut(command, ":")
	switch base {
	case "build", "run":
		if !hasSuffix || strings.TrimSpace(suffix) == "" {
			return base, backend.LLVM, nil
		}
		target := backend.BACKEND_TYPE(strings.ToLower(strings.TrimSpace(suffix)))
		switch target {
		case backend.LLVM:
			return base, target, nil
		default:
			return "", "", fmt.Errorf("invalid %s backend %q (expected llvm)", base, suffix)
		}
	default:
		return command, "", nil
	}
}

type buildFlags struct {
	outputPath string
	keepGen    bool
	debugBuild bool
}

func buildCommand(args []string, target backend.BACKEND_TYPE) error {
	opts, positional, err := parseBuildArgs("build", args)
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return fmt.Errorf("build accepts at most one path argument")
	}

	sourcePath := ""
	if len(positional) == 1 {
		sourcePath = positional[0]
	}
	resolvedPath, buildInfo, err := resolveBuildTarget("build", sourcePath)
	if err != nil {
		return err
	}
	if buildInfo.SelectedByDiscovery {
		colors.CYAN.Fprintf(os.Stderr, "using entry: %s\n", buildInfo.EntryPath)
	}

	target = parseBackendType(target)
	ctx, entry := compileEntry(resolvedPath, string(target), opts.debugBuild)
	if err := emitAndCheckDiagnostics(ctx); err != nil {
		return err
	}

	if opts.keepGen {
		if err := saveIRs(ctx, string(target), genArtifactsDir); err != nil {
			return err
		}
		colors.GREEN.Fprintln(os.Stdout, "Generated artifacts in "+genArtifactsDir)
	}

	outputPath := opts.outputPath
	if strings.TrimSpace(outputPath) == "" {
		outputPath = buildInfo.DefaultOutputPath
	}
	if err := buildExecutable(ctx, entry, outputPath, target); err != nil {
		return err
	}
	colors.GREEN.Fprintf(os.Stdout, "Built %s\n", outputPath)
	return nil
}

func parseBuildArgs(name string, args []string) (buildFlags, []string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	common := addCommandCommonFlags(fs)
	outputPath := fs.String("o", "", "compile and link to executable")
	keepGen := fs.Bool("keep-gen", false, "keep generated AST/HIR/MIR/backend IR in _gen directory")
	fs.BoolVar(keepGen, "k", false, "alias for -keep-gen")
	debugBuild := fs.Bool("debug", false, "enable debug build mode (emits debug info and debug-friendly codegen)")
	if err := fs.Parse(args); err != nil {
		return buildFlags{}, nil, err
	}
	if err := applyCommandCommonFlags(common); err != nil {
		return buildFlags{}, nil, err
	}
	return buildFlags{
		outputPath: *outputPath,
		keepGen:    *keepGen,
		debugBuild: *debugBuild,
	}, fs.Args(), nil
}

func runCommand(args []string, target backend.BACKEND_TYPE) error {
	parsedArgs, err := parseCommandArgs("run", args)
	if err != nil {
		return err
	}

	sourcePath := ""
	runtimeArgs := []string{}
	if len(parsedArgs) > 0 {
		sourcePath = parsedArgs[0]
		runtimeArgs = parsedArgs[1:]
	}
	resolvedPath, buildInfo, err := resolveBuildTarget("run", sourcePath)
	if err != nil {
		return err
	}
	if buildInfo.SelectedByDiscovery {
		colors.CYAN.Fprintf(os.Stderr, "using entry: %s\n", buildInfo.EntryPath)
	}

	target = parseBackendType(target)
	ctx, entry := compileEntry(resolvedPath, string(target), false)
	if err := emitAndCheckDiagnostics(ctx); err != nil {
		return err
	}

	tempPattern := tempRunFilePrefix + "*"
	if runtime.GOOS == "windows" {
		tempPattern = tempRunFilePrefix + "*.exe"
	}
	tempFile, err := os.CreateTemp("", tempPattern)
	if err != nil {
		return fmt.Errorf("create temp output: %w", err)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		return err
	}
	_ = os.Remove(tempPath)

	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(tempPath), ".exe") {
		tempPath += ".exe"
	}
	defer func() {
		_ = os.Remove(tempPath)
	}()

	if err := buildExecutable(ctx, entry, tempPath, target); err != nil {
		return err
	}

	cmd := exec.Command(tempPath, runtimeArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("run program: %w", err)
	}
	return nil
}

type buildTarget struct {
	EntryPath           string
	SelectedByDiscovery bool
	DefaultOutputPath   string
}

func resolveBuildTarget(commandName, path string) (resolvedPath string, info buildTarget, err error) {
	if strings.TrimSpace(path) == "" {
		info, err = resolveManifestBuildTarget(commandName, ".")
		if err != nil {
			return "", buildTarget{}, err
		}
		return info.EntryPath, info, nil
	}

	resolvedPath, err = filepath.Abs(path)
	if err != nil {
		return "", buildTarget{}, err
	}
	if ext := filepath.Ext(resolvedPath); ext != "" && !strings.EqualFold(ext, compiler.SOURCE_EXT) {
		return "", buildTarget{}, fmt.Errorf("unsupported source file extension %q (expected %s)", ext, compiler.SOURCE_EXT)
	}
	fileInfo, err := os.Stat(resolvedPath)
	if err != nil {
		return "", buildTarget{}, err
	}
	if fileInfo.IsDir() {
		targetInfo, err := resolveManifestBuildTarget(commandName, resolvedPath)
		if err != nil {
			return "", buildTarget{}, err
		}
		return targetInfo.EntryPath, targetInfo, nil
	}
	return resolvedPath, buildTarget{
		EntryPath:         resolvedPath,
		DefaultOutputPath: defaultOutputNameForEntry(resolvedPath),
	}, nil
}

func resolveManifestBuildTarget(commandName, startPath string) (buildTarget, error) {
	manifestPath, err := manifest.FindManifestPath(startPath)
	if err != nil {
		return buildTarget{}, fmt.Errorf("%s requires an input file or %s with package.entry", commandName, manifest.FileName)
	}
	file, err := manifest.Load(manifestPath)
	if err != nil {
		return buildTarget{}, err
	}
	entry := strings.TrimSpace(file.Package.Entry)
	if entry == "" {
		return buildTarget{}, fmt.Errorf("%s: package.entry is required for `ember %s` without an explicit file", manifestPath, commandName)
	}

	entry = strings.ReplaceAll(entry, "\\", "/")
	if filepath.Ext(entry) == "" {
		entry += compiler.SOURCE_EXT
	}
	if !strings.EqualFold(filepath.Ext(entry), compiler.SOURCE_EXT) {
		return buildTarget{}, fmt.Errorf("%s: package.entry must point to a %s file", manifestPath, compiler.SOURCE_EXT)
	}

	manifestDir := filepath.Dir(manifestPath)
	entryPath := filepath.Clean(filepath.Join(manifestDir, filepath.FromSlash(entry)))
	rel, relErr := filepath.Rel(manifestDir, entryPath)
	if relErr != nil {
		return buildTarget{}, relErr
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return buildTarget{}, fmt.Errorf("%s: package.entry must stay inside the package root", manifestPath)
	}

	entryInfo, statErr := os.Stat(entryPath)
	if statErr != nil {
		return buildTarget{}, fmt.Errorf("entry file not found: %s", entryPath)
	}
	if entryInfo.IsDir() {
		return buildTarget{}, fmt.Errorf("entry path is a directory: %s", entryPath)
	}
	outputPath := strings.TrimSpace(file.Package.Name)
	if outputPath == "" {
		outputPath = defaultOutputNameForEntry(entryPath)
	}
	return buildTarget{
		EntryPath:           entryPath,
		SelectedByDiscovery: true,
		DefaultOutputPath:   outputPath,
	}, nil
}

func defaultOutputNameForEntry(entryPath string) string {
	base := filepath.Base(entryPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func checkCommand(args []string) error {
	parsedArgs, err := parseCommandArgs("check", args)
	if err != nil {
		return err
	}

	path := "."
	if len(parsedArgs) > 0 {
		path = parsedArgs[0]
	}

	ctx, _ := compileEntry(path, string(backend.LLVM), false)
	if err := emitAndCheckDiagnostics(ctx); err != nil {
		return err
	}
	return nil
}

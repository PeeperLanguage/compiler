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
	"compiler/internal/project"
	"compiler/internal/target"
	"compiler/pkg/colors"
	"compiler/pkg/manifest"
	"compiler/pkg/peeper"
)

var errAlreadyReported = errors.New("diagnostics already reported")

// tempRunFilePrefix is the prefix for the temporary executable created by 'peeper run'.
const (
	tempRunFilePrefix = "peeper-run-"
	genArtifactsDir   = "_gen"
	debugBuildUsage   = "enable debug build mode (emits debug info and debug-friendly codegen)"
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

type commandCommonFlags struct {
	logFormat  *string
	m32        *bool
	targetOS   *string
	targetArch *string
}

func addCommandCommonFlags(fs *flag.FlagSet) commandCommonFlags {
	return commandCommonFlags{
		logFormat:  fs.String("logformat", string(colors.LogFormatANSI), "log output format (ansi|normal|html)"),
		m32:        fs.Bool("m32", false, "target 32-bit ABI"),
		targetOS:   fs.String("target-os", "", "target operating system (defaults to host GOOS)"),
		targetArch: fs.String("target-arch", "", "target architecture (defaults to host GOARCH)"),
	}
}

func applyCommandCommonFlags(flags commandCommonFlags) (string, string, error) {
	if err := colors.SetLogFormatString(*flags.logFormat); err != nil {
		return "", "", err
	}
	targetOS := target.NormalizeOS(*flags.targetOS)
	targetArch := target.NormalizeArch(*flags.targetArch)
	if _, err := target.LLVMTriple(targetOS, targetArch); err != nil {
		return "", "", err
	}
	sizeBits := target.DefaultSizeBitsForArch(targetArch)
	if *flags.m32 {
		sizeBits = target.Bits32
	}
	if err := target.SetSizeBits(sizeBits); err != nil {
		return "", "", err
	}
	return targetOS, targetArch, nil
}

type commandOptions struct {
	positional []string
	targetOS   string
	targetArch string
	debugBuild bool
}

func parseCommandArgs(name string, args []string, allowDebug bool) (commandOptions, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	common := addCommandCommonFlags(fs)
	debugBuild := false
	if allowDebug {
		fs.BoolVar(&debugBuild, "debug", false, debugBuildUsage)
	}
	if err := fs.Parse(args); err != nil {
		return commandOptions{}, err
	}
	targetOS, targetArch, err := applyCommandCommonFlags(common)
	if err != nil {
		return commandOptions{}, err
	}
	return commandOptions{
		positional: fs.Args(),
		targetOS:   targetOS,
		targetArch: targetArch,
		debugBuild: debugBuild,
	}, nil
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
	targetOS   string
	targetArch string
}

func buildCommand(args []string, backendTarget backend.BACKEND_TYPE) error {
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
	resolvedPath, buildInfo, err := resolveBuildTarget("build", sourcePath, opts.targetOS)
	if err != nil {
		return err
	}
	if buildInfo.SelectedByDiscovery {
		colors.CYAN.Fprintf(os.Stderr, "using entry: %s\n", buildInfo.EntryPath)
	}

	if backendTarget == "" {
		backendTarget = backend.LLVM
	}
	ctx, entry := compileEntry(resolvedPath, string(backendTarget), opts.debugBuild, opts.targetOS, opts.targetArch)
	if err := emitAndCheckDiagnostics(ctx); err != nil {
		return err
	}

	if opts.keepGen {
		if err := saveIRs(ctx, string(backendTarget), genArtifactsDir); err != nil {
			return err
		}
		colors.GREEN.Fprintln(os.Stdout, "Generated artifacts in "+genArtifactsDir)
	}

	outputPath := opts.outputPath
	if strings.TrimSpace(outputPath) == "" {
		outputPath = buildInfo.DefaultOutputPath
	}
	if err := buildExecutable(ctx, entry, outputPath, backendTarget); err != nil {
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
	debugBuild := fs.Bool("debug", false, debugBuildUsage)
	if err := fs.Parse(args); err != nil {
		return buildFlags{}, nil, err
	}
	targetOS, targetArch, err := applyCommandCommonFlags(common)
	if err != nil {
		return buildFlags{}, nil, err
	}
	return buildFlags{
		outputPath: *outputPath,
		keepGen:    *keepGen,
		debugBuild: *debugBuild,
		targetOS:   targetOS,
		targetArch: targetArch,
	}, fs.Args(), nil
}

func runCommand(args []string, backendTarget backend.BACKEND_TYPE) error {
	opts, err := parseCommandArgs("run", args, true)
	if err != nil {
		return err
	}

	sourcePath := ""
	runtimeArgs := []string{}
	if len(opts.positional) > 0 {
		sourcePath = opts.positional[0]
		runtimeArgs = opts.positional[1:]
	}
	resolvedPath, buildInfo, err := resolveBuildTarget("run", sourcePath, opts.targetOS)
	if err != nil {
		return err
	}
	if buildInfo.SelectedByDiscovery {
		colors.CYAN.Fprintf(os.Stderr, "using entry: %s\n", buildInfo.EntryPath)
	}
	if !target.IsHostTarget(opts.targetOS, opts.targetArch) {
		return fmt.Errorf("run target %s/%s does not match host %s/%s", opts.targetOS, opts.targetArch, runtime.GOOS, runtime.GOARCH)
	}

	if backendTarget == "" {
		backendTarget = backend.LLVM
	}
	ctx, entry := compileEntry(resolvedPath, string(backendTarget), opts.debugBuild, opts.targetOS, opts.targetArch)
	if err := emitAndCheckDiagnostics(ctx); err != nil {
		return err
	}

	tempPattern := tempRunFilePrefix + "*"
	if ext := target.ExecutableExt(opts.targetOS); ext != "" {
		tempPattern = tempRunFilePrefix + "*" + ext
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

	if ext := target.ExecutableExt(opts.targetOS); ext != "" && !strings.HasSuffix(strings.ToLower(tempPath), ext) {
		tempPath += ext
	}
	defer func() {
		_ = os.Remove(tempPath)
	}()

	if err := buildExecutable(ctx, entry, tempPath, backendTarget); err != nil {
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

func resolveBuildTarget(commandName, path string, targetOS string) (resolvedPath string, info buildTarget, err error) {
	if strings.TrimSpace(path) == "" {
		info, err = resolveManifestBuildTarget(commandName, ".", targetOS)
		if err != nil {
			return "", buildTarget{}, err
		}
		return info.EntryPath, info, nil
	}

	resolvedPath, err = filepath.Abs(path)
	if err != nil {
		return "", buildTarget{}, err
	}
	if ext := filepath.Ext(resolvedPath); ext != "" && !strings.EqualFold(ext, peeper.SourceExt) {
		return "", buildTarget{}, fmt.Errorf("unsupported source file extension %q (expected %s)", ext, peeper.SourceExt)
	}
	fileInfo, err := os.Stat(resolvedPath)
	if err != nil {
		return "", buildTarget{}, err
	}
	if fileInfo.IsDir() {
		targetInfo, err := resolveManifestBuildTarget(commandName, resolvedPath, targetOS)
		if err != nil {
			return "", buildTarget{}, err
		}
		return targetInfo.EntryPath, targetInfo, nil
	}
	return resolvedPath, buildTarget{
		EntryPath:         resolvedPath,
		DefaultOutputPath: defaultOutputNameForEntry(resolvedPath, targetOS),
	}, nil
}

func resolveManifestBuildTarget(commandName, startPath string, targetOS string) (buildTarget, error) {
	loadedProject, err := manifest.LoadProject(startPath)
	if err != nil {
		return buildTarget{}, fmt.Errorf("%s requires an input file or %s", commandName, manifest.FileName)
	}
	if loadedProject.File.Package.Build != manifest.BuildProgram {
		return buildTarget{}, fmt.Errorf("%s: `peeper %s` requires build = %q", loadedProject.ManifestPath, commandName, manifest.BuildProgram)
	}
	entryPath := filepath.Join(loadedProject.RootDir, "src", "main"+peeper.SourceExt)

	entryInfo, statErr := os.Stat(entryPath)
	if statErr != nil {
		return buildTarget{}, fmt.Errorf("entry file not found: %s", entryPath)
	}
	if entryInfo.IsDir() {
		return buildTarget{}, fmt.Errorf("entry path is a directory: %s", entryPath)
	}
	outputPath := strings.TrimSpace(loadedProject.File.Package.Name)
	if outputPath == "" {
		outputPath = defaultOutputNameForEntry(entryPath, targetOS)
	} else if ext := target.ExecutableExt(targetOS); ext != "" && !strings.HasSuffix(strings.ToLower(outputPath), ext) {
		outputPath += ext
	}
	return buildTarget{
		EntryPath:           entryPath,
		SelectedByDiscovery: true,
		DefaultOutputPath:   outputPath,
	}, nil
}

func defaultOutputNameForEntry(entryPath string, targetOS string) string {
	base := filepath.Base(entryPath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if ext := target.ExecutableExt(targetOS); ext != "" && !strings.HasSuffix(strings.ToLower(name), ext) {
		return name + ext
	}
	return name
}

func checkCommand(args []string) error {
	opts, err := parseCommandArgs("check", args, false)
	if err != nil {
		return err
	}

	path := "."
	if len(opts.positional) > 0 {
		path = opts.positional[0]
	}

	ctx, _ := compileEntry(path, string(backend.LLVM), false, opts.targetOS, opts.targetArch)
	if err := emitAndCheckDiagnostics(ctx); err != nil {
		return err
	}
	return nil
}

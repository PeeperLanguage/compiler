package main

import (
	"fmt"
	"os"
	"strings"

	"compiler/colors"
	"compiler/internal/backend"
	"compiler/internal/frontend/ast"
)

func main() {
	exe, _ := os.Executable()
	if strings.Contains(exe, "go-build") {
		fmt.Println("run compiled program instead of 'go run'")
		os.Exit(1)
	}

	if parseAndRunCommand(os.Args[1:]) {
		return
	}

	opts := parseCompilerFlags()

	ctx, entry := compileEntry(opts.inputPath, opts.backend, opts.debugBuild)
	if ctx == nil || ctx.Diagnostics == nil {
		colors.RED.Fprintln(os.Stderr, "compiler diagnostics unavailable")
		os.Exit(1)
	}
	if diags := ctx.Diagnostics.Diagnostics(); len(diags) > 0 {
		ctx.Diagnostics.EmitAll()
	}
	if ctx.Diagnostics.HasErrors() {
		os.Exit(1)
	}

	if opts.keepGen {
		if err := saveIRs(ctx, opts.backend, "_gen"); err != nil {
			colors.RED.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		colors.GREEN.Fprintln(os.Stdout, "Generated artifacts in _gen")
	}

	if opts.outputPath != "" {
		if err := buildExecutable(ctx, entry, opts.outputPath, backend.BACKEND_TYPE(opts.backend)); err != nil {
			colors.RED.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		colors.GREEN.Fprintf(os.Stdout, "Built %s\n", opts.outputPath)
		return
	}

	if entry != nil && entry.AST != nil {
		for _, decl := range entry.AST.Decls {
			fmt.Println(ast.DeclSummary(decl))
		}
		return
	}

	for _, mod := range ctx.Modules() {
		fmt.Printf("module %s\n", mod.ImportPath)
		if mod.AST == nil {
			continue
		}
		for _, decl := range mod.AST.Decls {
			fmt.Printf("  %s\n", ast.DeclSummary(decl))
		}
	}
}

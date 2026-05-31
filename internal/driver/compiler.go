package compiler

import (
	"compiler/core/diagnostics"
	"compiler/internal/context"
	"compiler/internal/pipeline"
	"compiler/internal/prelude"
	"os"
	"path/filepath"
	"strings"
)

const COMPILER_VERSION = "0.1.0"

const SOURCE_EXT = ".em"

// NewContext configures shared compiler state and loads the prelude.
func NewContext(cfg context.Config, diag *diagnostics.DiagnosticBag) *context.CompilerContext {
	ctx := context.NewWithConfig(cfg, diag)
	if err := prelude.Load(ctx); err != nil {
		ctx.Diagnostics.Add(diagnostics.NewError(err.Error()))
	}
	return ctx
}

// ParseFile loads one entry file and runs the pipeline against the provided context.
func ParseFile(ctx *context.CompilerContext, path string) *context.Module {
	if ctx == nil {
		return nil
	}
	diag := ctx.Diagnostics
	if diag == nil {
		diag = diagnostics.NewDiagnosticBag(path)
		ctx.Diagnostics = diag
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		diag.Add(diagnostics.NewError("resolve input path: " + err.Error()))
		return nil
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		diag.Add(diagnostics.NewError("read input file: " + err.Error()))
		return nil
	}
	module := &context.Module{
		Key:        context.ModuleKeyFor(context.ModuleOriginLocal, absPath),
		ImportPath: strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath)),
		FilePath:   absPath,
		IsEntry:    true,
		Origin:     context.ModuleOriginLocal,
		Content:    string(content),
	}
	if importPath, err := ctx.ImportPathForFile(context.ModuleOriginLocal, absPath); err == nil {
		module.ImportPath = importPath
	}
	if err := pipeline.New(ctx).Run(module); err != nil {
		diag.Add(diagnostics.NewError("pipeline run: " + err.Error()))
		return nil
	}
	return module
}

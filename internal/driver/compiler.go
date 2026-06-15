package compiler

import (
	"compiler/internal/diagnostics"
	"compiler/internal/pipeline"
	"compiler/internal/prelude"
	"compiler/internal/project"
	"os"
	"path/filepath"
	"strings"
)

const COMPILER_VERSION = "0.1.0"

const SOURCE_EXT = ".peep"

// NewContext configures shared compiler state and loads the prelude.
func NewContext(cfg project.Config, diag *diagnostics.DiagnosticBag) *project.CompilerContext {
	ctx := project.NewWithConfig(cfg, diag)
	if err := prelude.Load(ctx); err != nil {
		ctx.Diagnostics.Add(diagnostics.NewError(err.Error()))
	}
	return ctx
}

// ParseFile loads one entry file and runs the pipeline against the provided project.
func ParseFile(ctx *project.CompilerContext, path string) *project.Module {
	return ParseFileWithOverlay(ctx, path, "")
}

// ParseFileWithOverlay compiles the entry file using in-memory content instead of reading from disk if content is provided.
func ParseFileWithOverlay(ctx *project.CompilerContext, path string, content string) *project.Module {
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
	if content == "" {
		data, err := os.ReadFile(absPath)
		if err != nil {
			diag.Add(diagnostics.NewError("read input file: " + err.Error()))
			return nil
		}
		content = string(data)
	}
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, absPath),
		ImportPath: strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath)),
		FilePath:   absPath,
		IsEntry:    true,
		Origin:     project.ModuleOriginLocal,
		Content:    content,
	}
	if importPath, err := ctx.ImportPathForFile(project.ModuleOriginLocal, "", absPath); err == nil {
		module.ImportPath = importPath
	}
	if err := pipeline.New(ctx).Run(module); err != nil {
		diag.Add(diagnostics.NewError("pipeline run: " + err.Error()))
		return nil
	}
	return module
}

// AddOverlay registers a virtual/in-memory module in the compiler context.
func AddOverlay(ctx *project.CompilerContext, path string, content string) {
	if ctx == nil {
		return
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return
	}
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, absPath),
		ImportPath: strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath)),
		FilePath:   absPath,
		Origin:     project.ModuleOriginLocal,
		Content:    content,
	}
	if importPath, err := ctx.ImportPathForFile(project.ModuleOriginLocal, "", absPath); err == nil {
		module.ImportPath = importPath
	}
	ctx.AddModule(module)
}

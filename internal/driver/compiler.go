package compiler

import (
	"os"
	"path/filepath"

	"compiler/internal/diagnostics"
	"compiler/internal/phase"
	"compiler/internal/pipeline"
	"compiler/internal/prelude"
	"compiler/internal/project"
)

// NewCompilerContext configures shared compiler state and loads the prelude.
func NewCompilerContext(cfg project.Config, diag *diagnostics.DiagnosticBag) *project.CompilerContext {
	ctx := project.NewWithConfig(cfg, diag)
	if err := prelude.Load(ctx); err != nil {
		ctx.Diagnostics.AppendPhase(phase.Setup, "").Add(diagnostics.NewError(err.Error()))
	}
	return ctx
}

// CompileFile compiles the entry file from overlay when non-nil, otherwise it
// reads the source from disk.
func CompileFile(ctx *project.CompilerContext, path string, overlay *string) *project.Module {
	if ctx == nil {
		return nil
	}
	diag := ctx.Diagnostics
	if diag == nil {
		diag = diagnostics.NewDiagnosticBag()
		ctx.Diagnostics = diag
	}
	loadDiag := diag.BeginPhase(phase.Load, "")
	absPath, err := filepath.Abs(path)
	if err != nil {
		loadDiag.Add(diagnostics.NewError("resolve input path: " + err.Error()))
		return nil
	}
	content := ""
	if overlay == nil {
		data, err := os.ReadFile(absPath)
		if err != nil {
			loadDiag.Add(diagnostics.NewError("read input file: " + err.Error()))
			return nil
		}
		content = string(data)
	} else {
		content = *overlay
	}
	if module, ok := prelude.ModuleForFile(ctx, absPath, content); ok {
		module.IsEntry = true
		if err := pipeline.New(ctx).Run(module); err != nil {
			loadDiag.Add(diagnostics.NewError("pipeline run: " + err.Error()))
			return nil
		}
		return module
	}
	module := ctx.NewModuleForFile(absPath, content)
	if module != nil {
		module.IsEntry = true
	}
	if err := pipeline.New(ctx).Run(module); err != nil {
		loadDiag.Add(diagnostics.NewError("pipeline run: " + err.Error()))
		return nil
	}
	return module
}

// AddSource registers a virtual/in-memory module in the compiler context.
func AddSource(ctx *project.CompilerContext, path string, content string) {
	if ctx == nil {
		return
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return
	}
	if module, ok := prelude.ModuleForFile(ctx, absPath, content); ok {
		ctx.AddModule(module)
		return
	}
	ctx.AddModule(ctx.NewModuleForFile(absPath, content))
}

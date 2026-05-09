package compiler

import (
	"compiler/internal/context"
	"compiler/core/diagnostics"
)

const COMPILER_VERSION = "0.1.0"

const SOURCE_EXT = ".fer"



type Compiler struct {
	ctx      *context.CompilerContext
	pipeline *pipeline.Pipeline
}

func New(rootDir, extension string, diag *diagnostics.DiagnosticBag) *Compiler {
	cfg := context.Config{
		RootDir:   rootDir,
		Extension: extension,
	}
	return NewWithConfig(cfg, diag)
}

func NewWithConfig(cfg context.Config, diag *diagnostics.DiagnosticBag) *Compiler {
	ctx := context.NewWithConfig(cfg, diag)
	if err := prelude.Load(ctx); err != nil {
		ctx.Diagnostics.Add(diagnostics.NewError(err.Error()))
	}
	return &Compiler{ctx: ctx, pipeline: pipeline.New(ctx)}
}
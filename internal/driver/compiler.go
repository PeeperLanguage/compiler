package compiler

import (
	"compiler/core/diagnostics"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/pipeline"
	"compiler/internal/prelude"
	"os"
	"path/filepath"
	"strings"
)

const COMPILER_VERSION = "0.1.0"

const SOURCE_EXT = ".em"

// Top-level orchestration for one compilation session.
type Compiler struct {
	// Shared graph, diagnostics, config, and semantic state.
	ctx *context.CompilerContext
	// Ordered compiler phases.
	pipeline *pipeline.Pipeline
}

// Driver-facing module output.
type CompiledModule struct {
	// Matches context.Module.Key.
	Key string
	// Language module path.
	ImportPath string
	// Source file.
	FilePath string
	// Parsed syntax tree.
	AST *ast.Module
	// High-level IR.
	HIR string
	// Mid-level IR.
	MIR string
	// LLVM backend IR.
	LLVMIR string
}

// Result returned to command-level callers.
type ParseResult struct {
	// Shared phase diagnostics.
	Diagnostics *diagnostics.DiagnosticBag
	// Selected entry module.
	Module *CompiledModule
	// All compiled modules, including prelude.
	Modules []*CompiledModule
}

// Constructor for simple root/extension call sites.
func New(rootDir, extension string, diag *diagnostics.DiagnosticBag) *Compiler {
	cfg := context.Config{
		RootDir:   rootDir,
		Extension: extension,
	}
	return NewWithConfig(cfg, diag)
}

// Constructor with full compiler config.
func NewWithConfig(cfg context.Config, diag *diagnostics.DiagnosticBag) *Compiler {
	ctx := context.NewWithConfig(cfg, diag)
	if err := prelude.Load(ctx); err != nil {
		ctx.Diagnostics.Add(diagnostics.NewError(err.Error()))
	}
	return &Compiler{ctx: ctx, pipeline: pipeline.New(ctx)}
}

// Shared compiler context.
func (c *Compiler) Context() *context.CompilerContext {
	return c.ctx
}

// Load one entry file and run the central pipeline.
func (c *Compiler) ParseFile(path string) ParseResult {
	if c == nil || c.ctx == nil {
		result := ParseResult{Diagnostics: diagnostics.NewDiagnosticBag(path)}
		result.Diagnostics.Add(diagnostics.NewError("compiler is not initialized"))
		return result
	}
	result := ParseResult{Diagnostics: c.ctx.Diagnostics}
	if result.Diagnostics == nil {
		result.Diagnostics = diagnostics.NewDiagnosticBag(path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		result.Diagnostics.Add(diagnostics.NewError("resolve input path: " + err.Error()))
		return result
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		result.Diagnostics.Add(diagnostics.NewError("read input file: " + err.Error()))
		return result
	}
	module := &context.Module{
		Key:        "local:" + filepath.ToSlash(absPath),
		ImportPath: strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath)),
		FilePath:   absPath,
		IsEntry:    true,
		Origin:     context.ModuleOriginLocal,
		Content:    string(content),
	}
	pipelineResult := c.pipeline.Run(module)
	for _, stage := range pipelineResult.Stages {
		out := &CompiledModule{
			Key:        stage.Module.Key,
			ImportPath: stage.Module.ImportPath,
			FilePath:   stage.Module.FilePath,
			AST:        stage.AST,
			HIR:        stage.HIRText,
			MIR:        stage.MIRText,
			LLVMIR:     stage.LLVMIR,
		}
		result.Modules = append(result.Modules, out)
		if stage.Module.Key == pipelineResult.EntryKey {
			result.Module = out
		}
	}
	return result
}

package pipeline

import (
	"compiler/core/diagnostics"
	"compiler/internal/analysis/cfg"
	"compiler/internal/analysis/semantics/collector"
	"compiler/internal/analysis/semantics/resolver"
	"compiler/internal/analysis/semantics/typechecher"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir/hir_fold"
	"compiler/internal/ir/hir_lower"
	"compiler/internal/ir/llvm"
	"compiler/internal/ir/mir"
	"compiler/internal/tokens"
	"errors"
)

// Phase outputs for one module.
type StageArtifacts struct {
	// Shared source identity and graph data.
	Module *context.Module
	// Lexer output.
	Tokens []tokens.Token
	// Parsed syntax tree.
	AST *ast.Module
	// Semantic analysis completed.
	HasSem bool
	// High-level IR.
	HIRText string
	// Mid-level IR.
	MIRText string
	// LLVM backend IR.
	LLVMIR string
}

// Complete output of one pipeline run.
type Result struct {
	EntryKey string
}

// Ordered phase execution for one compiler context.
type Pipeline struct {
	ctx *context.CompilerContext
}

// Bind a pipeline to shared compiler state.
func New(ctx *context.CompilerContext) *Pipeline {
	return &Pipeline{ctx: ctx}
}

// Run the central lex -> parse -> analyze -> HIR -> MIR -> LLVM flow.
func (p *Pipeline) Run(entry *context.Module) error {
	if p == nil || p.ctx == nil || entry == nil {
		return errors.New("empty pipeline")
	}

	p.ctx.AddModule(entry)
	var diag *diagnostics.DiagnosticBag
	if p.ctx != nil {
		diag = p.ctx.Diagnostics
	}

	for _, module := range p.ctx.Modules() {

		module.Tokens = lexer.Lex(module.FilePath, module.Content, diag)
		
		module.AST = parser.ParseModule(module.FilePath, module.Tokens, diag)
		collector.Collect(p.ctx, module)
		resolver.Resolve(p.ctx, module)
		typechecher.Check(p.ctx, module)
		
		modhir := hir_lower.GenerateHIR(module)
		if modhir == nil {
			continue
		}
		modhir = hir_fold.ApplyConstantFolding(modhir, diag)
		module.HIR = modhir
		cfg.AnalyzeModule(modhir, diag)
		module.HIR = modhir
		if diag != nil && diag.HasErrors() {
			continue
		}

		modmir := mir.GenerateMIR(module.HIR)

		module.MIR = modmir
		module.LLVMIR = llvm.GenerateLLVMIR(modmir)
	}
	return nil
}

package pipeline

import (
	"compiler/core/diagnostics"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/tokens"
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
	Stages   map[string]*StageArtifacts
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
func (p *Pipeline) Run(entry *context.Module) Result {
	result := Result{Stages: make(map[string]*StageArtifacts)}
	if p == nil || p.ctx == nil || entry == nil {
		return result
	}

	p.ctx.UpsertModule(entry)
	result.EntryKey = entry.Key
	var diag *diagnostics.DiagnosticBag
	if p.ctx != nil {
		diag = p.ctx.Diagnostics
	}

	for _, module := range p.ctx.Modules() {
		stage := &StageArtifacts{Module: module}
		stage.Tokens = lex(module, diag)
		stage.AST = parse(module, stage.Tokens, diag)
		analyze(p.ctx, module, diag)
		stage.HasSem = true
		_, hirText := lowerHIR(module, diag)
		stage.HIRText = hirText
		if diag != nil && diag.HasErrors() {
			result.Stages[module.Key] = stage
			continue
		}
		_, mirText := lowerMIR(module)
		stage.MIRText = mirText
		stage.LLVMIR = lowerLLVMIR(module)
		result.Stages[module.Key] = stage
	}
	return result
}

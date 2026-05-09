package pipeline

import (
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/tokens"
)

type StageArtifacts struct {
	Module  *context.Module
	Tokens  []tokens.Token
	AST     *ast.Module
	HasSem  bool
	HIRText string
	MIRText string
	LLVMIR  string
}

type Result struct {
	EntryKey string
	Stages   map[string]*StageArtifacts
}

type Pipeline struct {
	ctx *context.CompilerContext
}

func New(ctx *context.CompilerContext) *Pipeline {
	return &Pipeline{ctx: ctx}
}

func (p *Pipeline) Run(entry *context.Module) Result {
	result := Result{Stages: make(map[string]*StageArtifacts)}
	if p == nil || p.ctx == nil || entry == nil {
		return result
	}

	p.ctx.UpsertModule(entry)
	result.EntryKey = entry.Key

	for _, module := range p.ctx.Modules() {
		stage := &StageArtifacts{Module: module}
		stage.Tokens = lex(module)
		stage.AST = parse(module, stage.Tokens)
		stage.HasSem = analyze(module, stage.AST)
		stage.HIRText = lowerHIR(module, stage.AST)
		stage.MIRText = lowerMIR(module, stage.HIRText)
		stage.LLVMIR = lowerLLVMIR(module, stage.MIRText)
		result.Stages[module.Key] = stage
	}
	return result
}

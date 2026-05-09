package pipeline

import (
	"compiler/core/source"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/tokens"
)

func lex(module *context.Module) []tokens.Token {
	if module == nil {
		return nil
	}
	pos := source.NewPosition()
	return []tokens.Token{{
		Kind:    tokens.EOF,
		Literal: "",
		Start:   pos,
		End:     pos,
	}}
}

func parse(module *context.Module, _ []tokens.Token) *ast.Module {
	if module == nil {
		return nil
	}
	return &ast.Module{
		FilePath: module.FilePath,
		Decls:    make([]ast.Decl, 0),
		Imports:  make([]*ast.ImportDecl, 0),
	}
}

func analyze(_ *context.Module, _ *ast.Module) bool {
	return true
}

func lowerHIR(module *context.Module, _ *ast.Module) string {
	if module == nil {
		return ""
	}
	return "; hir module " + module.ImportPath + "\n"
}

func lowerMIR(module *context.Module, _ string) string {
	if module == nil {
		return ""
	}
	return "; mir module " + module.ImportPath + "\n"
}

func lowerLLVMIR(module *context.Module, _ string) string {
	if module == nil {
		return ""
	}
	return "; llvm ir module " + module.ImportPath + "\n"
}

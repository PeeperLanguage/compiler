package pipeline

import (
	"compiler/core/diagnostics"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/tokens"
)

// Source text to tokens.
func lex(module *context.Module, diag *diagnostics.DiagnosticBag) []tokens.Token {
	if module == nil {
		return nil
	}
	return lexer.Lex(module.FilePath, module.Content, diag)
}

// Tokens to frontend AST.
// Should return partial ASTs after recoverable syntax errors.
func parse(module *context.Module, stream []tokens.Token, diag *diagnostics.DiagnosticBag) *ast.Module {
	if module == nil {
		return nil
	}
	return parser.ParseModule(module.FilePath, stream, diag)
}

// Collector, resolver, type checker, CTFE, and related semantic passes.
func analyze(_ *context.Module, _ *ast.Module) bool {
	return true
}

// Checked AST/semantic data to high-level IR.
func lowerHIR(module *context.Module, _ *ast.Module) string {
	if module == nil {
		return ""
	}
	return "; hir module " + module.ImportPath + "\n"
}

// HIR to target-independent mid-level IR.
func lowerMIR(module *context.Module, _ string) string {
	if module == nil {
		return ""
	}
	return "; mir module " + module.ImportPath + "\n"
}

// MIR to LLVM IR.
func lowerLLVMIR(module *context.Module, _ string) string {
	if module == nil {
		return ""
	}
	return "; llvm ir module " + module.ImportPath + "\n"
}

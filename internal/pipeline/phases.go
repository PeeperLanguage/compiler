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
	"compiler/internal/ir/hir"
	"compiler/internal/ir/hir_fold"
	"compiler/internal/ir/hir_lower"
	"compiler/internal/ir/mir"
	"compiler/internal/tokens"
)

// Source text to tokens.
func lex(module *context.Module, diag *diagnostics.DiagnosticBag) []tokens.Token {
	if module == nil {
		return nil
	}
	module.Tokens = lexer.Lex(module.FilePath, module.Content, diag)
	return module.Tokens
}

// Tokens to frontend AST.
// Should return partial ASTs after recoverable syntax errors.
func parse(module *context.Module, stream []tokens.Token, diag *diagnostics.DiagnosticBag) *ast.Module {
	if module == nil {
		return nil
	}
	module.AST = parser.ParseModule(module.FilePath, stream, diag)
	return module.AST
}

// Collector, resolver, type checker, CTFE, and related semantic passes.
func analyze(ctx *context.CompilerContext, module *context.Module, diag *diagnostics.DiagnosticBag) bool {
	if !collector.Collect(ctx, module, diag) {
		return false
	}
	if !resolver.Resolve(module, diag) {
		return false
	}
	return typechecher.Check(module, diag)
}

// Checked AST/semantic data to high-level IR.
func lowerHIR(module *context.Module, diag *diagnostics.DiagnosticBag) (*hir.Module, string) {
	if module == nil || module.Types == nil {
		return nil, ""
	}
	mod := hir_lower.LowerTyped(module)
	if mod == nil {
		return nil, ""
	}
	mod = hir_fold.FoldModule(mod, diag)
	module.HIR = mod
	cfg.CheckReturns(mod, diag)
	return mod, mod.Text()
}

// HIR to target-independent mid-level IR.
func lowerMIR(module *context.Module) (*mir.Module, string) {
	if module == nil || module.HIR == nil {
		return nil, ""
	}
	hirMod, ok := module.HIR.(*hir.Module)
	if !ok || hirMod == nil {
		return nil, ""
	}
	mod := mir.LowerHIR(hirMod)
	if mod == nil {
		return nil, ""
	}
	module.MIR = mod
	return mod, mod.Text()
}

// MIR to LLVM IR.
func lowerLLVMIR(module *context.Module) string {
	if module == nil || module.MIR == nil {
		return ""
	}
	mirMod, ok := module.MIR.(*mir.Module)
	if !ok || mirMod == nil {
		return ""
	}
	return lowerLLVMFromMIR(mirMod)
}

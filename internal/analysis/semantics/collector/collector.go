package collector

import (
	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/declinfo"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
)

func Collect(ctx *context.CompilerContext, module *context.Module, diag *diagnostics.DiagnosticBag) bool {
	if ctx == nil || module == nil || module.AST == nil {
		return false
	}
	module.ModuleScope = table.New(ctx.GlobalScope)
	module.Decls = &declinfo.ModuleInfo{
		Functions: make([]*declinfo.Function, 0),
		Externs:   make([]declinfo.ExternDecl, 0),
	}
	module.Bindings = nil
	module.Types = nil
	for _, decl := range module.AST.Decls {
		fn, ok := decl.(*ast.FnDecl)
		if !ok {
			continue
		}
		if fn.Name == nil || fn.Name.Name == "" {
			common.AddError(diag, module.FilePath, fn, diagnostics.ErrMissingIdentifier, "function name required")
			return false
		}
		kind := symbols.SymbolFunc
		if fn.Body == nil {
			kind = symbols.SymbolUnknown
		}
		sym := symbols.New(fn.Name.Name, kind, fn)
		if !module.ModuleScope.Declare(sym) {
			common.AddError(diag, module.FilePath, fn, diagnostics.ErrRedeclaredSymbol, "duplicate function `"+fn.Name.Name+"`")
			return false
		}
		if fn.Body == nil {
			module.Decls.Externs = append(module.Decls.Externs, declinfo.ExternDecl{Symbol: sym, Decl: fn})
			continue
		}
		module.Decls.Functions = append(module.Decls.Functions, &declinfo.Function{
			Symbol: sym,
			Decl:   fn,
			Scope:  table.New(module.ModuleScope),
		})
	}
	return true
}

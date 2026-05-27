package resolver

import (
	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/binding"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
)

func Resolve(module *context.Module, diag *diagnostics.DiagnosticBag) bool {
	if module == nil || module.Decls == nil {
		return false
	}
	module.Bindings = binding.NewModuleInfo()
	module.Types = nil
	if len(module.Decls.Functions) == 0 {
		return true
	}
	for _, collectedFn := range module.Decls.Functions {
		if collectedFn == nil || collectedFn.Decl == nil || collectedFn.Scope == nil {
			continue
		}
		module.Bindings.BindFunctionSymbol(collectedFn.Decl, collectedFn.Symbol)
		if collectedFn.Decl.Name != nil && collectedFn.Symbol != nil {
			module.Bindings.BindNode(collectedFn.Decl.Name, &binding.Resolution{
				Kind:   binding.ResolutionSymbol,
				Symbol: collectedFn.Symbol,
			})
		}
		for _, param := range collectedFn.Decl.Params {
			if param.Name == nil || param.Name.Name == "" {
				common.AddError(diag, module.FilePath, collectedFn.Decl, diagnostics.ErrMissingIdentifier, "parameter name required")
				return false
			}
			sym := symbols.New(param.Name.Name, symbols.SymbolParam, param.Name)
			if !collectedFn.Scope.Declare(sym) {
				common.AddError(diag, module.FilePath, param.Name, diagnostics.ErrRedeclaredSymbol, "duplicate parameter `"+param.Name.Name+"`")
				return false
			}
			module.Bindings.AddFunctionLocal(collectedFn.Decl, sym)
			module.Bindings.BindNode(param.Name, &binding.Resolution{
				Kind:   binding.ResolutionSymbol,
				Symbol: sym,
			})
		}
		if !resolveBlock(module, collectedFn.Decl, collectedFn.Scope, collectedFn.Decl.Body, diag) {
			return false
		}
	}
	return true
}

func resolveBlock(module *context.Module, fn *ast.FnDecl, scope *table.Scope, block *ast.BlockStmt, diag *diagnostics.DiagnosticBag) bool {
	if block == nil {
		return true
	}
	for _, stmt := range block.Stmts {
		if !resolveStmt(module, fn, scope, stmt, diag) {
			return false
		}
	}
	return true
}

func resolveStmt(module *context.Module, fn *ast.FnDecl, scope *table.Scope, stmt ast.Stmt, diag *diagnostics.DiagnosticBag) bool {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		return resolveBlock(module, fn, table.New(scope), node, diag)
	case *ast.LetDecl:
		if !resolveExpr(module, scope, node.Value, diag) {
			return false
		}
		sym := symbols.New(node.Name.Name, symbols.SymbolVar, node)
		if !scope.Declare(sym) {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrRedeclaredSymbol, "duplicate binding `"+node.Name.Name+"`")
			return false
		}
		module.Bindings.AddFunctionLocal(fn, sym)
		if node.Name != nil {
			module.Bindings.BindNode(node.Name, &binding.Resolution{
				Kind:   binding.ResolutionSymbol,
				Symbol: sym,
			})
		}
		return true
	case *ast.ConstDecl:
		if !resolveExpr(module, scope, node.Value, diag) {
			return false
		}
		sym := symbols.New(node.Name.Name, symbols.SymbolConst, node)
		if !scope.Declare(sym) {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrRedeclaredSymbol, "duplicate binding `"+node.Name.Name+"`")
			return false
		}
		module.Bindings.AddFunctionLocal(fn, sym)
		if node.Name != nil {
			module.Bindings.BindNode(node.Name, &binding.Resolution{
				Kind:   binding.ResolutionSymbol,
				Symbol: sym,
			})
		}
		return true
	case *ast.ReturnStmt:
		if node.Value == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
			return false
		}
		return resolveExpr(module, scope, node.Value, diag)
	case *ast.IfStmt:
		if node.Cond == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
			return false
		}
		if !resolveExpr(module, scope, node.Cond, diag) {
			return false
		}
		if !resolveBlock(module, fn, table.New(scope), node.Then, diag) {
			return false
		}
		if node.Else == nil {
			return true
		}
		if elseBlock, ok := node.Else.(*ast.BlockStmt); ok {
			return resolveBlock(module, fn, table.New(scope), elseBlock, diag)
		}
		return resolveStmt(module, fn, scope, node.Else, diag)
	case *ast.ExprStmt:
		return resolveExpr(module, scope, node.Expr, diag)
	default:
		common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidStatement, "unsupported statement for arithmetic flow")
		return false
	}
}

func resolveExpr(module *context.Module, scope *table.Scope, expr ast.Expr, diag *diagnostics.DiagnosticBag) bool {
	switch node := expr.(type) {
	case *ast.NumberLit:
		return true
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrUndefinedSymbol, "unknown identifier `"+node.Name+"`")
			return false
		}
		module.Bindings.BindNode(node, &binding.Resolution{
			Kind:   binding.ResolutionSymbol,
			Symbol: sym,
		})
		return true
	case *ast.UnaryExpr:
		return resolveExpr(module, scope, node.Expr, diag)
	case *ast.BinaryExpr:
		return resolveExpr(module, scope, node.Left, diag) && resolveExpr(module, scope, node.Right, diag)
	default:
		common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidExpression, "unsupported expression for arithmetic flow")
		return false
	}
}

package resolver

import (
	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/binding"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/declinfo"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
)

type resolver struct {
	module *context.Module
	diag   *diagnostics.DiagnosticBag
}

func (r *resolver) resolveModule() bool {
	if r == nil || r.module == nil || r.module.Decls == nil {
		return false
	}
	r.module.Bindings = binding.NewModuleInfo()
	r.module.Types = nil
	for _, collectedFn := range r.module.Decls.Functions {
		if collectedFn == nil || collectedFn.Decl == nil || collectedFn.Scope == nil {
			continue
		}
		if !r.resolveFunction(collectedFn) {
			return false
		}
	}
	return true
}

func (r *resolver) resolveFunction(fn *declinfo.Function) bool {
	if r == nil || r.module == nil || fn == nil || fn.Decl == nil || fn.Scope == nil {
		return false
	}
	r.module.Bindings.BindFunctionSymbol(fn.Decl, fn.Symbol)
	if fn.Decl.Name != nil && fn.Symbol != nil {
		r.module.Bindings.BindNode(fn.Decl.Name, &binding.Resolution{
			Kind:   binding.ResolutionSymbol,
			Symbol: fn.Symbol,
		})
	}
	for _, param := range fn.Decl.Params {
		if param.Name == nil || param.Name.Name == "" {
			common.AddError(r.diag, r.module.FilePath, fn.Decl, diagnostics.ErrMissingIdentifier, "parameter name required")
			return false
		}
		sym := symbols.New(param.Name.Name, symbols.SymbolParam, param.Name)
		if !fn.Scope.Declare(sym) {
			common.AddError(r.diag, r.module.FilePath, param.Name, diagnostics.ErrRedeclaredSymbol, "duplicate parameter `"+param.Name.Name+"`")
			return false
		}
		r.module.Bindings.AddFunctionLocal(fn.Decl, sym)
		r.module.Bindings.BindNode(param.Name, &binding.Resolution{
			Kind:   binding.ResolutionSymbol,
			Symbol: sym,
		})
	}
	return r.resolveBlock(fn, fn.Scope, fn.Decl.Body)
}

func (r *resolver) resolveBlock(fn *declinfo.Function, scope *table.Scope, block *ast.BlockStmt) bool {
	if block == nil {
		return true
	}
	for _, stmt := range block.Stmts {
		if !r.resolveStmt(fn, scope, stmt) {
			return false
		}
	}
	return true
}

func (r *resolver) resolveStmt(fn *declinfo.Function, scope *table.Scope, stmt ast.Stmt) bool {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		return r.resolveBlock(fn, table.New(scope), node)
	case *ast.LetDecl:
		if !r.resolveExpr(fn, scope, node.Value) {
			return false
		}
		sym := symbols.New(node.Name.Name, symbols.SymbolVar, node)
		if !scope.Declare(sym) {
			common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrRedeclaredSymbol, "duplicate binding `"+node.Name.Name+"`")
			return false
		}
		r.module.Bindings.AddFunctionLocal(fn.Decl, sym)
		if node.Name != nil {
			r.module.Bindings.BindNode(node.Name, &binding.Resolution{
				Kind:   binding.ResolutionSymbol,
				Symbol: sym,
			})
		}
		return true
	case *ast.ConstDecl:
		if !r.resolveExpr(fn, scope, node.Value) {
			return false
		}
		sym := symbols.New(node.Name.Name, symbols.SymbolConst, node)
		if !scope.Declare(sym) {
			common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrRedeclaredSymbol, "duplicate binding `"+node.Name.Name+"`")
			return false
		}
		r.module.Bindings.AddFunctionLocal(fn.Decl, sym)
		if node.Name != nil {
			r.module.Bindings.BindNode(node.Name, &binding.Resolution{
				Kind:   binding.ResolutionSymbol,
				Symbol: sym,
			})
		}
		return true
	case *ast.ReturnStmt:
		if node.Value == nil {
			common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
			return false
		}
		return r.resolveExpr(fn, scope, node.Value)
	case *ast.IfStmt:
		if node.Cond == nil {
			common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
			return false
		}
		if !r.resolveExpr(fn, scope, node.Cond) {
			return false
		}
		if !r.resolveBlock(fn, table.New(scope), node.Then) {
			return false
		}
		if node.Else == nil {
			return true
		}
		if elseBlock, ok := node.Else.(*ast.BlockStmt); ok {
			return r.resolveBlock(fn, table.New(scope), elseBlock)
		}
		return r.resolveStmt(fn, scope, node.Else)
	case *ast.ExprStmt:
		return r.resolveExpr(fn, scope, node.Expr)
	default:
		common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrInvalidStatement, "unsupported statement for arithmetic flow")
		return false
	}
}

func (r *resolver) resolveExpr(fn *declinfo.Function, scope *table.Scope, expr ast.Expr) bool {
	switch node := expr.(type) {
	case *ast.NumberLit:
		return true
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok {
			return reportUnresolved(r.module, r.module.Decls, fn, scope, node, r.diag)
		}
		r.module.Bindings.BindNode(node, &binding.Resolution{
			Kind:   binding.ResolutionSymbol,
			Symbol: sym,
		})
		return true
	case *ast.UnaryExpr:
		return r.resolveExpr(fn, scope, node.Expr)
	case *ast.BinaryExpr:
		return r.resolveExpr(fn, scope, node.Left) && r.resolveExpr(fn, scope, node.Right)
	default:
		common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrInvalidExpression, "unsupported expression for arithmetic flow")
		return false
	}
}

func Resolve(module *context.Module, diag *diagnostics.DiagnosticBag) bool {
	if module == nil || module.Decls == nil || diag == nil {
		return false
	}
	r := &resolver{module: module, diag: diag}
	return r.resolveModule()
}
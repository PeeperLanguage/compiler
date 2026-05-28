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

func (r *resolver) resolveModule() {
	if r == nil || r.module == nil || r.module.Decls == nil {
		return
	}
	r.module.Bindings = binding.NewModuleInfo()
	r.module.Types = nil
	for _, collectedFn := range r.module.Decls.Functions {
		if collectedFn == nil || collectedFn.Decl == nil || collectedFn.Scope == nil {
			continue
		}
		r.resolveFunction(collectedFn)
	}
}

func (r *resolver) resolveFunction(fn *declinfo.Function) {
	if r == nil || r.module == nil || fn == nil || fn.Decl == nil || fn.Scope == nil {
		return
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
			return
		}
		sym := symbols.New(param.Name.Name, symbols.SymbolParam, param.Name)
		if err, ok := fn.Scope.Declare(sym); !ok {
			common.AddError(r.diag, r.module.FilePath, param.Name, diagnostics.ErrRedeclaredSymbol, err.Error())
			return
		}
		r.module.Bindings.AddFunctionLocal(fn.Decl, sym)
		r.module.Bindings.BindNode(param.Name, &binding.Resolution{
			Kind:   binding.ResolutionSymbol,
			Symbol: sym,
		})
	}
	r.resolveBlock(fn, fn.Scope, fn.Decl.Body)
}

func (r *resolver) resolveBlock(fn *declinfo.Function, scope *table.Scope, block *ast.BlockStmt) {
	if block == nil {
		return
	}
	for _, stmt := range block.Stmts {
		r.resolveStmt(fn, scope, stmt)
	}
}

func (r *resolver) resolveStmt(fn *declinfo.Function, scope *table.Scope, stmt ast.Stmt) {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		r.resolveBlock(fn, table.New(scope), node)
	case *ast.LetDecl:
		sym := symbols.New(node.Name.Name, symbols.SymbolVar, node)
		if err, ok := scope.Declare(sym); !ok {
			common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrRedeclaredSymbol, err.Error())
			return
		}
		r.module.Bindings.AddFunctionLocal(fn.Decl, sym)
		if node.Name != nil {
			r.module.Bindings.BindNode(node.Name, &binding.Resolution{
				Kind:   binding.ResolutionSymbol,
				Symbol: sym,
			})
		}
		if node.Value == nil {
			return
		}
		r.resolveExpr(fn, scope, node.Value)
	case *ast.ConstDecl:
		sym := symbols.New(node.Name.Name, symbols.SymbolConst, node)
		if err, ok := scope.Declare(sym); !ok {
			common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrRedeclaredSymbol, err.Error())
			return
		}
		r.module.Bindings.AddFunctionLocal(fn.Decl, sym)
		if node.Name != nil {
			r.module.Bindings.BindNode(node.Name, &binding.Resolution{
				Kind:   binding.ResolutionSymbol,
				Symbol: sym,
			})
		}
		r.resolveExpr(fn, scope, node.Value)
	case *ast.ReturnStmt:
		if node.Value == nil {
			common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
			return
		}
		r.resolveExpr(fn, scope, node.Value)
	case *ast.IfStmt:
		if node.Cond == nil {
			common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
			return
		}
		r.resolveExpr(fn, scope, node.Cond)
		r.resolveBlock(fn, table.New(scope), node.Then)
		if elseBlock, ok := node.Else.(*ast.BlockStmt); ok {
			r.resolveBlock(fn, table.New(scope), elseBlock)
		}
		r.resolveStmt(fn, scope, node.Else)
	case *ast.ExprStmt:
		r.resolveExpr(fn, scope, node.Expr)
	default:
		common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrInvalidStatement, "unsupported statement for arithmetic flow")
		return
	}
}

func (r *resolver) resolveExpr(fn *declinfo.Function, scope *table.Scope, expr ast.Expr) {
	switch node := expr.(type) {
	case *ast.NumberLit:
		// nothing to look for literals
		return
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok {
			reportUnresolved(r.module, r.module.Decls, fn, scope, node, r.diag)
			return
		}
		r.module.Bindings.BindNode(node, &binding.Resolution{
			Kind:   binding.ResolutionSymbol,
			Symbol: sym,
		})
	case *ast.UnaryExpr:
		r.resolveExpr(fn, scope, node.Expr)
	case *ast.BinaryExpr:
		r.resolveExpr(fn, scope, node.Left)
		r.resolveExpr(fn, scope, node.Right)
	case *ast.CallExpr:
		r.resolveExpr(fn, scope, node.Callee)
		for _, arg := range node.Args {
			r.resolveExpr(fn, scope, arg)
		}
	case *ast.AsExpr:
		// Resolve the expression being cast
		r.resolveExpr(fn, scope, node.Expr)
		// The type expression doesn't need resolution
	default:
		common.AddError(r.diag, r.module.FilePath, node, diagnostics.ErrInvalidExpression, "unsupported expression for arithmetic flow")
	}
}

func Resolve(module *context.Module, diag *diagnostics.DiagnosticBag) {
	if module == nil || module.Decls == nil || diag == nil {
		return
	}
	r := &resolver{module: module, diag: diag}
	r.resolveModule()
}

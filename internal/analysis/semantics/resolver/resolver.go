package resolver

import (
	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
)

type resolver struct {
	ctx    *context.CompilerContext
	module *context.Module
}

func (r *resolver) resolveModule() {
	if r == nil || r.module == nil || r.module.AST == nil {
		return
	}
	if r.module.Semantics == nil {
		r.module.Semantics = context.NewSemanticInfo()
	}
	for _, decl := range r.module.AST.Decls {
		if fn, ok := decl.(*ast.FnDecl); ok && fn != nil && fn.Body != nil {
			r.resolveFunction(fn)
		}
	}
}

func (r *resolver) resolveFunction(fn *ast.FnDecl) {
	if r == nil || r.module == nil || fn == nil || fn.Body == nil {
		return
	}
	sym, found := r.module.ModuleScope.Lookup(fn.Name.Name)
	if !found || sym == nil || sym.Scope == nil {
		return
	}
	funcScope := sym.Scope.(*table.Scope)
	for _, param := range fn.Params {
		if param.Name == nil || param.Name.Name == "" {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, fn, diagnostics.ErrMissingIdentifier, "parameter name required")
			return
		}
		paramSym := symbols.New(param.Name.Name, symbols.SymbolParam, param.Name)
		if err := funcScope.Declare(paramSym); err != nil {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, param.Name, diagnostics.ErrRedeclaredSymbol, err.Error())
			return
		}
	}
	r.resolveBlock(funcScope, fn.Body)
}

func (r *resolver) resolveBlock(scope *table.Scope, block *ast.BlockStmt) {
	if block == nil {
		return
	}
	r.module.Semantics.BlockScopes[block.ID()] = scope
	for _, stmt := range block.Stmts {
		r.resolveStmt(scope, stmt)
	}
}

func (r *resolver) resolveStmt(scope *table.Scope, stmt ast.Stmt) {
	if stmt == nil {
		return
	}
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		r.resolveBlock(table.New(scope), node)
	case *ast.LetDecl:
		sym := symbols.New(node.Name.Name, symbols.SymbolVar, node)
		sym.Initializing = true
		defer func() { sym.Initializing = false }()
		if err := scope.Declare(sym); err != nil {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrRedeclaredSymbol, err.Error())
			return
		}
		if node.Value != nil {
			r.resolveExpr(scope, node.Value)
		}
	case *ast.ConstDecl:
		sym := symbols.New(node.Name.Name, symbols.SymbolConst, node)
		sym.Initializing = true
		defer func() { sym.Initializing = false }()
		if err := scope.Declare(sym); err != nil {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrRedeclaredSymbol, err.Error())
			return
		}
		if node.Value != nil {
			r.resolveExpr(scope, node.Value)
		}
	case *ast.ReturnStmt:
		if node.Value == nil {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
			return
		}
		r.resolveExpr(scope, node.Value)
	case *ast.IfStmt:
		if node.Cond == nil {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
			return
		}
		r.resolveExpr(scope, node.Cond)
		r.resolveBlock(table.New(scope), node.Then)
		if elseBlock, ok := node.Else.(*ast.BlockStmt); ok {
			r.resolveBlock(table.New(scope), elseBlock)
		} else if node.Else != nil {
			r.resolveStmt(scope, node.Else)
		}
	case *ast.ExprStmt:
		r.resolveExpr(scope, node.Expr)
	default:
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrInvalidStatement, "unsupported statement")
	}
}

func (r *resolver) resolveExpr(scope *table.Scope, expr ast.Expr) {
	switch node := expr.(type) {
	case *ast.NumberLit:
		return
	case *ast.StringLit:
		return
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if ok && sym != nil {
			sym.Used = true
			if sym.Kind == symbols.SymbolImport {
				common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrInvalidExpression,
					"import alias must be qualified with `::`")
				return
			}
			if sym.Initializing {
				msg := "symbol `" + node.Name + "` used before it's defined"
				r.ctx.Diagnostics.Add(
					diagnostics.NewError(msg).
						WithCode(diagnostics.ErrUseBeforeDecl).
						WithPrimaryLabel(node.Loc(), msg).
						WithHelp("rename binding or use earlier value"),
				)
				return
			}
			return
		}
		reportUnresolved(r.module, scope, node, r.ctx.Diagnostics)
	case *ast.ScopeResolution:
		if r.resolveScopeResolution(node) {
			return
		}
	case *ast.UnaryExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.BorrowExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.BinaryExpr:
		r.resolveExpr(scope, node.Left)
		r.resolveExpr(scope, node.Right)
	case *ast.CallExpr:
		r.resolveExpr(scope, node.Callee)
		for _, arg := range node.Args {
			r.resolveExpr(scope, arg)
		}
	case *ast.AsExpr:
		r.resolveExpr(scope, node.Expr)
	default:
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrInvalidExpression, "unsupported expression type")
	}
}

func Resolve(ctx *context.CompilerContext, module *context.Module) {
	if module == nil || ctx == nil {
		return
	}
	r := &resolver{module: module, ctx: ctx}
	r.resolveModule()
}

func (r *resolver) resolveScopeResolution(node *ast.ScopeResolution) bool {
	if r == nil || r.module == nil || node == nil {
		return false
	}
	qualifier := node.Module.Name
	member := node.Name.Name
	imp, ok := r.module.Imports[qualifier]
	if !ok {
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrModuleNotFound,
			"unknown import alias `"+qualifier+"`")
		return false
	}
	mod, ok := r.ctx.ModuleByKey(imp.Key)
	if !ok || mod == nil || mod.ModuleScope == nil {
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrModuleNotFound,
			"imported module not loaded for `"+qualifier+"`")
		return false
	}
	sym, ok := mod.ModuleScope.LookupLocal(member)
	if !ok || sym == nil {
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrUndefinedSymbol,
			"unknown identifier `"+member+"` in module `"+qualifier+"`")
		return false
	}
	if !sym.IsPub {
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrSymbolNotExported,
			"`"+member+"` is not exported from `"+qualifier+"`")
		return false
	}
	sym.Used = true
	if impSym, ok := r.module.ModuleScope.LookupLocal(qualifier); ok && impSym != nil {
		impSym.Used = true
	}
	return true
}

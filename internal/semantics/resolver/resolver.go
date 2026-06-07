package resolver

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
)

type resolver struct {
	ctx    *project.CompilerContext
	module *project.Module
}

func (r *resolver) resolveModule() {
	if r == nil || r.module == nil || r.module.AST == nil {
		return
	}
	if r.module.Semantics == nil {
		r.module.Semantics = project.NewSemanticInfo()
	}
	for _, decl := range r.module.AST.Decls {
		switch node := decl.(type) {
		case *ast.FnDecl:
			if node != nil && node.Body != nil {
				r.resolveFunction(node)
			}
		case *ast.ImplDecl:
			r.resolveImpl(node)
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
			r.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "parameter name required", fn.Loc(), "")
			return
		}
		paramSym := symbols.New(param.Name.Name, symbols.SymbolParam, param.Name)
		if err := funcScope.Declare(paramSym); err != nil {
			r.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err.Error(), param.Name.Loc(), "")
			return
		}
	}
	r.resolveBlock(funcScope, fn.Body)
}

func (r *resolver) resolveMethod(sym *symbols.Symbol, fn *ast.FnDecl) {
	if r == nil || r.module == nil || sym == nil || fn == nil || fn.Body == nil || sym.Scope == nil {
		return
	}
	funcScope := sym.Scope.(*table.Scope)
	for _, param := range fn.Params {
		if param.Name == nil || param.Name.Name == "" {
			r.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "parameter name required", fn.Loc(), "")
			return
		}
		paramSym := symbols.New(param.Name.Name, symbols.SymbolParam, param.Name)
		if err := funcScope.Declare(paramSym); err != nil {
			r.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err.Error(), param.Name.Loc(), "")
			return
		}
	}
	r.resolveBlock(funcScope, fn.Body)
}

func (r *resolver) resolveImpl(decl *ast.ImplDecl) {
	if r == nil || r.module == nil || r.module.Semantics == nil || decl == nil {
		return
	}
	for _, method := range decl.Methods {
		if method == nil || method.Body == nil {
			continue
		}
		sym, ok := r.module.Semantics.MethodSymbol[method.ID()]
		if !ok || sym == nil {
			continue
		}
		r.resolveMethod(sym, method)
	}
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
			r.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err.Error(), node.Loc(), "")
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
			r.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err.Error(), node.Loc(), "")
			return
		}
		if node.Value != nil {
			r.resolveExpr(scope, node.Value)
		}
	case *ast.ReturnStmt:
		if node.Value == nil {
			r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn, "return value required", node.Loc(), "")
			return
		}
		r.resolveExpr(scope, node.Value)
	case *ast.IfStmt:
		if node.Cond == nil {
			r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement, "if condition required", node.Loc(), "")
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
	case *ast.AssignStmt:
		r.resolveExpr(scope, node.Target)
		r.resolveExpr(scope, node.Value)
	default:
		r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement, "unsupported statement", node.Loc(), "")
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
				r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression, "import alias must be qualified with `::`", node.Loc(), "")
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
	case *ast.SelectorExpr:
		r.resolveExpr(scope, node.Expr)
	case *ast.StructLit:
		for _, field := range node.Fields {
			r.resolveExpr(scope, field.Value)
		}
	case *ast.UnaryExpr:
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
		r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression, "unsupported expression type", node.Loc(), "")
	}
}

func Resolve(ctx *project.CompilerContext, module *project.Module) {
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
		r.ctx.Diagnostics.AddError(diagnostics.ErrModuleNotFound, "unknown import alias `"+qualifier+"`", node.Loc(), "")
		return false
	}
	mod, ok := r.ctx.ModuleByKey(imp.Key)
	if !ok || mod == nil || mod.ModuleScope == nil {
		r.ctx.Diagnostics.AddError(diagnostics.ErrModuleNotFound, "imported module not loaded for `"+qualifier+"`", node.Loc(), "")
		return false
	}
	sym, ok := mod.ModuleScope.LookupLocal(member)
	if !ok || sym == nil {
		r.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol, "unknown identifier `"+member+"` in module `"+qualifier+"`", node.Loc(), "")
		return false
	}
	if !sym.IsPub {
		r.ctx.Diagnostics.AddError(diagnostics.ErrSymbolNotExported, "`"+member+"` is not exported from `"+qualifier+"`", node.Loc(), "")
		return false
	}
	sym.Used = true
	if impSym, ok := r.module.ModuleScope.LookupLocal(qualifier); ok && impSym != nil {
		impSym.Used = true
	}
	return true
}

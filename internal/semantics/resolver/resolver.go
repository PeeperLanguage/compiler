package resolver

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	semantic_errors "compiler/internal/semantics/errors"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/source"
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
	r.markPendingTopLevelBindings()
	for _, stmt := range r.module.AST.Stmts {
		decl, ok := stmt.(ast.Decl) // ? Why even needed?
		if !ok {
			continue
		}
		switch node := decl.(type) {
		case *ast.LetDecl:
			r.resolveTopLevelBinding(node.Name, node.Value)
		case *ast.ConstDecl:
			r.resolveTopLevelBinding(node.Name, node.Value)
		}
	}
	for _, stmt := range r.module.AST.Stmts {
		decl, ok := stmt.(ast.Decl)
		if !ok {
			continue
		}
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

func (r *resolver) markPendingTopLevelBindings() {
	if r == nil || r.module == nil || r.module.ModuleScope == nil {
		return
	}
	for _, sym := range r.module.ModuleScope.Symbols() {
		if sym == nil {
			continue
		}
		switch sym.Kind {
		case symbols.SymbolVar, symbols.SymbolConst:
			sym.Initializing = true
		}
	}
}

func (r *resolver) resolveTopLevelBinding(name *ast.Ident, value ast.Expr) {
	if r == nil || r.module == nil || r.module.ModuleScope == nil || name == nil || name.Name == "" {
		return
	}
	sym, ok := r.module.ModuleScope.LookupLocal(name.Name)
	if !ok || sym == nil {
		return
	}
	if value != nil {
		r.resolveExpr(r.module.ModuleScope, value)
	}
	sym.Initializing = false
	sym.Initialized = value != nil
}

func (r *resolver) resolveFunction(fn *ast.FnDecl) {
	if r == nil || r.module == nil || fn == nil || fn.Body == nil {
		return
	}
	sym, found := r.module.ModuleScope.Lookup(fn.Name.Name)
	if !found || sym == nil || sym.Scope == nil {
		return
	}
	r.resolveFunctionBody(sym, fn)
}

func (r *resolver) resolveFunctionBody(sym *symbols.Symbol, fn *ast.FnDecl) {
	if r == nil || r.module == nil || sym == nil || fn == nil || fn.Body == nil || sym.Scope == nil {
		return
	}
	funcScope := sym.Scope.(*table.Scope)
	for _, param := range fn.Params {
		if param.Name == nil || param.Name.Name == "" {
			r.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "parameter name required", ast.LocOf(fn), "")
			return
		}
		paramSym := symbols.New(param.Name.Name, symbols.SymbolParam, param.Name, ast.LocOf(param.Name))
		paramSym.Initialized = true
		if err := funcScope.Declare(paramSym); err != nil {
			semantic_errors.RedeclarationError(r.ctx, funcScope, err.Error(), param.Name.Name, param.Name.Location)
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
		r.resolveFunctionBody(sym, method)
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
		r.resolveLocalBinding(scope, node.Name, symbols.SymbolVar, node.Value, node, node.Location)
	case *ast.ConstDecl:
		r.resolveLocalBinding(scope, node.Name, symbols.SymbolConst, node.Value, node, node.Location)
	case *ast.ReturnStmt:
		if node.Value != nil {
			r.resolveExpr(scope, node.Value)
		}
	case *ast.IfStmt:
		if node.Cond == nil {
			r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement, "if condition required", ast.LocOf(node), "")
			return
		}
		r.resolveExpr(scope, node.Cond)
		// Snapshot definite-assignment state before branching.
		before := snapshotInitialized(scope)
		// Resolve `then` in a child scope so locals do not leak out.
		r.resolveBlock(table.New(scope), node.Then)
		// Record which names `then` definitely initialized.
		thenState := snapshotInitialized(scope)
		// Restore pre-branch state before checking `else`.
		restoreInitialized(before)
		if elseBlock, ok := node.Else.(*ast.BlockStmt); ok {
			// Resolve `else` in a separate child scope.
			r.resolveBlock(table.New(scope), elseBlock)
			// Keep init only when both branches initialize same symbol.
			mergeInitialized(before, thenState, snapshotInitialized(scope))
			return
		}
		if node.Else != nil {
			// Non-block `else` still counts as second branch.
			r.resolveStmt(scope, node.Else)
			// Same merge rule: both branches must initialize it.
			mergeInitialized(before, thenState, snapshotInitialized(scope))
			return
		}
		// No `else`: `then` alone cannot prove definite assignment.
		restoreInitialized(before)
	case *ast.ForStmt:
		if node.Cond != nil {
			r.resolveExpr(scope, node.Cond)
		}
		before := snapshotInitialized(scope)
		r.resolveBlock(table.New(scope), node.Body)
		restoreInitialized(before)
	case *ast.ExprStmt:
		r.resolveExpr(scope, node.Expr)
	case *ast.AssignStmt:
		r.resolveAssignTarget(scope, node.Target)
		r.resolveExpr(scope, node.Value)
		r.markAssigned(scope, node.Target)
	default:
		r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement, "unsupported statement", ast.LocOf(node), "")
	}
}

func (r *resolver) resolveLocalBinding(scope *table.Scope, name *ast.Ident, kind symbols.Kind, value ast.Expr, node ast.Node, loc *source.Location) {
	sym := symbols.New(name.Name, kind, node, ast.LocOf(name))
	sym.Initializing = true
	if err := scope.Declare(sym); err != nil {
		semantic_errors.RedeclarationError(r.ctx, scope, err.Error(), name.Name, loc)
		return
	}
	if value != nil {
		r.resolveExpr(scope, value)
		sym.Initialized = true
	}
	sym.Initializing = false
}

func (r *resolver) resolveExpr(scope *table.Scope, expr ast.Expr) {
	switch node := expr.(type) {
	case *ast.NumberLit:
		return
	case *ast.StringLit:
		return
	case *ast.NoneLit:
		return
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if ok && sym != nil {
			sym.Used = true
			if sym.Kind == symbols.SymbolImport {
				r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression, "import alias must be qualified with `::`", ast.LocOf(node), "")
				return
			}
			if sym.Initializing {
				msg := "symbol `" + node.Name + "` used before it's defined"
				r.ctx.Diagnostics.Add(
					diagnostics.NewError(msg).
						WithCode(diagnostics.ErrUseBeforeDecl).
						WithPrimaryLabel(ast.LocOf(node), msg).
						WithHelp("rename binding or use earlier value"),
				)
				return
			}
			if !sym.Initialized && symbols.RequiresInitialization(sym.Kind) {
				msg := "symbol `" + node.Name + "` used before it's initialized"
				r.ctx.Diagnostics.Add(
					diagnostics.NewError(msg).
						WithCode(diagnostics.ErrUninitializedVariable).
						WithPrimaryLabel(ast.LocOf(node), msg).
						WithHelp("assign a value before reading this symbol"),
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
		if scopedType, ok := node.Type.(*ast.ScopeResolution); ok {
			r.resolveScopeResolution(scopedType)
		}
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
		r.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression, "unsupported expression type", ast.LocOf(node), "")
	}
}

func Resolve(ctx *project.CompilerContext, module *project.Module) {
	if module == nil || ctx == nil {
		return
	}
	r := &resolver{module: module, ctx: ctx}
	r.resolveModule()
}

func snapshotInitialized(scope *table.Scope) map[*symbols.Symbol]bool {
	state := make(map[*symbols.Symbol]bool)
	for curr := scope; curr != nil; curr = curr.Parent() {
		for _, sym := range curr.Symbols() {
			if sym != nil {
				state[sym] = sym.Initialized
			}
		}
	}
	return state
}

func restoreInitialized(state map[*symbols.Symbol]bool) {
	for sym, initialized := range state {
		if sym != nil {
			sym.Initialized = initialized
		}
	}
}

func mergeInitialized(before, thenState, elseState map[*symbols.Symbol]bool) {
	for sym, wasInitialized := range before {
		if sym == nil {
			continue
		}
		sym.Initialized = wasInitialized || (thenState[sym] && elseState[sym])
	}
}

func (r *resolver) resolveAssignTarget(scope *table.Scope, expr ast.Expr) {
	switch node := expr.(type) {
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if ok && sym != nil {
			sym.Used = true
			return
		}
		reportUnresolved(r.module, scope, node, r.ctx.Diagnostics)
	case *ast.SelectorExpr:
		r.resolveExpr(scope, node.Expr)
	default:
		r.resolveExpr(scope, expr)
	}
}

func (r *resolver) markAssigned(scope *table.Scope, expr ast.Expr) {
	ident, ok := expr.(*ast.Ident)
	if !ok || ident == nil {
		return
	}
	sym, ok := scope.Lookup(ident.Name)
	if !ok || sym == nil {
		return
	}
	sym.Initialized = true
}

func (r *resolver) resolveScopeResolution(node *ast.ScopeResolution) bool {
	if r == nil || r.module == nil || node == nil {
		return false
	}
	qualifier := node.Module.Name
	member := node.Name.Name
	resolved, ok := project.LookupImportedSymbol(r.ctx, r.module, qualifier, member)
	if !ok || resolved.Symbol == nil {
		if r.ctx != nil {
			if _, exists := r.module.Imports[qualifier]; !exists {
				r.ctx.Diagnostics.AddError(diagnostics.ErrModuleNotFound, "unknown import alias `"+qualifier+"`", ast.LocOf(node), "")
			} else if resolved.Module == nil || resolved.Module.ModuleScope == nil {
				r.ctx.Diagnostics.AddError(diagnostics.ErrModuleNotFound, "imported module not loaded for `"+qualifier+"`", ast.LocOf(node), "")
			} else {
				r.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol, "unknown identifier `"+member+"` in module `"+qualifier+"`", ast.LocOf(node), "")
			}
		}
		return false
	}
	if !resolved.Symbol.IsPub {
		r.ctx.Diagnostics.AddError(diagnostics.ErrSymbolNotExported, "`"+member+"` is not exported from `"+qualifier+"`", ast.LocOf(node), "use of unexported symbol").
			WithSecondaryLabel(resolved.Symbol.Location, "defined here").
			WithNote("symbols with uppercase are exported otherwise private")
		return false
	}
	return true
}

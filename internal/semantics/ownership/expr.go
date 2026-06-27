package ownership

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

type useKind uint8

const (
	useRead useKind = iota
	useCopy
	useConsume
)

func (a *analyzer) checkExpr(scope *table.Scope, expr ast.Expr, st state, use useKind) {
	if a == nil || expr == nil {
		return
	}
	switch e := expr.(type) {
	case *ast.Ident:
		a.checkIdent(scope, e, st, use)
	case *ast.MoveExpr:
		a.checkMove(scope, e, st, use)
	case *ast.SelectorExpr:
		a.checkSelector(scope, e, st, use)
	case *ast.StructLit:
		for _, field := range e.Fields {
			a.checkExpr(scope, field.Value, st, useCopy)
		}
	case *ast.CallExpr:
		a.checkCall(scope, e, st)
	case *ast.UnaryExpr:
		a.checkExpr(scope, e.Expr, st, useRead)
	case *ast.BinaryExpr:
		a.checkExpr(scope, e.Left, st, useRead)
		a.checkExpr(scope, e.Right, st, useRead)
	case *ast.AsExpr:
		a.checkExpr(scope, e.Expr, st, useCopy)
	case *ast.ScopeResolution, *ast.NumberLit, *ast.StringLit, *ast.BoolLit, *ast.NoneLit, *ast.BadExpr:
		return
	default:
		return
	}
}

func (a *analyzer) checkIdent(scope *table.Scope, ident *ast.Ident, st state, use useKind) {
	if scope == nil || ident == nil {
		return
	}
	sym, ok := scope.Lookup(ident.Name)
	if !ok || sym == nil {
		return
	}
	if site, moved := st[sym]; moved {
		diag := a.ctx.Diagnostics.AddError(diagnostics.ErrUseAfterMove,
			"value used after move", ast.LocOf(ident), "")
		if site != nil {
			diag.WithSecondaryLabel(ast.LocOf(site), "moved here")
		}
		return
	}
	if !ownershipTrackedSymbol(sym) {
		return
	}
	switch use {
	case useCopy:
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"copy of move-only value requires `move` or consuming context", ast.LocOf(ident), "")
	case useConsume:
		st[sym] = ident
	}
}

func (a *analyzer) checkMove(scope *table.Scope, move *ast.MoveExpr, st state, use useKind) {
	if scope == nil || move == nil {
		return
	}
	ident, ok := move.Expr.(*ast.Ident)
	if !ok || ident == nil {
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
			"`move` currently requires an identifier operand", ast.LocOf(move), "")
		return
	}
	if use == useRead {
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"explicit `move` is not allowed in expression", ast.LocOf(move), "")
		return
	}
	sym, found := scope.Lookup(ident.Name)
	if !found || sym == nil {
		return
	}
	if site, moved := st[sym]; moved {
		diag := a.ctx.Diagnostics.AddError(diagnostics.ErrUseAfterMove,
			"value used after move", ast.LocOf(ident), "")
		if site != nil {
			diag.WithSecondaryLabel(ast.LocOf(site), "moved here")
		}
		return
	}
	if ownershipTrackedSymbol(sym) {
		st[sym] = move
	}
}

func (a *analyzer) checkSelector(scope *table.Scope, selector *ast.SelectorExpr, st state, use useKind) {
	if selector == nil {
		return
	}
	a.checkExpr(scope, selector.Expr, st, useRead)
	if use == useRead {
		return
	}
	if ownershipTrackedType(a.exprType(selector)) {
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"move-only subexpression must be bound before copy or move", ast.LocOf(selector), "")
	}
}

func (a *analyzer) checkCall(scope *table.Scope, call *ast.CallExpr, st state) {
	if call == nil {
		return
	}
	if selector, ok := call.Callee.(*ast.SelectorExpr); ok && selector != nil {
		a.checkMethodCall(scope, selector, call, st)
		return
	}
	a.checkExpr(scope, call.Callee, st, useRead)
	fn, ok := a.exprType(call.Callee).(*typeinfo.FuncType)
	if !ok || fn == nil || len(call.Args) != len(fn.Params) {
		for _, arg := range call.Args {
			a.checkExpr(scope, arg, st, useRead)
		}
		return
	}
	for i, arg := range call.Args {
		a.checkExpr(scope, arg, st, paramUse(fn, i))
	}
}

func (a *analyzer) checkMethodCall(scope *table.Scope, selector *ast.SelectorExpr, call *ast.CallExpr, st state) {
	fn, ok := a.exprType(selector).(*typeinfo.FuncType)
	if !ok || fn == nil || selector == nil || call == nil {
		if selector != nil {
			a.checkExpr(scope, selector.Expr, st, useRead)
		}
		for _, arg := range call.Args {
			a.checkExpr(scope, arg, st, useRead)
		}
		return
	}
	a.checkExpr(scope, selector.Expr, st, paramUse(fn, 0))
	if len(call.Args)+1 != len(fn.Params) {
		for _, arg := range call.Args {
			a.checkExpr(scope, arg, st, useRead)
		}
		return
	}
	for i, arg := range call.Args {
		a.checkExpr(scope, arg, st, paramUse(fn, i+1))
	}
}

func paramUse(fn *typeinfo.FuncType, index int) useKind {
	if fn != nil && index >= 0 && index < len(fn.Consumes) && fn.Consumes[index] {
		return useConsume
	}
	return useCopy
}

func (a *analyzer) exprType(expr ast.Expr) typeinfo.Type {
	if a == nil || a.module == nil || a.module.Semantics == nil || expr == nil {
		return nil
	}
	return a.module.Semantics.ExprTypes[expr.ID()]
}

func ownershipTrackedSymbol(sym *symbols.Symbol) bool {
	typ, ok := symbols.GetSymbolType(sym)
	return ok && ownershipTrackedType(typ)
}

func ownershipTrackedType(t typeinfo.Type) bool {
	if t == nil || typeinfo.IsCopyType(t) {
		return false
	}
	ptr, ok := typeinfo.Underlying(t).(*typeinfo.RawPtrType)
	return !ok || ptr == nil
}

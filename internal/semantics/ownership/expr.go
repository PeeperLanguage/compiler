package ownership

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/place"
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
	case *ast.AddressExpr:
		a.checkExpr(scope, e.Expr, st, useRead)
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
	if site, moved := st.moved[sym]; moved {
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
		st.moved[sym] = ident
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
	if use != useConsume {
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"explicit `move` requires a consuming parameter or move binding target", ast.LocOf(move), "")
		return
	}
	sym, found := scope.Lookup(ident.Name)
	if !found || sym == nil {
		return
	}
	if site, moved := st.moved[sym]; moved {
		diag := a.ctx.Diagnostics.AddError(diagnostics.ErrUseAfterMove,
			"value used after move", ast.LocOf(ident), "")
		if site != nil {
			diag.WithSecondaryLabel(ast.LocOf(site), "moved here")
		}
		return
	}
	if ownershipTrackedSymbol(sym) {
		st.moved[sym] = move
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

func (a *analyzer) updatePointerBinding(scope *table.Scope, node ast.Stmt, value ast.Expr, st state) {
	if scope == nil || node == nil {
		return
	}
	sym, found := scope.LookupNode(node)
	if !found {
		return
	}
	a.updatePointerSymbol(sym, scope, value, st)
}

func (a *analyzer) updatePointerSymbol(sym *symbols.Symbol, scope *table.Scope, value ast.Expr, st state) {
	if sym == nil || st.pointers == nil {
		return
	}
	typ, hasType := symbols.GetSymbolType(sym)
	if !hasType {
		delete(st.pointers, sym)
		return
	}
	if _, ok := typeinfo.Underlying(typ).(*typeinfo.RawPtrType); !ok {
		delete(st.pointers, sym)
		return
	}
	if origin, ok := a.pointerOrigin(scope, value, st); ok {
		st.pointers[sym] = origin
		return
	}
	delete(st.pointers, sym)
}

func (a *analyzer) checkPointerEscape(scope *table.Scope, expr ast.Expr, st state) {
	if expr == nil {
		return
	}
	if origin, ok := a.pointerOrigin(scope, expr, st); ok {
		a.reportPointerEscape(expr, origin)
		return
	}
	switch e := expr.(type) {
	case *ast.StructLit:
		for _, field := range e.Fields {
			a.checkPointerEscape(scope, field.Value, st)
		}
	case *ast.MoveExpr:
		a.checkPointerEscape(scope, e.Expr, st)
	}
}

func (a *analyzer) pointerOrigin(scope *table.Scope, expr ast.Expr, st state) (pointerOrigin, bool) {
	switch e := expr.(type) {
	case *ast.AddressExpr:
		root, ok := place.LocalRoot(scope, a.module.ModuleScope, e.Expr, a.exprType)
		if !ok || root == nil {
			return pointerOrigin{}, false
		}
		return pointerOrigin{root: root, site: e}, true
	case *ast.Ident:
		if scope == nil {
			return pointerOrigin{}, false
		}
		sym, found := scope.Lookup(e.Name)
		if !found || sym == nil {
			return pointerOrigin{}, false
		}
		origin, ok := st.pointers[sym]
		return origin, ok
	case *ast.MoveExpr:
		return a.pointerOrigin(scope, e.Expr, st)
	default:
		return pointerOrigin{}, false
	}
}

func (a *analyzer) reportPointerEscape(expr ast.Expr, origin pointerOrigin) {
	if a == nil || a.ctx == nil || a.ctx.Diagnostics == nil || origin.root == nil {
		return
	}
	diag := a.ctx.Diagnostics.AddError(diagnostics.ErrPointerEscape,
		"cannot return pointer to local storage", ast.LocOf(expr), "")
	if origin.root.Location != nil {
		diag.WithSecondaryLabel(origin.root.Location, "local storage declared here")
	}
	diag.WithHelp("allocate the value with an explicit allocator before returning a pointer to it")
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

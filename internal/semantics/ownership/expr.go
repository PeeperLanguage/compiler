package ownership

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
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

func (a *analyzer) checkExpr(
	scope *table.Scope,
	expr ast.Expr,
	st state,
	use useKind,
	loans *loanContext,
	projectionBase bool,
) {
	if a == nil || expr == nil {
		return
	}
	switch e := expr.(type) {
	case *ast.Ident:
		a.checkIdent(scope, e, st, use)
		sym := a.module.Semantics.ResolvedSymbols[e.ID()]
		if _, reference := referenceMutability(sym); reference {
			loans.useReference(sym)
			return
		}
		if !projectionBase {
			a.checkStorageAccess(scope, e, st, loans, storageAccessForUse(a.exprType(e), use))
		}
	case *ast.AddressExpr:
		access := storageSharedBorrow
		if e.Mode == ast.AddressMutable {
			access = storageMutableBorrow
		}
		a.checkAddressExpr(scope, e, st, loans, access)
	case *ast.SelectorExpr:
		a.checkSelector(scope, e, st, use, loans)
		if !projectionBase {
			a.checkStorageAccess(scope, e, st, loans, storageAccessForUse(a.exprType(e), use))
		}
	case *ast.IndexExpr:
		if typeinfo.IsInvalidOrUnknown(a.exprType(e)) {
			return
		}
		_, slicing := e.Index.(*ast.RangeExpr)
		a.checkExpr(scope, e.Expr, st, useRead, loans, true)
		a.checkExpr(scope, e.Index, st, useRead, loans, false)
		if !projectionBase {
			access := storageAccessForUse(a.exprType(e), use)
			if slicing {
				access = storageSharedBorrow
				if _, mutable, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(a.exprType(e))); reference && mutable {
					access = storageMutableBorrow
				}
			}
			a.checkStorageAccess(scope, e, st, loans, access)
		}
		if slicing {
			return
		}
		if a.planProjectionBaseDrop(e, e.Expr) {
			return
		}
		if use != useRead && ownershipTrackedType(a.exprType(e)) {
			a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
				"move-only indexed element cannot be used by value; borrow it with `&` or `&mut`", ast.LocOf(e), "")
		}
	case *ast.RangeExpr:
		a.checkExpr(scope, e.Start, st, useRead, loans, false)
		a.checkExpr(scope, e.End, st, useRead, loans, false)
	case *ast.StructLit:
		for _, field := range e.Fields {
			a.checkExpr(scope, field.Value, st, useConsume, loans, false)
		}
	case *ast.ArrayLit:
		for _, value := range e.Values {
			a.checkExpr(scope, value, st, useConsume, loans, false)
		}
	case *ast.CallExpr:
		a.checkCall(scope, e, st, loans)
	case *ast.FreeExpr:
		a.checkExpr(scope, e.Expr, st, useConsume, loans, false)
	case *ast.PrintExpr:
		a.checkExpr(scope, e.Expr, st, useRead, loans, false)
	case *ast.UnaryExpr:
		a.checkExpr(scope, e.Expr, st, useRead, loans, false)
	case *ast.BinaryExpr:
		a.checkExpr(scope, e.Left, st, useRead, loans, false)
		a.checkExpr(scope, e.Right, st, useRead, loans, false)
	case *ast.AsExpr:
		a.checkExpr(scope, e.Expr, st, useConsume, loans, false)
	case *ast.ScopeResolution, *ast.NumberLit, *ast.StringLit, *ast.ByteLit, *ast.CharLit, *ast.BoolLit, *ast.NoneLit, *ast.BadExpr:
		return
	default:
		return
	}
}

func (a *analyzer) expandedDefaultBinding(ident *ast.Ident) (place.Binding, bool) {
	if a == nil || a.module == nil || a.module.Semantics == nil || ident == nil {
		return place.Binding{}, false
	}
	if _, ok := a.module.Semantics.ExpandedDefaultBindings[ident.ID()]; !ok {
		return place.Binding{}, false
	}
	return place.Binding{Symbol: a.module.Semantics.ResolvedSymbols[ident.ID()]}, true
}

func (a *analyzer) checkAddressExpr(
	scope *table.Scope,
	expr *ast.AddressExpr,
	st state,
	loans *loanContext,
	access storageAccess,
) {
	if expr == nil {
		return
	}
	a.checkExpr(scope, expr.Expr, st, useRead, loans, true)
	if expr.Mode != ast.AddressRaw {
		a.checkStorageAccess(scope, expr.Expr, st, loans, access)
	}
}

func storageAccessForUse(typ typeinfo.Type, use useKind) storageAccess {
	if use == useConsume && ownershipTrackedType(typ) {
		return storageConsume
	}
	return storageRead
}

func (a *analyzer) checkIdent(scope *table.Scope, ident *ast.Ident, st state, use useKind) {
	if scope == nil || ident == nil {
		return
	}
	var sym *symbols.Symbol
	var ok bool
	if a.module != nil && a.module.Semantics != nil {
		sym = a.module.Semantics.ResolvedSymbols[ident.ID()]
		ok = sym != nil
	}
	if !ok {
		sym, ok = scope.Lookup(ident.Name)
	}
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
		if symType, ok := symbols.GetSymbolType(sym); ok {
			if _, mutable, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(symType)); ok && mutable {
				a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
					"mutable reference cannot be copied; pass it directly to transfer or reborrow", ast.LocOf(ident), "")
				return
			}
		}
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"copy of move-only value requires a consuming context", ast.LocOf(ident), "")
	case useConsume:
		st.moved[sym] = ident
		delete(st.live, sym)
	}
}

func (a *analyzer) checkSelector(
	scope *table.Scope,
	selector *ast.SelectorExpr,
	st state,
	use useKind,
	loans *loanContext,
) {
	if selector == nil {
		return
	}
	a.checkExpr(scope, selector.Expr, st, useRead, loans, true)
	if a.planProjectionBaseDrop(selector, selector.Expr) {
		return
	}
	if use == useRead {
		return
	}
	if ownershipTrackedType(a.exprType(selector)) {
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"move-only subexpression must be bound before it can be consumed", ast.LocOf(selector), "")
	}
}

func (a *analyzer) planProjectionBaseDrop(projection, base ast.Expr) bool {
	if a == nil || a.cleanup == nil || projection == nil || base == nil {
		return false
	}
	if place.IsPlaceExpr(base) || !typeinfo.NeedsDrop(a.exprType(base)) {
		return false
	}
	if typeinfo.NeedsDrop(a.exprType(projection)) {
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"ownership-bearing projection from temporary must be bound before use", ast.LocOf(projection), "")
		return true
	}
	a.cleanup.ProjectionBase[ir.NodeID(projection.ID())] = struct{}{}
	return false
}

func (a *analyzer) checkCall(scope *table.Scope, call *ast.CallExpr, st state, loans *loanContext) {
	if call == nil {
		return
	}
	temporaryMark := len(loans.temporary)
	reservationMark := len(loans.reserved)
	defer func() {
		loans.temporary = loans.temporary[:temporaryMark]
		loans.reserved = loans.reserved[:reservationMark]
	}()
	if selector, ok := call.Callee.(*ast.SelectorExpr); ok && selector != nil {
		if a.checkMethodCall(scope, selector, call, st, loans) {
			a.activateCallReservations(call, reservationMark, loans)
		}
		return
	}
	a.checkExpr(scope, call.Callee, st, useRead, loans, false)
	if ident, ok := call.Callee.(*ast.Ident); ok && ident != nil {
		sym := a.module.Semantics.ResolvedSymbols[ident.ID()]
		if sym != nil && sym.CompilerOp == symbols.CompilerOpAlloc {
			if len(call.Args) > 0 {
				a.checkExpr(scope, call.Args[0], st, useConsume, loans, false)
			}
			for _, arg := range call.Args[1:] {
				a.checkExpr(scope, arg, st, useRead, loans, false)
			}
			return
		}
	}
	fn, ok := a.exprType(call.Callee).(*typeinfo.FuncType)
	if !ok || fn == nil || len(call.Args) != len(fn.Params) {
		for _, arg := range call.Args {
			a.checkExpr(scope, arg, st, useRead, loans, false)
		}
		return
	}
	for i, arg := range call.Args {
		a.checkCallArgument(scope, arg, fn.Params[i], call, st, loans)
	}
	a.activateCallReservations(call, reservationMark, loans)
}

func (a *analyzer) checkMethodCall(
	scope *table.Scope,
	selector *ast.SelectorExpr,
	call *ast.CallExpr,
	st state,
	loans *loanContext,
) bool {
	fn, ok := a.exprType(selector).(*typeinfo.FuncType)
	if !ok || fn == nil || selector == nil || call == nil {
		if selector != nil {
			a.checkExpr(scope, selector.Expr, st, useRead, loans, false)
		}
		for _, arg := range call.Args {
			a.checkExpr(scope, arg, st, useRead, loans, false)
		}
		return false
	}
	a.checkCallArgument(scope, selector.Expr, fn.Params[0], call, st, loans)
	if len(call.Args)+1 != len(fn.Params) {
		for _, arg := range call.Args {
			a.checkExpr(scope, arg, st, useRead, loans, false)
		}
		return false
	}
	for i, arg := range call.Args {
		a.checkCallArgument(scope, arg, fn.Params[i+1], call, st, loans)
	}
	return true
}

func (a *analyzer) checkCallArgument(
	scope *table.Scope,
	arg ast.Expr,
	paramType typeinfo.Type,
	call *ast.CallExpr,
	st state,
	loans *loanContext,
) {
	_, mutable, reference := typeinfo.ReferenceValueTarget(paramType)
	if !reference {
		use := useConsume
		if typeinfo.IsImplicitCopyType(paramType) {
			use = useRead
		}
		a.checkExpr(scope, arg, st, use, loans, false)
		return
	}
	access := storageSharedBorrow
	if mutable {
		access = storageMutableReservation
	}
	if explicitBorrow, explicit := arg.(*ast.AddressExpr); explicit {
		a.checkAddressExpr(scope, explicitBorrow, st, loans, access)
	} else {
		a.checkExpr(scope, arg, st, useRead, loans, true)
		a.checkStorageAccess(scope, arg, st, loans, access)
	}
	origins := a.originsForExpr(scope, arg, st)
	if len(origins) == 0 {
		return
	}
	loan := referenceLoan{
		id:      loanID{node: arg},
		origins: origins,
		mutable: mutable,
		site:    arg,
	}
	if mutable {
		loans.reserved = append(loans.reserved, loanFact{
			loan:         loan,
			holder:       a.referenceHolder(arg),
			keepingAlive: call,
		})
		return
	}
	loans.addTemporary([]referenceLoan{loan}, call)
}

func (a *analyzer) exprType(expr ast.Expr) typeinfo.Type {
	if a == nil || a.module == nil || a.module.Semantics == nil || expr == nil {
		return nil
	}
	return a.module.Semantics.ExprTypes[expr.ID()]
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
	}
}

func (a *analyzer) pointerOrigin(scope *table.Scope, expr ast.Expr, st state) (pointerOrigin, bool) {
	switch e := expr.(type) {
	case *ast.AddressExpr:
		if e.Mode != ast.AddressRaw {
			return pointerOrigin{}, false
		}
		root, ok := place.LocalRoot(scope, a.module.ModuleScope, e.Expr, a.exprType, a.expandedDefaultBinding)
		if !ok || root == nil {
			return pointerOrigin{}, false
		}
		return pointerOrigin{root: root, site: e}, true
	case *ast.Ident:
		if scope == nil {
			return pointerOrigin{}, false
		}
		var sym *symbols.Symbol
		var found bool
		if a.module != nil && a.module.Semantics != nil {
			sym = a.module.Semantics.ResolvedSymbols[e.ID()]
			found = sym != nil
		}
		if !found {
			sym, found = scope.Lookup(e.Name)
		}
		if !found || sym == nil {
			return pointerOrigin{}, false
		}
		origin, ok := st.pointers[sym]
		return origin, ok
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
	if t == nil || typeinfo.IsImplicitCopyType(t) {
		return false
	}
	return true
}

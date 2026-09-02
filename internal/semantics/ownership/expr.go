package ownership

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func (a *analyzer) checkExpr(
	scope *symbols.Scope,
	expr ast.Expr,
	st state,
	use typeinfo.UseKind,
	loans *loanContext,
	projectionBase bool,
) {
	if a == nil || expr == nil {
		return
	}
	switch e := expr.(type) {
	case *ast.Ident:
		a.checkIdent(scope, e, st, use)
		sym := a.module.Bindings.NodeSymbols[e.ID()]
		if _, reference := referenceMutability(sym); reference {
			loans.useReference(sym)
			return
		}
		if !projectionBase {
			a.checkStorageAccess(e, loans, storageAccessForUse(a.exprType(e), use))
		}
		if referenceHoldingSymbol(sym) {
			loans.useReference(sym)
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
			a.checkStorageAccess(e, loans, storageAccessForUse(a.exprType(e), use))
		}
	case *ast.IndexExpr:
		if typeinfo.IsInvalidOrUnknown(a.exprType(e)) {
			return
		}
		_, slicing := e.Index.(*ast.RangeExpr)
		a.checkExpr(scope, e.Expr, st, typeinfo.UseRead, loans, true)
		a.checkExpr(scope, e.Index, st, typeinfo.UseRead, loans, false)
		if !projectionBase {
			access := storageAccessForUse(a.exprType(e), use)
			if slicing {
				access = storageSharedBorrow
				if _, mutable, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(a.exprType(e))); reference && mutable {
					access = storageMutableBorrow
				}
			}
			a.checkStorageAccess(e, loans, access)
		}
		if slicing {
			return
		}
		if a.planProjectionBaseDrop(e, e.Expr) {
			return
		}
		if use != typeinfo.UseRead && ownershipTrackedType(a.exprType(e)) {
			if a.partialVariantPayloadMove(e) {
				a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
					"move-only variant payload cannot be moved from partial place; borrow it instead", ast.LocOf(e), "")
				return
			}
			a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
				"move-only indexed element cannot be used by value; borrow it with `&` or `&mut`", ast.LocOf(e), "")
		}
	case *ast.RangeExpr:
		a.checkExpr(scope, e.Start, st, typeinfo.UseRead, loans, false)
		a.checkExpr(scope, e.End, st, typeinfo.UseRead, loans, false)
	case *ast.StructLit:
		a.checkLiteralFields(scope, e.Fields, st, loans)
	case *ast.VariantLit:
		a.checkExpr(scope, e.Payload, st, typeinfo.UseMove, loans, false)
	case *ast.ArrayLit:
		for _, value := range e.Values {
			a.checkExpr(scope, value, st, typeinfo.UseMove, loans, false)
		}
	case *ast.CallExpr:
		a.checkCall(scope, e, st, loans)
	case *ast.FreeExpr:
		a.checkExpr(scope, e.Expr, st, typeinfo.UseMove, loans, false)
	case *ast.PrintExpr:
		a.checkExpr(scope, e.Expr, st, typeinfo.UseRead, loans, false)
	case *ast.UnaryExpr:
		a.checkExpr(scope, e.Expr, st, typeinfo.UseRead, loans, false)
	case *ast.BinaryExpr:
		if _, concat := a.module.Typechecking.StringConcatenations[e.ID()]; concat {
			a.checkExpr(scope, e.Left, st, typeinfo.UseMove, loans, false)
			a.checkExpr(scope, e.Right, st, typeinfo.UseRead, loans, false)
			return
		}
		a.checkExpr(scope, e.Left, st, typeinfo.UseRead, loans, false)
		a.checkExpr(scope, e.Right, st, typeinfo.UseRead, loans, false)
	case *ast.IsExpr:
		a.checkExpr(scope, e.Value, st, typeinfo.UseRead, loans, false)
	case *ast.AsExpr:
		a.checkExpr(scope, e.Expr, st, typeinfo.UseMove, loans, false)
	case *ast.ScopeResolution, *ast.NumberLit, *ast.StringLit, *ast.ByteLit, *ast.CharLit, *ast.BoolLit, *ast.NoneLit, *ast.BadExpr:
		return
	default:
		panic(fmt.Sprintf("ownership: unhandled expression %T", expr))
	}
}

func (a *analyzer) checkLiteralFields(scope *symbols.Scope, fields []ast.StructLitField, st state, loans *loanContext) {
	for _, field := range fields {
		a.checkExpr(scope, field.Value, st, typeinfo.UseMove, loans, false)
	}
}

func (a *analyzer) checkAddressExpr(
	scope *symbols.Scope,
	expr *ast.AddressExpr,
	st state,
	loans *loanContext,
	access storageAccess,
) {
	if expr == nil {
		return
	}
	a.checkExpr(scope, expr.Expr, st, typeinfo.UseRead, loans, true)
	if expr.Mode != ast.AddressRaw {
		a.checkStorageAccess(expr.Expr, loans, access)
	}
}

func storageAccessForUse(typ typeinfo.Type, use typeinfo.UseKind) storageAccess {
	if use == typeinfo.UseMove && ownershipTrackedType(typ) {
		return storageConsume
	}
	return storageRead
}

func (a *analyzer) checkIdent(scope *symbols.Scope, ident *ast.Ident, st state, use typeinfo.UseKind) {
	if scope == nil || ident == nil {
		return
	}
	var sym *symbols.Symbol
	var ok bool
	if a.module != nil && a.module.Bindings != nil {
		sym = a.module.Bindings.NodeSymbols[ident.ID()]
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
	case typeinfo.UseCopy:
		if symType, ok := symbols.GetSymbolType(sym); ok {
			if _, mutable, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(symType)); ok && mutable {
				a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
					"mutable reference cannot be copied; pass it directly to transfer or reborrow", ast.LocOf(ident), "")
				return
			}
		}
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"copy of move-only value requires a consuming context", ast.LocOf(ident), "")
	case typeinfo.UseMove:
		st.moved[sym] = ident
		delete(st.live, sym)
	}
}

func (a *analyzer) checkSelector(
	scope *symbols.Scope,
	selector *ast.SelectorExpr,
	st state,
	use typeinfo.UseKind,
	loans *loanContext,
) {
	if selector == nil {
		return
	}
	a.checkExpr(scope, selector.Expr, st, typeinfo.UseRead, loans, true)
	if a.planProjectionBaseDrop(selector, selector.Expr) {
		return
	}
	if use == typeinfo.UseRead {
		return
	}
	if ownershipTrackedType(a.exprType(selector)) {
		if a.partialVariantPayloadMove(selector) {
			a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
				"move-only variant payload cannot be moved from partial place; borrow it instead", ast.LocOf(selector), "")
			return
		}
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

func (a *analyzer) checkCall(scope *symbols.Scope, call *ast.CallExpr, st state, loans *loanContext) {
	if call == nil {
		return
	}
	args := a.module.Typechecking.CallArgumentsOrSource(call)
	temporaryMark := len(loans.temporary)
	reservationMark := len(loans.reserved)
	defer func() {
		loans.temporary = loans.temporary[:temporaryMark]
		loans.reserved = loans.reserved[:reservationMark]
	}()
	if selector, ok := call.Callee.(*ast.SelectorExpr); ok && selector != nil {
		if a.checkMethodCall(scope, selector, call, args, st, loans) {
			a.activateCallReservations(call, reservationMark, loans)
		}
		return
	}
	a.checkExpr(scope, call.Callee, st, typeinfo.UseRead, loans, false)
	if ident, ok := call.Callee.(*ast.Ident); ok && ident != nil {
		sym := a.module.Bindings.NodeSymbols[ident.ID()]
		if sym != nil && sym.CompilerOp == symbols.CompilerOpAlloc {
			for _, arg := range args {
				a.checkExpr(scope, arg, st, a.publishedUse(arg, nil), loans, false)
			}
			return
		}
		if sym != nil && sym.CompilerOp == symbols.CompilerOpFromBytes {
			definition, found := intrinsics.LookupFunction(sym.CompilerOp)
			if !found {
				panic("missing from_bytes intrinsic definition")
			}
			fn := definition.Signature(nil, a.ctx.Target)
			for i, arg := range args {
				if i >= len(fn.Params) {
					a.checkExpr(scope, arg, st, typeinfo.UseRead, loans, false)
					continue
				}
				a.checkCallArgument(scope, arg, fn.Params[i], call, st, loans)
			}
			return
		}
	}
	fn, ok := a.exprType(call.Callee).(*typeinfo.FuncType)
	if !ok || fn == nil || len(args) != len(fn.Params) {
		for _, arg := range args {
			a.checkExpr(scope, arg, st, typeinfo.UseRead, loans, false)
		}
		return
	}
	for i, arg := range args {
		a.checkCallArgument(scope, arg, fn.Params[i], call, st, loans)
	}
	a.activateCallReservations(call, reservationMark, loans)
}

func (a *analyzer) checkMethodCall(
	scope *symbols.Scope,
	selector *ast.SelectorExpr,
	call *ast.CallExpr,
	args []ast.Expr,
	st state,
	loans *loanContext,
) bool {
	fn, ok := a.exprType(selector).(*typeinfo.FuncType)
	if !ok || fn == nil || selector == nil || call == nil {
		if selector != nil {
			a.checkExpr(scope, selector.Expr, st, typeinfo.UseRead, loans, false)
		}
		for _, arg := range args {
			a.checkExpr(scope, arg, st, typeinfo.UseRead, loans, false)
		}
		return false
	}
	a.checkCallArgument(scope, selector.Expr, fn.Params[0], call, st, loans)
	if len(args)+1 != len(fn.Params) {
		for _, arg := range args {
			a.checkExpr(scope, arg, st, typeinfo.UseRead, loans, false)
		}
		return false
	}
	for i, arg := range args {
		a.checkCallArgument(scope, arg, fn.Params[i+1], call, st, loans)
	}
	return true
}

func (a *analyzer) checkCallArgument(
	scope *symbols.Scope,
	arg ast.Expr,
	paramType typeinfo.Type,
	call *ast.CallExpr,
	st state,
	loans *loanContext,
) {
	_, mutable, reference := typeinfo.ReferenceValueTarget(paramType)
	if !reference {
		a.checkExpr(scope, arg, st, a.publishedUse(arg, paramType), loans, false)
		return
	}
	access := storageSharedBorrow
	if mutable {
		access = storageMutableReservation
	}
	if explicitBorrow, explicit := arg.(*ast.AddressExpr); explicit {
		a.checkAddressExpr(scope, explicitBorrow, st, loans, access)
	} else {
		a.checkExpr(scope, arg, st, typeinfo.UseRead, loans, true)
		a.checkStorageAccess(arg, loans, access)
	}
	origins := a.originsForExpr(arg)
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

// publishedUse maps the typechecker's published use kind for this argument
// publishedUse resolves the ownership use kind for one value use: the
// typechecker's published classification when present, otherwise the
// capability fallback for diagnostics-continued paths. The ownership
// validator will enforce presence for error-free programs.
func (a *analyzer) publishedUse(arg ast.Expr, paramType typeinfo.Type) typeinfo.UseKind {
	if a.module != nil && a.module.Typechecking != nil {
		if kind, ok := a.module.Typechecking.ValueUses[arg.ID()]; ok {
			return kind
		}
	}
	if paramType == nil || typeinfo.IsImplicitCopyType(paramType) {
		return typeinfo.UseRead
	}
	return typeinfo.UseMove
}

func (a *analyzer) exprType(expr ast.Expr) typeinfo.Type {
	if a == nil || a.module == nil || expr == nil {
		return nil
	}
	return a.module.EffectiveExprType(expr.ID())
}

func (a *analyzer) partialVariantPayloadMove(expr ast.Expr) bool {
	if a == nil || a.module == nil || a.module.Flow == nil || expr == nil {
		return false
	}
	payload, ok := a.module.Flow.Payloads[expr.ID()]
	return ok && len(payload.Cases) > 0 && !payload.Direct
}

func (a *analyzer) updatePointerSymbol(sym *symbols.Symbol, scope *symbols.Scope, value ast.Expr, st state) {
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

func (a *analyzer) checkPointerEscape(scope *symbols.Scope, expr ast.Expr, st state) {
	if expr == nil {
		return
	}
	if origin, ok := a.pointerOrigin(scope, expr, st); ok {
		a.reportPointerEscape(expr, origin)
		return
	}
	switch e := expr.(type) {
	case *ast.StructLit:
		a.checkLiteralPointerEscapes(scope, e.Fields, st)
	case *ast.VariantLit:
		a.checkPointerEscape(scope, e.Payload, st)
	}
}

func (a *analyzer) checkLiteralPointerEscapes(scope *symbols.Scope, fields []ast.StructLitField, st state) {
	for _, field := range fields {
		a.checkPointerEscape(scope, field.Value, st)
	}
}

func (a *analyzer) pointerOrigin(scope *symbols.Scope, expr ast.Expr, st state) (pointerOrigin, bool) {
	switch e := expr.(type) {
	case *ast.AddressExpr:
		if e.Mode != ast.AddressRaw {
			return pointerOrigin{}, false
		}
		root, ok := place.LocalRoot(scope, a.module.ModuleScope, e.Expr, a.exprType, a.module.ExpandedDefaultBinding)
		if !ok || root == nil {
			return pointerOrigin{}, false
		}
		return pointerOrigin{root: root, site: e}, true
	case *ast.Ident:
		if scope == nil {
			return pointerOrigin{}, false
		}
		if _, raw := typeinfo.Underlying(a.exprType(e)).(*typeinfo.RawPtrType); !raw {
			return pointerOrigin{}, false
		}
		if a.module != nil && a.module.Flow != nil {
			if origins, resolved := a.module.Flow.ResolvedValueOrigins[e.ID()]; resolved {
				for _, origin := range origins {
					if origin.Root == nil {
						continue
					}
					for current := scope; current != nil && current != a.module.ModuleScope; current = current.Parent() {
						local, found := current.LookupLocal(origin.Root.Name)
						if found && local == origin.Root {
							return pointerOrigin{root: origin.Root, site: e}, true
						}
					}
				}
				return pointerOrigin{}, false
			}
		}
		var sym *symbols.Symbol
		var found bool
		if a.module != nil && a.module.Bindings != nil {
			sym = a.module.Bindings.NodeSymbols[e.ID()]
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

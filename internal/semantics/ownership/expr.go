package ownership

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func storageAccessForUse(typ typeinfo.Type, use typeinfo.UseKind) storageAccess {
	if use == typeinfo.UseMove && ownershipTrackedType(typ) {
		return storageConsume
	}
	return storageRead
}

func (a *analyzer) planProjectionBaseDrop(projection, base ast.Expr) bool {
	if a == nil || a.cleanup == nil || projection == nil || base == nil {
		return false
	}
	if place.IsPlaceExpr(base) || !typeinfo.OwnershipCapabilityOf(a.exprType(base)).Drop {
		return false
	}
	if typeinfo.OwnershipCapabilityOf(a.exprType(projection)).Drop {
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"ownership-bearing projection from temporary must be bound before use", ast.LocOf(projection), "")
		return true
	}
	a.cleanup.ProjectionBase[ir.NodeID(projection.ID())] = struct{}{}
	return false
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
	if t == nil || typeinfo.OwnershipCapabilityOf(t).Copy == typeinfo.CopyImplicit {
		return false
	}
	return true
}

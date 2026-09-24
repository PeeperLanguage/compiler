package ownership

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func storageAccessForUse(typ typeinfo.Type, use typeinfo.UseKind) storageAccess {
	if use == typeinfo.UseMove && ownershipTrackedType(typ) {
		return storageConsume
	}
	return storageRead
}

func (a *analyzer) planProjectionBaseDrop(projection, base thir.Expr) bool {
	if a == nil || a.cleanup == nil || a.module == nil || projection == nil || base == nil {
		return false
	}
	if storage := base.ExprPlace(); storage != nil && storage.Root != nil {
		return false
	}
	if !typeinfo.OwnershipCapabilityOf(a.exprType(base)).Drop {
		return false
	}
	if typeinfo.OwnershipCapabilityOf(a.exprType(projection)).Drop {
		a.diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"ownership-bearing projection from temporary must be bound before use", projection.SourceInfo().Location, "")
		return true
	}
	a.cleanup.ProjectionBase[projection.SourceInfo().NodeID] = struct{}{}
	return false
}

func (a *analyzer) exprType(expr thir.Expr) typeinfo.Type {
	if a == nil || a.module == nil || expr == nil {
		return nil
	}
	return a.module.EffectiveExprType(ast.NodeID(expr.SourceInfo().NodeID))
}

func (a *analyzer) partialVariantPayloadMove(id ast.NodeID) bool {
	if a == nil || a.module == nil || a.module.Flow == nil || id == 0 {
		return false
	}
	payload, ok := a.module.Flow.Payload(id)
	return ok && len(payload.Cases) > 0 && !payload.Direct
}

func (a *analyzer) updatePointerSymbol(sym *symbols.Symbol, scope *symbols.Scope, value thir.Expr, st state) {
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
	if origin := a.pointerOrigin(scope, value, st); origin != nil {
		st.pointers[sym] = origin
		return
	}
	delete(st.pointers, sym)
}

func (a *analyzer) checkPointerEscape(scope *symbols.Scope, expr thir.Expr, st state) {
	if expr == nil {
		return
	}
	if origin := a.pointerOrigin(scope, expr, st); origin != nil {
		a.reportPointerEscape(expr, origin)
		return
	}
	switch e := expr.(type) {
	case *thir.StructLiteral:
		for _, field := range e.Fields {
			a.checkPointerEscape(scope, field.Value, st)
		}
	case *thir.Variant:
		a.checkPointerEscape(scope, e.Payload, st)
	}
}

func (a *analyzer) pointerOrigin(scope *symbols.Scope, expr thir.Expr, st state) *symbols.Symbol {
	switch e := expr.(type) {
	case *thir.Address:
		if e.Mode != thir.AddressRaw {
			return nil
		}
		return a.localPointerRoot(scope, e.Value)
	case *thir.Ident:
		if scope == nil {
			return nil
		}
		if _, raw := typeinfo.Underlying(a.exprType(e)).(*typeinfo.RawPtrType); !raw {
			return nil
		}
		if a.module != nil && a.module.Flow != nil {
			if resolution, resolved := a.module.Flow.Origins(ast.NodeID(e.SourceInfo().NodeID)); resolved {
				for _, origin := range resolution.Value {
					if origin.Root == nil {
						continue
					}
					for current := scope; current != nil && current != a.module.ModuleScope; current = current.Parent() {
						local, found := current.LookupLocal(origin.Root.Name)
						if found && local == origin.Root {
							return origin.Root
						}
					}
				}
				return nil
			}
		}
		sym := e.Symbol
		if sym == nil {
			sym, _ = scope.Lookup(e.Name)
		}
		return st.pointers[sym]
	default:
		return nil
	}
}

// localPointerRoot retains declaration-module locality for expanded defaults
// and stops at pointer projections, as place.LocalRoot does for source syntax.
func (a *analyzer) localPointerRoot(scope *symbols.Scope, expr thir.Expr) *symbols.Symbol {
	if a == nil || a.module == nil || scope == nil || expr == nil {
		return nil
	}
	switch e := expr.(type) {
	case *thir.Ident:
		if e.IsExpandedDefaultBinding {
			return nil
		}
		if e.Symbol != nil {
			for current := scope; current != nil && current != a.module.ModuleScope; current = current.Parent() {
				if local, found := current.LookupLocal(e.Name); found && local == e.Symbol {
					return local
				}
			}
			return nil
		}
		for current := scope; current != nil && current != a.module.ModuleScope; current = current.Parent() {
			if local, found := current.LookupLocal(e.Name); found {
				return local
			}
		}
	case *thir.Field:
		if _, pointer := typeinfo.PointerTarget(typeinfo.Underlying(a.exprType(e.Base))); !pointer {
			return a.localPointerRoot(scope, e.Base)
		}
	case *thir.Index:
		if _, pointer := typeinfo.PointerTarget(typeinfo.Underlying(a.exprType(e.Base))); !pointer {
			return a.localPointerRoot(scope, e.Base)
		}
	}
	return nil
}

func (a *analyzer) reportPointerEscape(expr thir.Expr, origin *symbols.Symbol) {
	if a == nil || a.diagnostics == nil || origin == nil {
		return
	}
	diag := a.diagnostics.AddError(diagnostics.ErrPointerEscape,
		"cannot return pointer to local storage", expr.SourceInfo().Location, "")
	if origin.Location != nil {
		diag.WithSecondaryLabel(origin.Location, "local storage declared here")
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

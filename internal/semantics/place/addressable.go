package place

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type ExprTypeFunc func(ast.Expr) typeinfo.Type

// Binding carries a symbol lookup result with transient context.
// Symbol is the underlying cross-module symbol pointer; Local is
// true only when the symbol was resolved in the current module's
// scope tree. A shared *symbols.Symbol cannot carry "local-to-this-
// module" because the same pointer appears in both the declaration
// and caller module's ExpandedDefaultBindings.
type Binding struct {
	Symbol *symbols.Symbol
	Local  bool
}

// BindingResolver supplies symbols for idents that were injected
// into the caller AST (e.g. cloned default expressions). When the
// resolver reports a match, scope lookup must be skipped entirely.
// Expanded defaults use Local=false to prevent LocalRoot from
// misclassifying declaration-module storage as a caller pointer-
// escape source.
type BindingResolver func(*ast.Ident) (Binding, bool)

func IsPlaceExpr(expr ast.Expr) bool {
	if node, ok := expr.(*ast.Ident); ok {
		return node != nil
	}
	base, ok := placeProjectionBase(expr)
	return ok && IsPlaceExpr(base)
}

func Addressable(scope *symbols.Scope, expr ast.Expr, exprType ExprTypeFunc, resolve BindingResolver) bool {
	if scope == nil || expr == nil {
		return false
	}
	if e, ok := expr.(*ast.Ident); ok {
		if e == nil {
			return false
		}
		if resolve != nil {
			if binding, found := resolve(e); found {
				return addressableSymbol(binding.Symbol)
			}
		}
		sym, found := scope.Lookup(e.Name)
		return found && addressableSymbol(sym)
	}
	base, ok := placeProjectionBase(expr)
	if !ok {
		return false
	}
	if exprType != nil {
		if _, ok := typeinfo.PointerTarget(typeinfo.Underlying(exprType(base))); ok {
			return true
		}
		if _, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(exprType(base))); ok {
			return true
		}
	}
	return Addressable(scope, base, exprType, resolve)
}

func MutableAddressable(scope *symbols.Scope, expr ast.Expr, exprType ExprTypeFunc, resolve BindingResolver) (mutable bool, sharedReference typeinfo.Type) {
	if scope == nil || expr == nil {
		return false, nil
	}
	if e, ok := expr.(*ast.Ident); ok {
		if e == nil {
			return false, nil
		}
		if resolve != nil {
			if binding, found := resolve(e); found {
				sym := binding.Symbol
				return sym != nil && (sym.Kind == symbols.SymbolVar || sym.Kind == symbols.SymbolParam) && sym.IsMutable(), nil
			}
		}
		return scope.IsMutableBinding(e.Name), nil
	}
	base, ok := placeProjectionBase(expr)
	if !ok {
		return false, nil
	}
	if exprType != nil {
		if _, ok := typeinfo.Underlying(exprType(base)).(*typeinfo.RawPtrType); ok {
			return true, nil
		}
		if target, mutable, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(exprType(base))); ok {
			if mutable {
				return true, nil
			}
			return false, target
		}
	}
	return MutableAddressable(scope, base, exprType, resolve)
}

func LocalRoot(scope, moduleScope *symbols.Scope, expr ast.Expr, exprType ExprTypeFunc, resolve BindingResolver) (*symbols.Symbol, bool) {
	if scope == nil || moduleScope == nil || expr == nil {
		return nil, false
	}
	if e, ok := expr.(*ast.Ident); ok {
		if e == nil {
			return nil, false
		}
		if resolve != nil {
			if binding, found := resolve(e); found {
				// Expanded defaults have Local=false: the symbol
				// lives in the declaration module, not the caller,
				// so it is not a pointer-escape source.
				if binding.Local && addressableSymbol(binding.Symbol) {
					return binding.Symbol, true
				}
				return nil, false
			}
		}
		for current := scope; current != nil && current != moduleScope; current = current.Parent() {
			sym, found := current.LookupLocal(e.Name)
			if found {
				return sym, addressableSymbol(sym)
			}
		}
		return nil, false
	}
	base, ok := placeProjectionBase(expr)
	if !ok {
		return nil, false
	}
	if exprType != nil {
		if _, ok := typeinfo.PointerTarget(typeinfo.Underlying(exprType(base))); ok {
			return nil, false
		}
	}
	return LocalRoot(scope, moduleScope, base, exprType, resolve)
}

func placeProjectionBase(expr ast.Expr) (ast.Expr, bool) {
	switch node := expr.(type) {
	case *ast.SelectorExpr:
		if node == nil || node.Expr == nil {
			return nil, false
		}
		return node.Expr, true
	case *ast.IndexExpr:
		if node == nil || node.Expr == nil || node.Index == nil {
			return nil, false
		}
		if _, slicing := node.Index.(*ast.RangeExpr); slicing {
			return nil, false
		}
		return node.Expr, true
	default:
		return nil, false
	}
}

func addressableSymbol(sym *symbols.Symbol) bool {
	if sym == nil {
		return false
	}
	switch sym.Kind {
	case symbols.SymbolVar, symbols.SymbolConst, symbols.SymbolParam:
		return true
	default:
		return false
	}
}

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

// Projection describes one syntactic projection from a base expression.
//
// This is the canonical structural definition of Peeper place projections.
// Consumers that only need to know how selector/index syntax is nested must use
// this API rather than maintaining their own AST switches. Semantic place
// resolution remains in Resolve, which enriches these structural projections
// with pointer, reference, optional-payload, and stable-index information.
type Projection struct {
	Base  ast.Expr
	Step  OriginProjection
	Index ast.Expr
}

// Project reports the direct projection represented by expr. Slices are not
// places: they borrow a range rather than select one independently addressable
// element, so an IndexExpr containing a RangeExpr is deliberately rejected.
func Project(expr ast.Expr) (Projection, bool) {
	switch node := expr.(type) {
	case *ast.SelectorExpr:
		if node == nil || node.Expr == nil || node.Name == nil {
			return Projection{}, false
		}
		return Projection{
			Base: node.Expr,
			Step: OriginProjection{Kind: OriginField, Field: node.Name.Name},
		}, true
	case *ast.IndexExpr:
		if node == nil || node.Expr == nil || node.Index == nil {
			return Projection{}, false
		}
		if _, slicing := node.Index.(*ast.RangeExpr); slicing {
			return Projection{}, false
		}
		return Projection{
			Base:  node.Expr,
			Step:  OriginProjection{Kind: OriginIndex},
			Index: node.Index,
		}, true
	default:
		return Projection{}, false
	}
}

// Decompose peels every selector/index projection and returns the expression at
// the root plus the projection path in source order. It does not require that
// the root be an identifier: `make().field` therefore decomposes successfully
// and lets callers distinguish a temporary root from named storage themselves.
func Decompose(expr ast.Expr) (ast.Expr, []OriginProjection, bool) {
	if expr == nil {
		return nil, nil, false
	}
	projection, projected := Project(expr)
	if !projected {
		return expr, nil, true
	}
	root, path, ok := Decompose(projection.Base)
	if !ok {
		return nil, nil, false
	}
	path = append(path, projection.Step)
	return root, path, true
}

func IsPlaceExpr(expr ast.Expr) bool {
	root, _, ok := Decompose(expr)
	if !ok {
		return false
	}
	ident, identified := root.(*ast.Ident)
	return identified && ident != nil
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
	projection, ok := Project(expr)
	if !ok {
		return false
	}
	base := projection.Base
	if exprType != nil {
		if _, ok := typeinfo.PointerTarget(typeinfo.Underlying(exprType(base))); ok {
			return true
		}
		if _, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(exprType(base))); ok {
			return true
		}
	}
	return Addressable(scope, projection.Base, exprType, resolve)
}

func MutableAddressable(scope *symbols.Scope, expr ast.Expr, exprType ExprTypeFunc, resolve BindingResolver) (mutable bool, sharedReference typeinfo.Type, mutableBinding *symbols.Symbol) {
	if scope == nil || expr == nil {
		return false, nil, nil
	}
	if e, ok := expr.(*ast.Ident); ok {
		if e == nil {
			return false, nil, nil
		}
		if resolve != nil {
			if binding, found := resolve(e); found {
				sym := binding.Symbol
				if sym != nil && (sym.Kind == symbols.SymbolVar || sym.Kind == symbols.SymbolParam) && sym.IsMutable() {
					return true, nil, sym
				}
				return false, nil, nil
			}
		}
		sym, found := scope.Lookup(e.Name)
		if found && sym != nil && (sym.Kind == symbols.SymbolVar || sym.Kind == symbols.SymbolParam) && sym.IsMutable() {
			return true, nil, sym
		}
		return false, nil, nil
	}
	if index, ok := expr.(*ast.IndexExpr); ok {
		if _, slicing := index.Index.(*ast.RangeExpr); slicing {
			return MutableAddressable(scope, index.Expr, exprType, resolve)
		}
	}
	projection, ok := Project(expr)
	if !ok {
		return false, nil, nil
	}
	base := projection.Base
	if exprType != nil {
		baseType := typeinfo.Underlying(exprType(base))
		if _, ok := baseType.(*typeinfo.RawPtrType); ok {
			return true, nil, nil
		}
		if _, ok := typeinfo.PointerTarget(baseType); ok {
			return true, nil, nil
		}
		if target, mutable, ok := typeinfo.ReferenceTarget(baseType); ok {
			if mutable {
				return true, nil, nil
			}
			return false, target, nil
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
	projection, ok := Project(expr)
	if !ok {
		return nil, false
	}
	base := projection.Base
	if exprType != nil {
		if _, ok := typeinfo.PointerTarget(typeinfo.Underlying(exprType(base))); ok {
			return nil, false
		}
	}
	return LocalRoot(scope, moduleScope, base, exprType, resolve)
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

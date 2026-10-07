package place

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type ExprTypeFunc func(ast.Expr) typeinfo.Type

// BindingResolver supplies symbols for expressions that were resolved outside
// the current scope tree (e.g. cloned defaults or qualified imports). When the
// resolver reports a match, scope lookup must be skipped entirely.
type BindingResolver func(ast.Expr) (*symbols.Symbol, bool)

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

func IsAddressable(scope *symbols.Scope, expr ast.Expr, exprType ExprTypeFunc, resolve BindingResolver) bool {
	if scope == nil || expr == nil {
		return false
	}
	switch expr.(type) {
	case *ast.Ident, *ast.ScopeResolution:
		sym, found := resolveSymbol(scope, expr, resolve)
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
		if _, _, ok := typeinfo.ReferenceValueTarget(exprType(base)); ok {
			return true
		}
	}
	return IsAddressable(scope, projection.Base, exprType, resolve)
}

func MutableAddressable(scope *symbols.Scope, expr ast.Expr, exprType ExprTypeFunc, resolve BindingResolver) (isMutable bool, sharedReference typeinfo.Type, mutableBinding *symbols.Symbol) {
	if scope == nil || expr == nil {
		return false, nil, nil
	}
	switch expr.(type) {
	case *ast.Ident, *ast.ScopeResolution:
		sym, found := resolveSymbol(scope, expr, resolve)
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
	isMutable, sharedReference, mutableBinding = MutableAddressable(scope, base, exprType, resolve)
	if exprType != nil {
		baseType := typeinfo.Underlying(exprType(base))
		if _, ok := baseType.(*typeinfo.RawPtrType); ok {
			return true, nil, nil
		}
		// Ordinary projections, including owning-pointer dereferences, preserve
		// access-path mutability. Only references supply a new capability, and
		// cannot restore write access removed by an enclosing shared reference.
		if sharedReference != nil {
			return false, sharedReference, nil
		}
		if target, isMutable, ok := typeinfo.ReferenceValueTarget(baseType); ok {
			if isMutable {
				return true, nil, nil
			}
			return false, target, nil
		}
	}
	return isMutable, sharedReference, mutableBinding
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

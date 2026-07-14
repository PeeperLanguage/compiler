package place

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

type ExprTypeFunc func(ast.Expr) typeinfo.Type

func IsPlaceExpr(expr ast.Expr) bool {
	switch node := expr.(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		return node != nil && IsPlaceExpr(node.Expr)
	case *ast.IndexExpr:
		return isElementIndexExpr(node) && IsPlaceExpr(node.Expr)
	default:
		return false
	}
}

func Addressable(scope *table.Scope, expr ast.Expr, exprType ExprTypeFunc) bool {
	if scope == nil || expr == nil {
		return false
	}
	var base ast.Expr
	switch e := expr.(type) {
	case *ast.Ident:
		sym, found := scope.Lookup(e.Name)
		return found && addressableSymbol(sym)
	case *ast.SelectorExpr:
		base = e.Expr
	case *ast.IndexExpr:
		if !isElementIndexExpr(e) {
			return false
		}
		base = e.Expr
	default:
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
	return Addressable(scope, base, exprType)
}

func MutableAddressable(scope *table.Scope, expr ast.Expr, exprType ExprTypeFunc) (mutable bool, sharedReference typeinfo.Type) {
	if scope == nil || expr == nil {
		return false, nil
	}
	var base ast.Expr
	switch e := expr.(type) {
	case *ast.Ident:
		return scope.IsMutableBinding(e.Name), nil
	case *ast.SelectorExpr:
		base = e.Expr
	case *ast.IndexExpr:
		if !isElementIndexExpr(e) {
			return false, nil
		}
		base = e.Expr
	default:
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
	return MutableAddressable(scope, base, exprType)
}

func LocalRoot(scope, moduleScope *table.Scope, expr ast.Expr, exprType ExprTypeFunc) (*symbols.Symbol, bool) {
	if scope == nil || moduleScope == nil || expr == nil {
		return nil, false
	}
	var base ast.Expr
	switch e := expr.(type) {
	case *ast.Ident:
		for current := scope; current != nil && current != moduleScope; current = current.Parent() {
			sym, found := current.LookupLocal(e.Name)
			if found {
				return sym, addressableSymbol(sym)
			}
		}
		return nil, false
	case *ast.SelectorExpr:
		base = e.Expr
	case *ast.IndexExpr:
		if !isElementIndexExpr(e) {
			return nil, false
		}
		base = e.Expr
	default:
		return nil, false
	}
	if exprType != nil {
		if _, ok := typeinfo.PointerTarget(typeinfo.Underlying(exprType(base))); ok {
			return nil, false
		}
	}
	return LocalRoot(scope, moduleScope, base, exprType)
}

func isElementIndexExpr(expr *ast.IndexExpr) bool {
	if expr == nil || expr.Index == nil {
		return false
	}
	_, slicing := expr.Index.(*ast.RangeExpr)
	return !slicing
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

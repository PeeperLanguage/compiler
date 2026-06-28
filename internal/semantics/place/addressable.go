package place

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

type ExprTypeFunc func(ast.Expr) typeinfo.Type

func Addressable(scope *table.Scope, expr ast.Expr, exprType ExprTypeFunc) bool {
	if scope == nil || expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ast.Ident:
		sym, found := scope.Lookup(e.Name)
		return found && addressableSymbol(sym)
	case *ast.SelectorExpr:
		if exprType != nil {
			if _, ok := typeinfo.Underlying(exprType(e.Expr)).(*typeinfo.RawPtrType); ok {
				return true
			}
		}
		return Addressable(scope, e.Expr, exprType)
	default:
		return false
	}
}

func MutableAddressable(scope *table.Scope, expr ast.Expr, exprType ExprTypeFunc) bool {
	if scope == nil || expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ast.Ident:
		return scope.IsMutableVar(e.Name)
	case *ast.SelectorExpr:
		if exprType != nil {
			if _, ok := typeinfo.Underlying(exprType(e.Expr)).(*typeinfo.RawPtrType); ok {
				return true
			}
		}
		return MutableAddressable(scope, e.Expr, exprType)
	default:
		return false
	}
}

func LocalRoot(scope, moduleScope *table.Scope, expr ast.Expr, exprType ExprTypeFunc) (*symbols.Symbol, bool) {
	if scope == nil || moduleScope == nil || expr == nil {
		return nil, false
	}
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
		if exprType != nil {
			if _, ok := typeinfo.Underlying(exprType(e.Expr)).(*typeinfo.RawPtrType); ok {
				return nil, false
			}
		}
		return LocalRoot(scope, moduleScope, e.Expr, exprType)
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

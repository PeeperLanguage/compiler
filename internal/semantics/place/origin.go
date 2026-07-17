package place

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

type OriginProjectionKind uint8

const (
	OriginPointee OriginProjectionKind = iota
	OriginField
	OriginIndex
	OriginWildcard
)

type OriginProjection struct {
	Kind  OriginProjectionKind
	Field string
	Index string
}

type Origin struct {
	Root        *symbols.Symbol
	Projections []OriginProjection
}

type OriginOptions struct {
	ExprType         ExprTypeFunc
	ReferenceOrigins func(*symbols.Symbol) []Origin
	ConstantIndex    func(ast.Expr) (string, bool)
}

// Origins resolves safe-reference dereferences eagerly. Canonical origins never
// retain a reference binding as storage identity when its referent is known.
func Origins(scope *table.Scope, expr ast.Expr, opts OriginOptions) []Origin {
	if scope == nil || expr == nil {
		return nil
	}
	switch node := expr.(type) {
	case *ast.AddressExpr:
		return Origins(scope, node.Expr, opts)
	case *ast.Ident:
		sym, found := scope.Lookup(node.Name)
		if !found || sym == nil {
			return nil
		}
		if typ, ok := symbols.GetSymbolType(sym); ok {
			if _, _, reference := typeinfo.ReferenceValueTarget(typ); reference && opts.ReferenceOrigins != nil {
				return CloneOrigins(opts.ReferenceOrigins(sym))
			}
		}
		return []Origin{{Root: sym}}
	case *ast.SelectorExpr:
		if node.Name == nil {
			return nil
		}
		origins := Origins(scope, node.Expr, opts)
		origins = appendIndirectProjection(origins, node.Expr, opts.ExprType)
		return appendOriginProjection(origins, OriginProjection{Kind: OriginField, Field: node.Name.Name})
	case *ast.IndexExpr:
		origins := Origins(scope, node.Expr, opts)
		origins = appendIndirectProjection(origins, node.Expr, opts.ExprType)
		if _, rangeIndex := node.Index.(*ast.RangeExpr); rangeIndex {
			return appendOriginProjection(origins, OriginProjection{Kind: OriginWildcard})
		}
		if opts.ConstantIndex != nil {
			if value, ok := opts.ConstantIndex(node.Index); ok {
				return appendOriginProjection(origins, OriginProjection{Kind: OriginIndex, Index: value})
			}
		}
		return appendOriginProjection(origins, OriginProjection{Kind: OriginWildcard})
	default:
		return nil
	}
}

func CloneOrigins(origins []Origin) []Origin {
	cloned := make([]Origin, len(origins))
	for i, origin := range origins {
		cloned[i] = origin
		cloned[i].Projections = append([]OriginProjection(nil), origin.Projections...)
	}
	return cloned
}

func MergeOrigins(left, right []Origin) []Origin {
	merged := CloneOrigins(left)
	for _, candidate := range right {
		found := false
		for _, existing := range merged {
			if sameOrigin(existing, candidate) {
				found = true
				break
			}
		}
		if !found {
			candidate.Projections = append([]OriginProjection(nil), candidate.Projections...)
			merged = append(merged, candidate)
		}
	}
	return merged
}

func SameOrigins(left, right []Origin) bool {
	if len(left) != len(right) {
		return false
	}
	for _, candidate := range left {
		found := false
		for _, existing := range right {
			if sameOrigin(existing, candidate) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func appendIndirectProjection(origins []Origin, base ast.Expr, exprType ExprTypeFunc) []Origin {
	if exprType == nil {
		return origins
	}
	if _, owned := typeinfo.PointerTarget(typeinfo.Underlying(exprType(base))); !owned {
		return origins
	}
	return appendOriginProjection(origins, OriginProjection{Kind: OriginPointee})
}

func appendOriginProjection(origins []Origin, projection OriginProjection) []Origin {
	out := CloneOrigins(origins)
	for i := range out {
		path := out[i].Projections
		if len(path) > 0 && path[len(path)-1].Kind == OriginWildcard {
			continue
		}
		out[i].Projections = append(path, projection)
	}
	return out
}

func sameOrigin(left, right Origin) bool {
	if left.Root != right.Root || len(left.Projections) != len(right.Projections) {
		return false
	}
	for i := range left.Projections {
		if left.Projections[i] != right.Projections[i] {
			return false
		}
	}
	return true
}

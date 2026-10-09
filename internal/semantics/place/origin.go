package place

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
)

type OriginProjectionKind uint8

const (
	OriginPointee OriginProjectionKind = iota
	OriginField
	OriginIndex
	OriginBindingIndex
	OriginVariantPayload
	OriginWildcard
)

type OriginProjection struct {
	Kind    OriginProjectionKind
	Field   string
	Index   string
	Binding *symbols.Symbol
	Case    int
}

type Origin struct {
	Root        *symbols.Symbol
	Projections []OriginProjection
}

// Resolution keeps carrier storage distinct from referenced value storage.
// Stable is false when any projection cannot retain identity across CFG sites.
type Resolution struct {
	StorageOrigins []Origin
	ValueOrigins   []Origin
	Dependencies   []*symbols.Symbol
	IsStable       bool
}

func resolveSymbol(scope *symbols.Scope, expr ast.Expr, resolve BindingResolver) (*symbols.Symbol, bool) {
	if expr == nil {
		return nil, false
	}
	if resolve != nil {
		if sym, found := resolve(expr); found {
			return sym, sym != nil
		}
	}
	if ident, ok := expr.(*ast.Ident); ok && ident != nil {
		return scope.Lookup(ident.Name)
	}
	return nil, false
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
			if areSameOrigin(existing, candidate) {
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

func AreSameOrigins(left, right []Origin) bool {
	if len(left) != len(right) {
		return false
	}
	for _, candidate := range left {
		found := false
		for _, existing := range right {
			if areSameOrigin(existing, candidate) {
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

// OriginsOverlap is conservative unless two canonical paths prove disjoint at
// a concrete field or fixed index. Prefixes overlap because one path names
// storage containing the other; symbolic indexes and wildcards may alias any
// indexed descendant.
func OriginsOverlap(left, right []Origin) bool {
	for _, leftOrigin := range left {
		for _, rightOrigin := range right {
			if originOverlap(leftOrigin, rightOrigin) {
				return true
			}
		}
	}
	return false
}

// VariantPayloadOrigins projects carrier storage through exact proven cases.
func VariantPayloadOrigins(origins []Origin, cases []int) []Origin {
	out := CloneOrigins(origins)
	for _, caseIndex := range cases {
		out = appendOriginProjection(out, OriginProjection{Kind: OriginVariantPayload, Case: caseIndex})
	}
	return out
}

// FieldOrigins projects aggregate storage through one named field.
func FieldOrigins(origins []Origin, name string) []Origin {
	return appendOriginProjection(origins, OriginProjection{Kind: OriginField, Field: name})
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

func areSameOrigin(left, right Origin) bool {
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

func originOverlap(left, right Origin) bool {
	if left.Root == nil || left.Root != right.Root {
		return false
	}
	limit := min(len(left.Projections), len(right.Projections))
	for i := range limit {
		leftProjection := left.Projections[i]
		rightProjection := right.Projections[i]
		if leftProjection == rightProjection {
			continue
		}
		if leftProjection.Kind == OriginWildcard || rightProjection.Kind == OriginWildcard {
			return true
		}
		if leftProjection.Kind == OriginField && rightProjection.Kind == OriginField {
			return false
		}
		if leftProjection.Kind == OriginIndex && rightProjection.Kind == OriginIndex {
			return false
		}
		return true
	}
	return true
}

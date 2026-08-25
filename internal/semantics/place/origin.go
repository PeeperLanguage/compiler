package place

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
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

type ResolveOptions struct {
	ExprType          ExprTypeFunc
	ResolveBinding    BindingResolver
	ReferenceOrigins  func([]Origin) []Origin
	RawPointerOrigins func([]Origin) []Origin
	CallOrigins       func(*ast.CallExpr) []Origin
	ConstantIndex     func(ast.Expr) (string, bool)
	PayloadCases      func(ast.Expr) []int
}

// Resolution keeps carrier storage distinct from referenced value storage.
// Stable is false when any projection cannot retain identity across CFG sites.
type Resolution struct {
	StorageOrigins []Origin
	ValueOrigins   []Origin
	Dependencies   []*symbols.Symbol
	Stable         bool
}

// Resolve is the canonical place walk. Value origins preserve the previous
// eager safe-reference normalization; storage origins retain carrier identity.
func Resolve(scope *symbols.Scope, expr ast.Expr, opts ResolveOptions) Resolution {
	if scope == nil || expr == nil {
		return Resolution{}
	}
	switch node := expr.(type) {
	case *ast.AddressExpr:
		resolved := Resolve(scope, node.Expr, opts)
		if opts.PayloadCases != nil {
			if cases := opts.PayloadCases(node.Expr); len(cases) > 0 {
				resolved.ValueOrigins = VariantPayloadOrigins(resolved.StorageOrigins, cases)
			}
		}
		return resolved
	case *ast.Ident:
		sym, found := resolveSymbol(scope, node, opts.ResolveBinding)
		if !found || sym == nil {
			return Resolution{}
		}
		storage := []Origin{{Root: sym}}
		if typ, ok := symbols.GetSymbolType(sym); ok {
			return resolveStoredValueOrigins(typ, Resolution{
				StorageOrigins: storage, ValueOrigins: CloneOrigins(storage), Stable: true,
			}, opts)
		}
		return Resolution{StorageOrigins: storage, ValueOrigins: CloneOrigins(storage), Stable: true}
	case *ast.SelectorExpr:
		if node.Name == nil {
			return Resolution{}
		}
		base := Resolve(scope, node.Expr, opts)
		origins := appendVariantPayloadProjections(base.ValueOrigins, node.Expr, opts.ExprType, opts.PayloadCases)
		origins = appendIndirectProjection(origins, node.Expr, opts.ExprType)
		origins = appendOriginProjection(origins, OriginProjection{Kind: OriginField, Field: node.Name.Name})
		resolved := Resolution{
			StorageOrigins: origins,
			ValueOrigins:   CloneOrigins(origins),
			Dependencies:   append([]*symbols.Symbol(nil), base.Dependencies...),
			Stable:         base.Stable && len(origins) > 0,
		}
		return resolveStoredExpressionOrigins(node, resolved, opts)
	case *ast.IndexExpr:
		base := Resolve(scope, node.Expr, opts)
		origins := appendVariantPayloadProjections(base.ValueOrigins, node.Expr, opts.ExprType, opts.PayloadCases)
		origins = appendIndirectProjection(origins, node.Expr, opts.ExprType)
		dependencies := append([]*symbols.Symbol(nil), base.Dependencies...)
		if _, rangeIndex := node.Index.(*ast.RangeExpr); rangeIndex {
			origins = appendOriginProjection(origins, OriginProjection{Kind: OriginWildcard})
			return Resolution{StorageOrigins: origins, ValueOrigins: CloneOrigins(origins)}
		}
		if opts.ConstantIndex != nil {
			if value, ok := opts.ConstantIndex(node.Index); ok {
				origins = appendOriginProjection(origins, OriginProjection{Kind: OriginIndex, Index: value})
				resolved := Resolution{
					StorageOrigins: origins,
					ValueOrigins:   CloneOrigins(origins),
					Dependencies:   dependencies,
					Stable:         base.Stable && len(origins) > 0,
				}
				return resolveStoredExpressionOrigins(node, resolved, opts)
			}
		}
		if index, ok := node.Index.(*ast.Ident); ok {
			if sym, found := resolveSymbol(scope, index, opts.ResolveBinding); found && sym != nil {
				if typ, typed := symbols.GetSymbolType(sym); typed && typeinfo.IsIntegral(typ) {
					origins = appendOriginProjection(origins, OriginProjection{Kind: OriginBindingIndex, Binding: sym})
					dependencies = append(dependencies, sym)
					resolved := Resolution{
						StorageOrigins: origins,
						ValueOrigins:   CloneOrigins(origins),
						Dependencies:   dependencies,
						Stable:         base.Stable && len(origins) > 0,
					}
					return resolveStoredExpressionOrigins(node, resolved, opts)
				}
			}
		}
		origins = appendOriginProjection(origins, OriginProjection{Kind: OriginWildcard})
		return Resolution{StorageOrigins: origins, ValueOrigins: CloneOrigins(origins), Dependencies: dependencies}
	case *ast.CallExpr:
		if opts.CallOrigins != nil {
			return Resolution{ValueOrigins: CloneOrigins(opts.CallOrigins(node))}
		}
		return Resolution{}
	default:
		return Resolution{}
	}
}

func resolveStoredExpressionOrigins(expr ast.Expr, resolved Resolution, opts ResolveOptions) Resolution {
	if opts.ExprType == nil {
		return resolved
	}
	return resolveStoredValueOrigins(opts.ExprType(expr), resolved, opts)
}

func resolveStoredValueOrigins(typ typeinfo.Type, resolved Resolution, opts ResolveOptions) Resolution {
	if _, _, reference := typeinfo.ReferenceValueTarget(typ); reference && opts.ReferenceOrigins != nil {
		resolved.ValueOrigins = CloneOrigins(opts.ReferenceOrigins(resolved.StorageOrigins))
	} else if _, raw := typeinfo.Underlying(typ).(*typeinfo.RawPtrType); raw && opts.RawPointerOrigins != nil {
		resolved.ValueOrigins = CloneOrigins(opts.RawPointerOrigins(resolved.StorageOrigins))
	}
	return resolved
}

func resolveSymbol(scope *symbols.Scope, ident *ast.Ident, resolve BindingResolver) (*symbols.Symbol, bool) {
	if ident == nil {
		return nil, false
	}
	if resolve != nil {
		if binding, found := resolve(ident); found {
			return binding.Symbol, binding.Symbol != nil
		}
	}
	return scope.Lookup(ident.Name)
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

func appendIndirectProjection(origins []Origin, base ast.Expr, exprType ExprTypeFunc) []Origin {
	if exprType == nil {
		return origins
	}
	if _, owned := typeinfo.PointerTarget(typeinfo.Underlying(exprType(base))); !owned {
		return origins
	}
	return appendOriginProjection(origins, OriginProjection{Kind: OriginPointee})
}

func appendVariantPayloadProjections(
	origins []Origin,
	base ast.Expr,
	exprType ExprTypeFunc,
	payloadCases func(ast.Expr) []int,
) []Origin {
	if payloadCases == nil {
		return origins
	}
	if exprType != nil {
		if _, _, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(exprType(base))); reference {
			return origins
		}
	}
	return VariantPayloadOrigins(origins, payloadCases(base))
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

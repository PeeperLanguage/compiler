package place

import (
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func TestPlaceExpressionProjectionGrammar(t *testing.T) {
	value := &ast.Ident{Name: "value"}
	field := &ast.SelectorExpr{Expr: value, Name: &ast.Ident{Name: "field"}}
	index := &ast.IndexExpr{Expr: field, Index: &ast.NumberLit{Value: "0"}}
	rangeIndex := &ast.IndexExpr{
		Expr:  value,
		Index: &ast.RangeExpr{Start: &ast.NumberLit{Value: "0"}, End: &ast.NumberLit{Value: "1"}},
	}
	missingIndex := &ast.IndexExpr{Expr: value}
	missingSelectorBase := &ast.SelectorExpr{Name: &ast.Ident{Name: "field"}}
	missingIndexBase := &ast.IndexExpr{Index: &ast.NumberLit{Value: "0"}}
	call := &ast.CallExpr{Callee: value}
	var nilSelector *ast.SelectorExpr

	tests := []struct {
		name string
		expr ast.Expr
		want bool
	}{
		{name: "identifier", expr: value, want: true},
		{name: "selector", expr: field, want: true},
		{name: "nested index", expr: index, want: true},
		{name: "range", expr: rangeIndex},
		{name: "missing index", expr: missingIndex},
		{name: "missing selector base", expr: missingSelectorBase},
		{name: "missing index base", expr: missingIndexBase},
		{name: "call", expr: call},
		{name: "nil selector", expr: nilSelector},
		{name: "nil expression"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsPlaceExpr(test.expr); got != test.want {
				t.Fatalf("IsPlaceExpr() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPlaceAddressabilityUsesResolvedBindingBeforeScope(t *testing.T) {
	scope := symbols.NewScope(nil)
	scopeValue := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolVar, "value"), "value", symbols.SymbolVar, &ast.LetDecl{IsMutable: true}, nil)
	if err := scope.Declare(scopeValue); err != nil {
		t.Fatal(err)
	}
	resolvedValue := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolConst, "value"), "value", symbols.SymbolConst, nil, nil)
	projection := &ast.SelectorExpr{Expr: &ast.Ident{Name: "value"}, Name: &ast.Ident{Name: "field"}}
	resolve := func(ast.Expr) (*symbols.Symbol, bool) {
		return resolvedValue, true
	}
	if !IsAddressable(scope, projection, nil, resolve) {
		t.Fatal("Addressable() rejected resolved binding")
	}

	resolve = func(ast.Expr) (*symbols.Symbol, bool) {
		return symbols.New(symbols.ProjectedSymbolID(symbols.SymbolFunc, "value"), "value", symbols.SymbolFunc, nil, nil), true
	}
	if IsAddressable(scope, projection, nil, resolve) {
		t.Fatal("Addressable() fell back to shadowed scope binding")
	}
}

func TestPlaceAddressabilityPointerAndReferenceBoundaries(t *testing.T) {
	scope := symbols.NewScope(nil)
	base := &ast.Ident{Name: "value"}
	projection := &ast.SelectorExpr{Expr: base, Name: &ast.Ident{Name: "field"}}
	tests := []struct {
		name string
		typ  typeinfo.Type
		want bool
	}{
		{name: "owned pointer", typ: &typeinfo.OwnedPtrType{Target: typeinfo.DefaultIntegerType()}, want: true},
		{name: "reference", typ: &typeinfo.RefType{Target: typeinfo.DefaultIntegerType()}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exprType := func(expr ast.Expr) typeinfo.Type {
				if expr == base {
					return test.typ
				}
				return nil
			}
			if got := IsAddressable(scope, projection, exprType, nil); got != test.want {
				t.Fatalf("Addressable() = %v, want %v", got, test.want)
			}
		})
	}

	mutableReference := &typeinfo.RefType{IsMutable: true, Target: typeinfo.DefaultIntegerType()}
	sharedReference := &typeinfo.RefType{Target: typeinfo.DefaultIntegerType()}
	for _, test := range []struct {
		name   string
		typ    typeinfo.Type
		want   bool
		shared typeinfo.Type
	}{
		{name: "raw pointer", typ: &typeinfo.RawPtrType{}, want: true},
		{name: "mutable reference", typ: mutableReference, want: true},
		{name: "shared reference", typ: sharedReference, shared: typeinfo.DefaultIntegerType()},
	} {
		t.Run(test.name, func(t *testing.T) {
			exprType := func(expr ast.Expr) typeinfo.Type {
				if expr == base {
					return test.typ
				}
				return nil
			}
			mutable, shared, _ := MutableAddressable(scope, projection, exprType, nil)
			if mutable != test.want || !typeinfo.IsSameType(shared, test.shared) {
				t.Fatalf("MutableAddressable() = (%v, %v), want (%v, %v)", mutable, shared, test.want, test.shared)
			}
		})
	}
}

func TestMergeOriginsUnionsWithoutAliasingInputPaths(t *testing.T) {
	leftRoot := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolVar, "left"), "left", symbols.SymbolVar, nil, nil)
	rightRoot := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolVar, "right"), "right", symbols.SymbolVar, nil, nil)
	left := []Origin{{Root: leftRoot, Projections: []OriginProjection{{Kind: OriginField, Field: "value"}}}}
	right := []Origin{
		{Root: leftRoot, Projections: []OriginProjection{{Kind: OriginField, Field: "value"}}},
		{Root: rightRoot},
	}

	merged := MergeOrigins(left, right)
	if len(merged) != 2 || !AreSameOrigins(merged, []Origin{left[0], right[1]}) {
		t.Fatalf("merged origins = %#v", merged)
	}
	merged[0].Projections[0].Field = "changed"
	if left[0].Projections[0].Field != "value" {
		t.Fatalf("merge aliased input projection storage")
	}
}

func TestOriginsOverlap(t *testing.T) {
	root := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolVar, "root"), "root", symbols.SymbolVar, nil, nil)
	other := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolVar, "other"), "other", symbols.SymbolVar, nil, nil)
	field := func(name string) OriginProjection { return OriginProjection{Kind: OriginField, Field: name} }
	index := func(value string) OriginProjection { return OriginProjection{Kind: OriginIndex, Index: value} }
	bindingIndex := func(binding *symbols.Symbol) OriginProjection {
		return OriginProjection{Kind: OriginBindingIndex, Binding: binding}
	}
	wildcard := OriginProjection{Kind: OriginWildcard}
	leftIndex := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolVar, "leftIndex"), "leftIndex", symbols.SymbolVar, nil, nil)
	rightIndex := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolVar, "rightIndex"), "rightIndex", symbols.SymbolVar, nil, nil)

	tests := []struct {
		name    string
		left    []Origin
		right   []Origin
		overlap bool
	}{
		{name: "same root", left: []Origin{{Root: root}}, right: []Origin{{Root: root}}, overlap: true},
		{name: "different roots", left: []Origin{{Root: root}}, right: []Origin{{Root: other}}},
		{name: "path prefix", left: []Origin{{Root: root}}, right: []Origin{{Root: root, Projections: []OriginProjection{field("value")}}}, overlap: true},
		{name: "same field", left: []Origin{{Root: root, Projections: []OriginProjection{field("value")}}}, right: []Origin{{Root: root, Projections: []OriginProjection{field("value")}}}, overlap: true},
		{name: "different fields", left: []Origin{{Root: root, Projections: []OriginProjection{field("left")}}}, right: []Origin{{Root: root, Projections: []OriginProjection{field("right")}}}},
		{name: "different fixed indexes", left: []Origin{{Root: root, Projections: []OriginProjection{index("0")}}}, right: []Origin{{Root: root, Projections: []OriginProjection{index("1")}}}},
		{name: "same binding index", left: []Origin{{Root: root, Projections: []OriginProjection{bindingIndex(leftIndex)}}}, right: []Origin{{Root: root, Projections: []OriginProjection{bindingIndex(leftIndex)}}}, overlap: true},
		{name: "different binding indexes may alias", left: []Origin{{Root: root, Projections: []OriginProjection{bindingIndex(leftIndex)}}}, right: []Origin{{Root: root, Projections: []OriginProjection{bindingIndex(rightIndex)}}}, overlap: true},
		{name: "binding and fixed indexes may alias", left: []Origin{{Root: root, Projections: []OriginProjection{bindingIndex(leftIndex)}}}, right: []Origin{{Root: root, Projections: []OriginProjection{index("1")}}}, overlap: true},
		{name: "wildcard index", left: []Origin{{Root: root, Projections: []OriginProjection{wildcard}}}, right: []Origin{{Root: root, Projections: []OriginProjection{index("1")}}}, overlap: true},
		{name: "different projection kinds", left: []Origin{{Root: root, Projections: []OriginProjection{field("value")}}}, right: []Origin{{Root: root, Projections: []OriginProjection{index("0")}}}, overlap: true},
		{name: "any origin pair", left: []Origin{{Root: other}, {Root: root, Projections: []OriginProjection{field("value")}}}, right: []Origin{{Root: root, Projections: []OriginProjection{field("value")}}}, overlap: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := OriginsOverlap(test.left, test.right); got != test.overlap {
				t.Fatalf("OriginsOverlap() = %v, want %v", got, test.overlap)
			}
		})
	}
}

func TestVariantPayloadOriginsPreserveExactCasePath(t *testing.T) {
	root := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolVar, "value"), "value", symbols.SymbolVar, nil, nil)
	origins := VariantPayloadOrigins([]Origin{{Root: root}}, []int{2, 1})
	want := []Origin{{Root: root, Projections: []OriginProjection{
		{Kind: OriginVariantPayload, Case: 2},
		{Kind: OriginVariantPayload, Case: 1},
	}}}
	if !AreSameOrigins(origins, want) {
		t.Fatalf("variant payload origins = %#v, want %#v", origins, want)
	}
}

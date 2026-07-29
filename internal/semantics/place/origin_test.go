package place

import (
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

func TestOriginsPreferResolvedBindingOverShadowingScope(t *testing.T) {
	scope := table.New(nil)
	callerValue := symbols.New("value", symbols.SymbolVar, nil, nil)
	declarationValue := symbols.New("value", symbols.SymbolConst, nil, nil)
	if err := scope.Declare(callerValue); err != nil {
		t.Fatal(err)
	}

	ident := &ast.Ident{Name: "value"}
	origins := Origins(scope, ident, OriginOptions{
		ResolveBinding: func(*ast.Ident) (Binding, bool) {
			return Binding{Symbol: declarationValue}, true
		},
	})
	if !SameOrigins(origins, []Origin{{Root: declarationValue}}) {
		t.Fatalf("origins = %#v, want declaration binding", origins)
	}
}

func TestOriginsNormalizeReferenceRootsAndProjections(t *testing.T) {
	scope := table.New(nil)
	value := symbols.New("value", symbols.SymbolVar, nil, nil)
	value.BindType(&typeinfo.StructType{Fields: []typeinfo.Field{{Name: "items", Type: &typeinfo.ArrayType{Len: "2", Elem: typeinfo.DefaultIntegerType()}}}})
	reference := symbols.New("reference", symbols.SymbolVar, nil, nil)
	reference.BindType(&typeinfo.RefType{Target: value.Type})
	if err := scope.Declare(value); err != nil {
		t.Fatal(err)
	}
	if err := scope.Declare(reference); err != nil {
		t.Fatal(err)
	}

	base := &ast.Ident{Name: "reference"}
	field := &ast.SelectorExpr{Expr: base, Name: &ast.Ident{Name: "items"}}
	index := &ast.IndexExpr{Expr: field, Index: &ast.NumberLit{Value: "1"}}
	types := map[ast.Expr]typeinfo.Type{
		base:  reference.Type,
		field: value.Type.(*typeinfo.StructType).Fields[0].Type,
	}
	origins := Origins(scope, index, OriginOptions{
		ExprType: func(expr ast.Expr) typeinfo.Type { return types[expr] },
		ReferenceOrigins: func(sym *symbols.Symbol) []Origin {
			if sym == reference {
				return []Origin{{Root: value}}
			}
			return nil
		},
		ConstantIndex: func(ast.Expr) (string, bool) { return "1", true },
	})
	want := []Origin{{Root: value, Projections: []OriginProjection{
		{Kind: OriginField, Field: "items"},
		{Kind: OriginIndex, Index: "1"},
	}}}
	if !SameOrigins(origins, want) {
		t.Fatalf("origins = %#v, want %#v", origins, want)
	}
}

func TestOriginsPreserveOwningPointeeAndCollapseUnknownDescendants(t *testing.T) {
	scope := table.New(nil)
	owner := symbols.New("owner", symbols.SymbolVar, nil, nil)
	inner := &typeinfo.ArrayType{Len: "2", Elem: typeinfo.DefaultIntegerType()}
	owner.BindType(&typeinfo.OwnedPtrType{Target: &typeinfo.ArrayType{Len: "2", Elem: inner}})
	if err := scope.Declare(owner); err != nil {
		t.Fatal(err)
	}

	base := &ast.Ident{Name: "owner"}
	unknown := &ast.Ident{Name: "index"}
	first := &ast.IndexExpr{Expr: base, Index: unknown}
	second := &ast.IndexExpr{Expr: first, Index: &ast.NumberLit{Value: "0"}}
	types := map[ast.Expr]typeinfo.Type{
		base:  owner.Type,
		first: inner,
	}
	origins := Origins(scope, second, OriginOptions{
		ExprType: func(expr ast.Expr) typeinfo.Type { return types[expr] },
		ConstantIndex: func(expr ast.Expr) (string, bool) {
			literal, ok := expr.(*ast.NumberLit)
			if !ok {
				return "", false
			}
			return literal.Value, true
		},
	})
	want := []Origin{{Root: owner, Projections: []OriginProjection{
		{Kind: OriginPointee},
		{Kind: OriginWildcard},
	}}}
	if !SameOrigins(origins, want) {
		t.Fatalf("origins = %#v, want %#v", origins, want)
	}
}

func TestMergeOriginsUnionsWithoutAliasingInputPaths(t *testing.T) {
	leftRoot := symbols.New("left", symbols.SymbolVar, nil, nil)
	rightRoot := symbols.New("right", symbols.SymbolVar, nil, nil)
	left := []Origin{{Root: leftRoot, Projections: []OriginProjection{{Kind: OriginField, Field: "value"}}}}
	right := []Origin{
		{Root: leftRoot, Projections: []OriginProjection{{Kind: OriginField, Field: "value"}}},
		{Root: rightRoot},
	}

	merged := MergeOrigins(left, right)
	if len(merged) != 2 || !SameOrigins(merged, []Origin{left[0], right[1]}) {
		t.Fatalf("merged origins = %#v", merged)
	}
	merged[0].Projections[0].Field = "changed"
	if left[0].Projections[0].Field != "value" {
		t.Fatalf("merge aliased input projection storage")
	}
}

func TestOriginsOverlap(t *testing.T) {
	root := symbols.New("root", symbols.SymbolVar, nil, nil)
	other := symbols.New("other", symbols.SymbolVar, nil, nil)
	field := func(name string) OriginProjection { return OriginProjection{Kind: OriginField, Field: name} }
	index := func(value string) OriginProjection { return OriginProjection{Kind: OriginIndex, Index: value} }
	wildcard := OriginProjection{Kind: OriginWildcard}

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

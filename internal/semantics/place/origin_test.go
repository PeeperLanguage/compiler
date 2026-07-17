package place

import (
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

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

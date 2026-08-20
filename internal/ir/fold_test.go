package ir

import (
	"compiler/internal/constvalue"
	"compiler/internal/source"
	"testing"
)

func TestFoldExprConstantArithmetic(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, Signed: true, Bits: 32})
	expr := &Binary{
		Op:   "+",
		Left: &IntLit{Value: "2", Type: i32},
		Right: &Binary{
			Op:    "*",
			Left:  &IntLit{Value: "3", Type: i32},
			Right: &IntLit{Value: "4", Type: i32},
			Type:  i32,
		},
		Type: i32,
	}
	folded := FoldExpr(types, expr, nil)
	lit, ok := folded.(*IntLit)
	if !ok || lit.Value != "14" {
		t.Fatalf("expected 14, got %#v", folded)
	}
}

func TestFoldExprPreservesExpressionOrigin(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, Signed: true, Bits: 32})
	expr := &Binary{
		Op:     "+",
		Left:   &IntLit{Value: "2", Type: i32},
		Right:  &IntLit{Value: "3", Type: i32},
		Type:   i32,
		NodeID: 73,
	}
	folded, ok := FoldExpr(types, expr, nil).(*IntLit)
	if !ok || folded.NodeID != expr.NodeID {
		t.Fatalf("folded origin = %#v, want NodeID %d", folded, expr.NodeID)
	}
}

func TestFoldExprConstantCondition(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, Signed: true, Bits: 32})
	boolType := types.Intern(Type{Kind: TypeBool})
	expr := &Binary{
		Op:    "<",
		Left:  &IntLit{Value: "1", Type: i32},
		Right: &IntLit{Value: "2", Type: i32},
		Type:  boolType,
	}
	folded := FoldExpr(types, expr, nil)
	lit, ok := folded.(*BoolLit)
	if !ok || !lit.Value {
		t.Fatalf("expected true bool literal, got %#v", folded)
	}
}

func TestFoldExprConstEnv(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, Signed: true, Bits: 32})
	expr := &Binary{
		Op:    "+",
		Left:  &Ident{Name: "a$1", Type: i32},
		Right: &IntLit{Value: "5", Type: i32},
		Type:  i32,
	}
	value, ok := constvalue.NewIntText("2", "i32")
	if !ok {
		t.Fatal("NewIntText failed")
	}
	folded := FoldExpr(types, expr, map[string]constvalue.Value{
		"a$1": value,
	})
	lit, ok := folded.(*IntLit)
	if !ok || lit.Value != "7" {
		t.Fatalf("expected 7, got %#v", folded)
	}
}

func TestFoldExprPreservesLoadIdentity(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, Signed: true, Bits: 32})
	arrayI32 := types.Intern(Type{Kind: TypeArray, Elem: i32, Length: "3"})
	loc := &source.Location{}
	root := &Ident{Name: "values", Type: arrayI32}
	expr := &Load{
		Place: &Place{
			Root: root,
			Projections: []PlaceProjection{{
				Kind: PlaceProjectionIndex,
				Index: &Binary{
					Op:    "+",
					Left:  &IntLit{Value: "1", Type: i32},
					Right: &IntLit{Value: "1", Type: i32},
					Type:  i32,
				},
				Type: i32,
			}},
			Type: i32,
		},
		DropRoot: true,
		NodeID:   42,
		Location: loc,
	}

	folded, ok := FoldExpr(types, expr, nil).(*Load)
	if !ok {
		t.Fatalf("folded expression = %#v, want load", folded)
	}
	if folded.NodeID != expr.NodeID || !folded.DropRoot || folded.Location != loc || folded.Place.Root != root {
		t.Fatalf("folded load identity = %#v, want NodeID, drop root, location, and place root preserved", folded)
	}
	index, ok := folded.Place.Projections[0].Index.(*IntLit)
	if !ok || index.Value != "2" {
		t.Fatalf("folded index = %#v, want literal 2", folded.Place.Projections[0].Index)
	}
}

package ir

import (
	"strings"
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/source"
)

type unhandledFoldExpr struct{}

func (*unhandledFoldExpr) exprNode()                  {}
func (*unhandledFoldExpr) inspectChildren(func(Expr)) {}
func (*unhandledFoldExpr) String() string             { return "unhandled" }
func (*unhandledFoldExpr) TypeID() TypeID             { return InvalidType }
func (*unhandledFoldExpr) Origin() SourceInfo         { return SourceInfo{} }
func (*unhandledFoldExpr) setOrigin(SourceInfo)       {}

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

func TestFoldExprFoldsEveryCompositeExpression(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, Signed: true, Bits: 32})
	loc := &source.Location{}
	foldable := func() Expr {
		return &Binary{
			Op:       "+",
			Left:     &IntLit{Value: "1", Type: i32},
			Right:    &IntLit{Value: "2", Type: i32},
			Type:     i32,
			NodeID:   5,
			Location: loc,
		}
	}
	place := func() *Place {
		return &Place{
			Root: &Ident{Name: "storage", Type: i32},
			Projections: []PlaceProjection{{
				Kind:  PlaceProjectionIndex,
				Index: foldable(),
				Type:  i32,
			}},
			Type:     i32,
			Location: loc,
		}
	}
	tests := []struct {
		name string
		expr Expr
	}{
		{name: "optional", expr: &OptionalSome{Value: foldable(), Type: i32, NodeID: 9, Location: loc}},
		{name: "unary", expr: &Unary{Op: "opaque", Arg: foldable(), Type: i32, NodeID: 9, Location: loc}},
		{name: "binary", expr: &Binary{Op: "opaque", Left: foldable(), Right: foldable(), Type: i32, NodeID: 9, Location: loc}},
		{name: "call", expr: &Call{Callee: foldable(), Args: []Expr{foldable()}, Type: i32, NodeID: 9, Location: loc}},
		{name: "load", expr: &Load{Place: place(), DropRoot: true, NodeID: 9, Location: loc}},
		{name: "address", expr: &AddrOf{Place: place(), Type: i32, NodeID: 9, Location: loc}},
		{name: "temporary borrow", expr: &TempBorrow{Value: foldable(), Slice: true, Type: i32, NodeID: 9, Location: loc}},
		{name: "length", expr: &Len{Value: foldable(), Type: i32, NodeID: 9, Location: loc}},
		{name: "string chars", expr: &StringChars{Value: foldable(), Type: i32, NodeID: 9, Location: loc}},
		{name: "slice", expr: &SliceView{Source: place(), Start: foldable(), End: foldable(), EndExclusive: true, Type: i32, NodeID: 9, Location: loc}},
		{name: "interface make", expr: &InterfaceMake{Value: foldable(), Slots: []InterfaceSlot{{MethodName: "method"}}, Type: i32, NodeID: 9, Location: loc}},
		{name: "interface call", expr: &InterfaceCall{Base: foldable(), Slot: 2, Args: []Expr{foldable()}, Consumes: true, Type: i32, NodeID: 9, Location: loc}},
		{name: "field", expr: &Field{Base: foldable(), Index: 3, DropBase: true, Type: i32, NodeID: 9, Location: loc}},
		{name: "struct", expr: &StructLit{Fields: []Expr{foldable()}, Type: i32, NodeID: 9, Location: loc}},
		{name: "array", expr: &ArrayLit{Values: []Expr{foldable()}, Dynamic: true, Type: i32, NodeID: 9, Location: loc}},
		{name: "dynamic array operation", expr: &DynamicArrayOp{Array: foldable(), Length: foldable(), Value: foldable(), ArrayType: i32, Type: i32, NodeID: 9, Location: loc}},
		{name: "allocation", expr: &AllocExpr{Value: foldable(), Allocator: foldable(), Type: i32, NodeID: 9, Location: loc}},
		{name: "cast", expr: &Cast{Expr: foldable(), Type: i32, NodeID: 9, Location: loc}},
		{name: "print", expr: &Print{Value: foldable(), Newline: true, NodeID: 9, Location: loc}},
		{name: "drop", expr: &Drop{Value: foldable(), NodeID: 9, Location: loc}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			folded := FoldExpr(types, test.expr, nil)
			var unfolded bool
			var threes int
			InspectExpr(folded, func(expr Expr) bool {
				switch expr := expr.(type) {
				case *Binary:
					if expr.Op == "+" {
						unfolded = true
					}
				case *IntLit:
					if expr.Value == "3" {
						threes++
					}
				}
				return true
			})
			if unfolded || threes == 0 {
				t.Fatalf("folded expression = %#v, unfolded=%t threes=%d", folded, unfolded, threes)
			}
			if origin := folded.Origin(); origin.NodeID != 9 || origin.Location != loc {
				t.Fatalf("folded origin = %#v, want NodeID 9 and original location", origin)
			}
		})
	}
}

func TestFoldExprPreservesCompositeMetadata(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, Signed: true, Bits: 32})
	value := &IntLit{Value: "1", Type: i32}
	slots := []InterfaceSlot{{MethodName: "method"}}
	tests := []struct {
		name  string
		expr  Expr
		check func(Expr) bool
	}{
		{name: "temporary borrow", expr: &TempBorrow{Value: value, Slice: true}, check: func(expr Expr) bool { return expr.(*TempBorrow).Slice }},
		{name: "interface make", expr: &InterfaceMake{Value: value, Slots: slots}, check: func(expr Expr) bool { return len(expr.(*InterfaceMake).Slots) == 1 }},
		{name: "interface call", expr: &InterfaceCall{Base: value, Slot: 3, Consumes: true}, check: func(expr Expr) bool { node := expr.(*InterfaceCall); return node.Slot == 3 && node.Consumes }},
		{name: "field", expr: &Field{Base: value, Index: 4, DropBase: true}, check: func(expr Expr) bool { node := expr.(*Field); return node.Index == 4 && node.DropBase }},
		{name: "array", expr: &ArrayLit{Values: []Expr{value}, Dynamic: true}, check: func(expr Expr) bool { return expr.(*ArrayLit).Dynamic }},
		{name: "print", expr: &Print{Value: value, Newline: true}, check: func(expr Expr) bool { return expr.(*Print).Newline }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if folded := FoldExpr(types, test.expr, nil); !test.check(folded) {
				t.Fatalf("metadata lost from %s: %v", test.name, folded)
			}
		})
	}
}

func TestFoldExprPanicsOnUnhandledExpression(t *testing.T) {
	defer func() {
		panicValue := recover()
		if panicValue == nil || !strings.Contains(panicValue.(string), "unhandled IR expression") {
			t.Fatalf("panic = %v, want unhandled IR expression", panicValue)
		}
	}()
	FoldExpr(NewTypeTable(), &unhandledFoldExpr{}, nil)
}

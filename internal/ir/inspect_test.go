package ir

import (
	"strings"
	"testing"
)

func ident(name string) *Ident { return &Ident{Name: name} }

func TestInspectExprVisitsCompositeChildrenInOrder(t *testing.T) {
	place := func() *Place {
		return &Place{
			Root: ident("root"),
			Projections: []PlaceProjection{
				{Kind: PlaceProjectionDeref},
				{Kind: PlaceProjectionIndex, Index: ident("index")},
			},
		}
	}
	tests := []struct {
		name string
		expr Expr
		want string
	}{
		{name: "optional", expr: &OptionalSome{Value: ident("value")}, want: "value"},
		{name: "unary", expr: &Unary{Arg: ident("arg")}, want: "arg"},
		{name: "binary", expr: &Binary{Left: ident("left"), Right: ident("right")}, want: "left,right"},
		{name: "call", expr: &Call{Callee: ident("callee"), Args: []Expr{ident("first"), ident("second")}}, want: "callee,first,second"},
		{name: "load", expr: &Load{Place: place()}, want: "root,index"},
		{name: "address", expr: &AddrOf{Place: place()}, want: "root,index"},
		{name: "temporary borrow", expr: &TempBorrow{Value: ident("value")}, want: "value"},
		{name: "length", expr: &Len{Value: ident("value")}, want: "value"},
		{name: "string chars", expr: &StringChars{Value: ident("value")}, want: "value"},
		{name: "slice view", expr: &SliceView{Source: place(), Start: ident("start"), End: ident("end")}, want: "root,index,start,end"},
		{name: "interface make", expr: &InterfaceMake{Value: ident("value")}, want: "value"},
		{name: "interface call", expr: &InterfaceCall{Base: ident("base"), Args: []Expr{ident("arg")}}, want: "base,arg"},
		{name: "field", expr: &Field{Base: ident("base")}, want: "base"},
		{name: "struct", expr: &StructLit{Fields: []Expr{ident("first"), ident("second")}}, want: "first,second"},
		{name: "array", expr: &ArrayLit{Values: []Expr{ident("first"), ident("second")}}, want: "first,second"},
		{name: "dynamic array operation", expr: &DynamicArrayOp{Array: ident("array"), Length: ident("length"), Value: ident("value")}, want: "array,length,value"},
		{name: "allocation", expr: &AllocExpr{Value: ident("value"), Allocator: ident("allocator")}, want: "value,allocator"},
		{name: "cast", expr: &Cast{Expr: ident("value")}, want: "value"},
		{name: "print", expr: &Print{Value: ident("value")}, want: "value"},
		{name: "drop", expr: &Drop{Value: ident("value")}, want: "value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var names []string
			InspectExpr(test.expr, func(expr Expr) bool {
				if value, ok := expr.(*Ident); ok {
					names = append(names, value.Name)
				}
				return true
			})
			if got := strings.Join(names, ","); got != test.want {
				t.Fatalf("visited identifiers = %q, want %q", got, test.want)
			}
		})
	}
}

func TestInspectExprPrunesChildren(t *testing.T) {
	expr := &Binary{Left: ident("left"), Right: &Unary{Arg: ident("hidden")}}
	var names []string
	InspectExpr(expr, func(expr Expr) bool {
		if value, ok := expr.(*Ident); ok {
			names = append(names, value.Name)
		}
		_, unary := expr.(*Unary)
		return !unary
	})
	if got, want := strings.Join(names, ","), "left"; got != want {
		t.Fatalf("visited identifiers = %q, want %q", got, want)
	}
}

func TestInspectPlaceVisitsRootBeforeIndexes(t *testing.T) {
	place := &Place{
		Root: ident("root"),
		Projections: []PlaceProjection{
			{Kind: PlaceProjectionIndex, Index: ident("first")},
			{Kind: PlaceProjectionIndex, Index: &Unary{Arg: ident("nested")}},
		},
	}
	var names []string
	InspectPlace(place, func(expr Expr) bool {
		if value, ok := expr.(*Ident); ok {
			names = append(names, value.Name)
		}
		return true
	})
	if got, want := strings.Join(names, ","), "root,first,nested"; got != want {
		t.Fatalf("visited identifiers = %q, want %q", got, want)
	}
}

package ir

import (
	"testing"

	"compiler/internal/source"
)

func TestSourceInfoOwnsExpressionOrigin(t *testing.T) {
	location := &source.Location{}
	expr := &IntLit{
		Value:      "1",
		SourceInfo: SourceInfo{NodeID: 7, Location: location},
	}

	if expr.NodeID != 7 || expr.Location != location {
		t.Fatalf("promoted source fields = (%d, %p), want (7, %p)", expr.NodeID, expr.Location, location)
	}
	if got := expr.Origin(); got != expr.SourceInfo {
		t.Fatalf("origin = %#v, want %#v", got, expr.SourceInfo)
	}

	replacement := SourceInfo{NodeID: 9}
	expr.setOrigin(replacement)
	if expr.SourceInfo != replacement {
		t.Fatalf("updated origin = %#v, want %#v", expr.SourceInfo, replacement)
	}
}

func TestWithOriginPreservesTypedNilExpression(t *testing.T) {
	var expr Expr = (*IntLit)(nil)
	if got := WithOrigin(expr, SourceInfo{NodeID: 9}); got != expr {
		t.Fatalf("WithOrigin(typed nil) = %#v, want original typed nil", got)
	}
}

func TestSignatureText(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, Signed: true, Bits: 32})
	u64 := types.Intern(Type{Kind: TypeInteger, Bits: 64})
	callback := types.Intern(Type{Kind: TypeFunction, Params: []TypeID{i32}, Return: i32})
	got := SignatureText(types, []Param{
		{Name: "x", Type: i32},
		{Name: "cb", Type: callback},
	}, u64)
	if got != "(x: i32, cb: fn(i32) -> i32) -> u64" {
		t.Fatalf("signature text mismatch: %q", got)
	}
}

func TestPlaceText(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, Signed: true, Bits: 32})
	array := types.Intern(Type{Kind: TypeArray, Elem: i32, Length: "4"})
	place := &Place{
		Root: &Ident{Name: "xs", Type: array},
		Projections: []PlaceProjection{
			{Kind: PlaceProjectionIndex, Index: &IntLit{Value: "0", Type: i32}, Type: i32},
		},
		Type: i32,
	}
	if got := place.String(); got != "xs[0]" {
		t.Fatalf("place string = %q, want xs[0]", got)
	}
	if got := types.Text(place.Type); got != "i32" {
		t.Fatalf("place type = %q, want i32", got)
	}
}

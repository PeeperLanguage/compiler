package ir

import (
	"testing"
)

func TestSignatureText(t *testing.T) {
	got := SignatureText([]Param{
		{Name: "x", Type: "i32"},
		{Name: "cb", Type: "fn(i32) -> i32"},
	}, "u64")
	if got != "(x: i32, cb: fn(i32) -> i32) -> u64" {
		t.Fatalf("signature text mismatch: %q", got)
	}
}

func TestPlaceText(t *testing.T) {
	place := &Place{
		Root: &Ident{Name: "xs", Type: "[4]i32"},
		Projections: []PlaceProjection{
			{Kind: PlaceProjectionIndex, Index: &IntLit{Value: "0", Type: "i32"}, Type: "i32"},
		},
		Type: "i32",
	}
	if got := place.String(); got != "xs[0]" {
		t.Fatalf("place string = %q, want xs[0]", got)
	}
	if got := place.TypeText(); got != "i32" {
		t.Fatalf("place type = %q, want i32", got)
	}
}

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

func TestIndexExprText(t *testing.T) {
	expr := &Index{
		Base:  &Ident{Name: "xs", Type: "[4]i32"},
		Index: &IntLit{Value: "0", Type: "i32"},
		Type:  "i32",
	}
	if got := expr.String(); got != "xs[0]" {
		t.Fatalf("index string = %q, want xs[0]", got)
	}
	if got := expr.TypeText(); got != "i32" {
		t.Fatalf("index type = %q, want i32", got)
	}
}

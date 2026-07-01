package ir

import (
	"testing"

	"compiler/internal/frontend/ast"
)

func TestTypeTextNamedAndFunction(t *testing.T) {
	fnType := &ast.FuncType{
		Params: []ast.TypeExpr{
			&ast.NamedType{Name: "i32"},
			&ast.NamedType{Name: "bool"},
		},
		Return: &ast.NamedType{Name: "i32"},
	}
	if got := TypeText(&ast.NamedType{Name: "u64"}); got != "u64" {
		t.Fatalf("named type text mismatch: %q", got)
	}
	if got := TypeText(fnType); got != "fn(i32, bool) -> i32" {
		t.Fatalf("func type text mismatch: %q", got)
	}
}

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

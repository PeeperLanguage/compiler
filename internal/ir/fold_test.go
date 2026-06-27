package ir

import "testing"

func TestFoldExprConstantArithmetic(t *testing.T) {
	expr := &Binary{
		Op:   "+",
		Left: &IntLit{Value: "2", Type: "i32"},
		Right: &Binary{
			Op:    "*",
			Left:  &IntLit{Value: "3", Type: "i32"},
			Right: &IntLit{Value: "4", Type: "i32"},
			Type:  "i32",
		},
		Type: "i32",
	}
	folded := FoldExpr(expr, nil)
	lit, ok := folded.(*IntLit)
	if !ok || lit.Value != "14" {
		t.Fatalf("expected 14, got %#v", folded)
	}
}

func TestFoldExprConstantCondition(t *testing.T) {
	expr := &Binary{
		Op:    "<",
		Left:  &IntLit{Value: "1", Type: "i32"},
		Right: &IntLit{Value: "2", Type: "i32"},
		Type:  "bool",
	}
	folded := FoldExpr(expr, nil)
	lit, ok := folded.(*BoolLit)
	if !ok || !lit.Value {
		t.Fatalf("expected true bool literal, got %#v", folded)
	}
}

func TestFoldExprConstEnv(t *testing.T) {
	expr := &Binary{
		Op:    "+",
		Left:  &Ident{Name: "a$1", Type: "i32"},
		Right: &IntLit{Value: "5", Type: "i32"},
		Type:  "i32",
	}
	folded := FoldExpr(expr, map[string]ConstValue{
		"a$1": &IntConst{Value: "2", TypeID: "i32"},
	})
	lit, ok := folded.(*IntLit)
	if !ok || lit.Value != "7" {
		t.Fatalf("expected 7, got %#v", folded)
	}
}

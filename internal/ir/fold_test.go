package ir

import "testing"

func TestFoldExprConstantArithmetic(t *testing.T) {
	expr := &Binary{
		Op:   "+",
		Left: &IntLit{Value: 2},
		Right: &Binary{
			Op:    "*",
			Left:  &IntLit{Value: 3},
			Right: &IntLit{Value: 4},
		},
	}
	folded := FoldExpr(expr)
	lit, ok := folded.(*IntLit)
	if !ok || lit.Value != 14 {
		t.Fatalf("expected 14, got %#v", folded)
	}
}

func TestFoldExprConstantCondition(t *testing.T) {
	expr := &Binary{
		Op:    "<",
		Left:  &IntLit{Value: 1},
		Right: &IntLit{Value: 2},
	}
	folded := FoldExpr(expr)
	lit, ok := folded.(*IntLit)
	if !ok || lit.Value != 1 {
		t.Fatalf("expected true as 1, got %#v", folded)
	}
}

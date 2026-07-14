package constvalue

import "testing"

func TestFoldIntegerBitwiseOperatorsUseFiniteWidth(t *testing.T) {
	tests := []struct {
		name  string
		op    string
		left  *IntConst
		right *IntConst
		want  string
	}{
		{name: "and", op: "&", left: &IntConst{Value: "12", TypeID: "u8"}, right: &IntConst{Value: "10", TypeID: "u8"}, want: "8"},
		{name: "or", op: "|", left: &IntConst{Value: "12", TypeID: "u8"}, right: &IntConst{Value: "10", TypeID: "u8"}, want: "14"},
		{name: "xor", op: "^", left: &IntConst{Value: "12", TypeID: "u8"}, right: &IntConst{Value: "10", TypeID: "u8"}, want: "6"},
		{name: "left shift wraps", op: "<<", left: &IntConst{Value: "127", TypeID: "i8"}, right: &IntConst{Value: "1", TypeID: "i8"}, want: "-2"},
		{name: "signed right shift", op: ">>", left: &IntConst{Value: "-8", TypeID: "i8"}, right: &IntConst{Value: "2", TypeID: "i8"}, want: "-2"},
		{name: "unsigned right shift", op: ">>", left: &IntConst{Value: "128", TypeID: "u8"}, right: &IntConst{Value: "2", TypeID: "u8"}, want: "32"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FoldBinary(tt.op, tt.left, tt.right)
			if !ok {
				t.Fatalf("FoldBinary(%q) failed", tt.op)
			}
			value, ok := got.(*IntConst)
			if !ok || value.Value != tt.want || value.TypeText() != tt.left.TypeText() {
				t.Fatalf("FoldBinary(%q) = %#v, want %s %s", tt.op, got, tt.want, tt.left.TypeText())
			}
		})
	}
}

func TestFoldIntegerComplementUsesFiniteWidth(t *testing.T) {
	tests := []struct {
		value *IntConst
		want  string
	}{
		{value: &IntConst{Value: "0", TypeID: "u8"}, want: "255"},
		{value: &IntConst{Value: "0", TypeID: "i8"}, want: "-1"},
		{value: &IntConst{Value: "127", TypeID: "i8"}, want: "-128"},
		{value: &IntConst{Value: "0", TypeID: "byte"}, want: "255"},
	}
	for _, tt := range tests {
		got, ok := FoldUnary("~", tt.value)
		if !ok {
			t.Fatalf("FoldUnary(~%s) failed", tt.value.TypeText())
		}
		value, ok := got.(*IntConst)
		if !ok || value.Value != tt.want || value.TypeText() != tt.value.TypeText() {
			t.Fatalf("FoldUnary(~%s) = %#v, want %s", tt.value.TypeText(), got, tt.want)
		}
	}
}

func TestFoldIntegerShiftRejectsInvalidCount(t *testing.T) {
	left := &IntConst{Value: "1", TypeID: "u8"}
	for _, right := range []*IntConst{
		{Value: "-1", TypeID: "u8"},
		{Value: "8", TypeID: "u8"},
		{Value: "999999999999999999999", TypeID: "u8"},
	} {
		if got, ok := FoldBinary("<<", left, right); ok || got != nil {
			t.Fatalf("FoldBinary accepted invalid shift %s: %#v", right.Value, got)
		}
	}
}

func TestFoldIntegerShiftNormalizesCountToFiniteWidth(t *testing.T) {
	left := &IntConst{Value: "1", TypeID: "u8"}
	right := &IntConst{Value: "256", TypeID: "u8"}
	got, ok := FoldBinary("<<", left, right)
	value, valueOK := got.(*IntConst)
	if !ok || !valueOK || value.Value != "1" {
		t.Fatalf("FoldBinary did not normalize u8 shift count: %#v", got)
	}
}

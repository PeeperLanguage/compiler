package constvalue

import (
	"math/big"
	"testing"
)

func mustIntConst(t *testing.T, value, typeID string) *IntConst {
	t.Helper()
	out, ok := NewIntText(value, typeID)
	if !ok {
		t.Fatalf("NewIntText(%q, %q) failed", value, typeID)
	}
	return out
}

func mustFloatConst(t *testing.T, value, typeID string) *FloatConst {
	t.Helper()
	out, ok := NewFloatText(value, typeID)
	if !ok {
		t.Fatalf("NewFloatText(%q, %q) failed", value, typeID)
	}
	return out
}

func TestNewIntNormalizesAndClonesValue(t *testing.T) {
	input := big.NewInt(300)
	value, ok := NewInt(input, "u8")
	if !ok || value.Text() != "44" || value.TypeText() != "u8" {
		t.Fatalf("NewInt(300, u8) = %#v, want 44 u8", value)
	}

	input.SetInt64(1)
	if value.Text() != "44" {
		t.Fatalf("NewInt kept mutable input pointer, got %s", value.Text())
	}

	copy := value.Int()
	copy.SetInt64(2)
	if value.Text() != "44" {
		t.Fatalf("Int exposed mutable stored pointer, got %s", value.Text())
	}
}

func TestConstConstructorsRejectInvalidTypeIDs(t *testing.T) {
	if value, ok := NewInt(big.NewInt(1), ""); ok || value != nil {
		t.Fatalf("NewInt accepted empty type: %#v", value)
	}
	if value, ok := NewFloat(1, "i32"); ok || value != nil {
		t.Fatalf("NewFloat accepted non-float type: %#v", value)
	}
	if value, ok := NewString("x", "i32"); ok || value != nil {
		t.Fatalf("NewString accepted non-string type: %#v", value)
	}
}

func TestNewFloatRoundsF32(t *testing.T) {
	value, ok := NewFloat(16777217, "f32")
	if !ok || value.Float() != float64(float32(16777217)) || value.Text() != "1.6777216e+07" {
		t.Fatalf("NewFloat f32 = %#v, want rounded f32", value)
	}
}

func TestTypeTextDoesNotDefaultBrokenIntegerState(t *testing.T) {
	var nilInt *IntConst
	if nilInt.TypeText() != "" {
		t.Fatalf("nil IntConst TypeText = %q, want empty", nilInt.TypeText())
	}

	broken := &IntConst{value: big.NewInt(500)}
	if broken.TypeText() != "" {
		t.Fatalf("broken IntConst TypeText = %q, want empty", broken.TypeText())
	}
}

func TestFoldIntegerBitwiseOperatorsUseFiniteWidth(t *testing.T) {
	tests := []struct {
		name  string
		op    string
		left  *IntConst
		right *IntConst
		want  string
	}{
		{name: "and", op: "&", left: mustIntConst(t, "12", "u8"), right: mustIntConst(t, "10", "u8"), want: "8"},
		{name: "or", op: "|", left: mustIntConst(t, "12", "u8"), right: mustIntConst(t, "10", "u8"), want: "14"},
		{name: "xor", op: "^", left: mustIntConst(t, "12", "u8"), right: mustIntConst(t, "10", "u8"), want: "6"},
		{name: "left shift wraps", op: "<<", left: mustIntConst(t, "127", "i8"), right: mustIntConst(t, "1", "i8"), want: "-2"},
		{name: "signed right shift", op: ">>", left: mustIntConst(t, "-8", "i8"), right: mustIntConst(t, "2", "i8"), want: "-2"},
		{name: "unsigned right shift", op: ">>", left: mustIntConst(t, "128", "u8"), right: mustIntConst(t, "2", "u8"), want: "32"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FoldBinary(tt.op, tt.left, tt.right)
			if !ok {
				t.Fatalf("FoldBinary(%q) failed", tt.op)
			}
			value, ok := got.(*IntConst)
			if !ok || value.Text() != tt.want || value.TypeText() != tt.left.TypeText() {
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
		{value: mustIntConst(t, "0", "u8"), want: "255"},
		{value: mustIntConst(t, "0", "i8"), want: "-1"},
		{value: mustIntConst(t, "127", "i8"), want: "-128"},
		{value: mustIntConst(t, "0", "byte"), want: "255"},
	}
	for _, tt := range tests {
		got, ok := FoldUnary("~", tt.value)
		if !ok {
			t.Fatalf("FoldUnary(~%s) failed", tt.value.TypeText())
		}
		value, ok := got.(*IntConst)
		if !ok || value.Text() != tt.want || value.TypeText() != tt.value.TypeText() {
			t.Fatalf("FoldUnary(~%s) = %#v, want %s", tt.value.TypeText(), got, tt.want)
		}
	}
}

func TestFoldIntegerArithmeticUsesFiniteWidth(t *testing.T) {
	tests := []struct {
		name  string
		op    string
		left  *IntConst
		right *IntConst
		want  string
	}{
		{name: "add wraps", op: "+", left: mustIntConst(t, "127", "i8"), right: mustIntConst(t, "1", "i8"), want: "-128"},
		{name: "sub wraps", op: "-", left: mustIntConst(t, "-128", "i8"), right: mustIntConst(t, "1", "i8"), want: "127"},
		{name: "mul wraps", op: "*", left: mustIntConst(t, "64", "i8"), right: mustIntConst(t, "2", "i8"), want: "-128"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FoldBinary(tt.op, tt.left, tt.right)
			if !ok {
				t.Fatalf("FoldBinary(%q) failed", tt.op)
			}
			value, ok := got.(*IntConst)
			if !ok || value.Text() != tt.want || value.TypeText() != tt.left.TypeText() {
				t.Fatalf("FoldBinary(%q) = %#v, want %s %s", tt.op, got, tt.want, tt.left.TypeText())
			}
		})
	}
}

func TestFoldUnaryMinusUsesFiniteWidth(t *testing.T) {
	got, ok := FoldUnary("-", mustIntConst(t, "-128", "i8"))
	value, valueOK := got.(*IntConst)
	if !ok || !valueOK || value.Text() != "-128" || value.TypeText() != "i8" {
		t.Fatalf("FoldUnary(-i8 min) = %#v, want -128 i8", got)
	}
}

func TestFoldIntegerDivisionUsesTruncTowardZero(t *testing.T) {
	got, ok := FoldBinary("/", mustIntConst(t, "-7", "i32"), mustIntConst(t, "3", "i32"))
	value, valueOK := got.(*IntConst)
	if !ok || !valueOK || value.Text() != "-2" || value.TypeText() != "i32" {
		t.Fatalf("FoldBinary(-7 / 3) = %#v, want -2 i32", got)
	}
}

func TestFoldFloatBinaryRoundsF32(t *testing.T) {
	got, ok := FoldBinary("+", mustFloatConst(t, "16777216", "f32"), mustFloatConst(t, "1", "f32"))
	value, valueOK := got.(*FloatConst)
	if !ok || !valueOK || value.Text() != "1.6777216e+07" || value.TypeText() != "f32" {
		t.Fatalf("FoldBinary(f32 add) = %#v, want 1.6777216e+07 f32", got)
	}
}

func TestFoldIntegerShiftRejectsInvalidCount(t *testing.T) {
	left := mustIntConst(t, "1", "u8")
	for _, right := range []*IntConst{
		mustIntConst(t, "-1", "u8"),
		mustIntConst(t, "8", "u8"),
		mustIntConst(t, "999999999999999999999", "u8"),
	} {
		if got, ok := FoldBinary("<<", left, right); ok || got != nil {
			t.Fatalf("FoldBinary accepted invalid shift %s: %#v", right.Text(), got)
		}
	}
}

func TestFoldIntegerShiftNormalizesCountToFiniteWidth(t *testing.T) {
	left := mustIntConst(t, "1", "u8")
	right := mustIntConst(t, "256", "u8")
	got, ok := FoldBinary("<<", left, right)
	value, valueOK := got.(*IntConst)
	if !ok || !valueOK || value.Text() != "1" {
		t.Fatalf("FoldBinary did not normalize u8 shift count: %#v", got)
	}
}

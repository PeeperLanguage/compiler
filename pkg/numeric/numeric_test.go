package numeric

import "testing"

func TestRegexClassifiers(t *testing.T) {
	if !IsDecimal("123_456") || IsDecimal("0x10") {
		t.Fatalf("decimal classification mismatch")
	}
	if !IsHexadecimal("0x1f") || !IsOctal("0o77") || !IsBinary("0b1010") {
		t.Fatalf("non-decimal classifiers mismatch")
	}
	if !IsFloat("1.25") || !IsFloat("1e3") || IsFloat("1") {
		t.Fatalf("float classification mismatch")
	}
	if !IsValidNumber("0x1f") || !IsValidNumber("0b1010") || !IsValidNumber("1.5e2") {
		t.Fatalf("valid number classification mismatch")
	}
	if IsValidNumber("0b4234") {
		t.Fatalf("expected invalid binary literal rejection")
	}
}

func TestValueHelpers(t *testing.T) {
	if CleanNumberString("1_2_3") != "123" {
		t.Fatalf("CleanNumberString failed")
	}
}

func TestParseLiteral(t *testing.T) {
	tests := []struct {
		input  string
		value  string
		suffix string
	}{
		{"42i32", "42", "i32"},
		{"255u8", "255", "u8"},
		{"2.4f32", "2.4", "f32"},
		{"1e3f64", "1e3", "f64"},
		{"0xffu24", "0xff", "u24"},
		{"1_000i64", "1000", "i64"},
	}
	for _, tt := range tests {
		got, err := ParseLiteral(tt.input)
		if err != nil || got.Value != tt.value || got.ExplicitType != tt.suffix {
			t.Fatalf("ParseLiteral(%q) = %#v, %v", tt.input, got, err)
		}
	}
	for _, input := range []string{"1i", "2.4i32", "0b102u8"} {
		if _, err := ParseLiteral(input); err == nil {
			t.Fatalf("ParseLiteral(%q) accepted invalid literal", input)
		}
	}
}

func TestStringParsingAndFits(t *testing.T) {
	v, err := StringToBigInt("-0x10")
	if err != nil || v.String() != "-16" {
		t.Fatalf("StringToBigInt mismatch: v=%v err=%v", v, err)
	}
	if _, err := StringToBigInt(""); err == nil {
		t.Fatalf("expected error for empty integer")
	}
	if f, err := StringToFloat("1_2.5"); err != nil || f != 12.5 {
		t.Fatalf("StringToFloat mismatch: %v %v", f, err)
	}

	if !FitsIntegerLiteral("127", 8, true) || FitsIntegerLiteral("128", 8, true) {
		t.Fatalf("signed int fit mismatch")
	}
	if !FitsIntegerLiteral("255", 8, false) || FitsIntegerLiteral("-1", 8, false) {
		t.Fatalf("unsigned int fit mismatch")
	}
	if !FitsFloatLiteral("1.0", 32) || !FitsFloatLiteral("1.0", 64) || FitsFloatLiteral("1.0", 16) {
		t.Fatalf("float fit mismatch")
	}
	if !FitsIntegerLiteralInFloat("1", 32) || !FitsIntegerLiteralInFloat("1", 64) || FitsIntegerLiteralInFloat("1", 16) {
		t.Fatalf("integer-in-float fit mismatch")
	}
}

func TestValidateLiteralAndCanonicalizeInteger(t *testing.T) {
	if err := ValidateLiteral("0b1010"); err != nil {
		t.Fatalf("expected valid binary literal, got %v", err)
	}
	if err := ValidateLiteral("1.5e2"); err != nil {
		t.Fatalf("expected valid scientific literal, got %v", err)
	}
	if err := ValidateLiteral("0b4234"); err == nil || err.Error() != "invalid binary literal 0b4234" {
		t.Fatalf("unexpected invalid-binary result: %v", err)
	}
	if err := ValidateLiteral("1.2e+"); err == nil || err.Error() != "invalid float literal 1.2e+" {
		t.Fatalf("unexpected invalid-float result: %v", err)
	}
	if got, err := CanonicalizeIntegerLiteral("0x10"); err != nil || got != "16" {
		t.Fatalf("CanonicalizeIntegerLiteral(0x10) = %q, %v", got, err)
	}
}

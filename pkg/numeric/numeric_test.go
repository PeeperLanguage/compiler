package numeric

import "testing"

func TestSplit(t *testing.T) {
	for _, test := range []struct {
		text string
		want Literal
	}{
		{"42", Literal{10, "42", ""}}, {"42i32", Literal{10, "42", "i32"}}, {"1_000u8", Literal{10, "1_000", "u8"}},
		{"2.5e-3f64", Literal{10, "2.5e-3", "f64"}}, {"0xffu24", Literal{16, "ff", "u24"}}, {"0XAB", Literal{16, "AB", ""}},
		{"0o7i", Literal{8, "7", "i"}}, {"0b1_0", Literal{2, "1_0", ""}}, {"0.5", Literal{10, "0.5", ""}}, {"07", Literal{10, "07", ""}},
	} {
		if got := Split(test.text); got != test.want {
			t.Errorf("Split(%q) = %+v, want %+v", test.text, got, test.want)
		}
	}
}

func TestLiteralQuestions(t *testing.T) {
	for _, test := range []struct {
		text                                           string
		inBase, float, suffixWellFormed, integerSuffix bool
	}{
		{"42", true, false, true, false},
		{"0xFG", false, false, true, false},
		{"0o8", false, false, true, false},
		{"0b102u8", false, false, true, true},
		{"0xu8", false, false, true, true}, // no digits
		{"0x1f_u8", true, false, true, true},
		{"1.5", true, true, true, false},
		{"1e3u8", true, true, true, true},
		{"2.4i32", true, true, true, true},
		{"1.5f32", true, true, true, false},
		{"1i", true, false, false, true},
		{"1i_32", true, false, false, true},
		{"1f", true, false, false, false},
	} {
		l := Split(test.text)
		if l.DigitsInBase() != test.inBase || l.IsFloat() != test.float ||
			l.SuffixWellFormed() != test.suffixWellFormed || l.IntegerSuffix() != test.integerSuffix {
			t.Errorf("%s: in base %v, float %v, suffix well formed %v, integer suffix %v", test.text,
				l.DigitsInBase(), l.IsFloat(), l.SuffixWellFormed(), l.IntegerSuffix())
		}
	}
}

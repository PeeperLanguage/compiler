package numeric

import "testing"

func TestSplit(t *testing.T) {
	for _, test := range []struct {
		text          string
		base          int
		digits, suffx string
	}{
		{"42", 10, "42", ""}, {"42i32", 10, "42", "i32"}, {"1_000u8", 10, "1_000", "u8"},
		{"2.5e-3f64", 10, "2.5e-3", "f64"}, {"0xffu24", 16, "ff", "u24"}, {"0XAB", 16, "AB", ""},
		{"0o7i", 8, "7", "i"}, {"0b1_0", 2, "1_0", ""}, {"0.5", 10, "0.5", ""}, {"07", 10, "07", ""},
	} {
		base, digits, suffix := Split(test.text)
		if base != test.base || digits != test.digits || suffix != test.suffx {
			t.Errorf("Split(%q) = %d %q %q", test.text, base, digits, suffix)
		}
	}
}
